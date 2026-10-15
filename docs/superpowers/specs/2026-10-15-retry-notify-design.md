# 备份失败重试与企业微信通知 设计文档

日期:2026-10-15
状态:已确认

## 背景与目标

`run` 模式备份失败(如百度网盘 superfile2 http=500)后直接终止,无任务级重试;daemon 模式虽有 10 分钟无限重试,但失败无人知晓。目标:

1. `run` 与 `daemon` 统一为**有界重试**:重试耗尽后终止任务,等待人工干预
2. 失败通过**企业微信 webhook** 通知;未配置 webhook 则跳过
3. 通知带**聚合(防轰炸)**机制

现状基线:

| 层级 | 现状 | 变更 |
|---|---|---|
| 分片级(uploadChunk) | 每分片重试 3 次(1s/2s/4s) | 不动 |
| `run` 任务级 | 无重试 | 新增有界重试(默认 3 次 × 30s) |
| daemon 任务级 | 10 分钟无限重试至当日成功 | 改为有界(默认 3 次 × 10m),耗尽后当日放弃 |
| 通知 | 无 | 新增企业微信 webhook 聚合通知 |

## 配置

```yaml
webhook_url: ""   # 企业微信机器人 webhook;空 = 不通知
retry:
  max_attempts: 3       # 总尝试次数(含首次),run 与 daemon 共用
  interval: 30s         # run 模式重试间隔
  daemon_interval: 10m  # daemon 模式重试间隔
```

- 不配置时使用上述默认值
- 校验:`max_attempts ≥ 1`;间隔为 Go duration 格式(如 `30s`、`10m`)

## 重试语义(run 与 daemon 一致)

- 每个任务独立计数:尝试 → 失败 → 等 interval → 再试,共 `max_attempts` 次
- 全部失败 → 任务终止,产生一条失败事件进入通知
- daemon:终止的任务**当天不再重试**(内存状态标记),次日正常调度
- 人工干预后恢复:手动 `run` 立即补跑,或等次日自动调度。幂等由远端完成标志(archive 文件存在 / raw `_SUCCESS`)保证,现有机制不变

## 通知(internal/notify)

### 发送

- HTTP POST 到 webhook,企业微信 markdown 消息
- 内容:hostname + 本地时间 + 项目/任务名 + 失败次数 + 错误摘要(超长截断)
- webhook 未配置 → Notifier 为 no-op,完全跳过(不建 goroutine、不发请求)
- 发送失败仅记日志,重试 1 次,不影响备份流程

### 聚合(防轰炸)

用户确认的机制:

1. +0s 发生一次失败 → **立即发送**
2. 60 秒内后续失败 → 静默累计
3. 自最后一次失败事件起静默满 60 秒 → 发送一条**汇总**(含累计条数与各自项目/任务/错误摘要),此后周期重置:下一次失败再次立即发送

实现:后台 goroutine + timer;提供 `Close()` 在 `run` 退出前 flush 未发送的累计(run 场景进程要退出,不能丢);daemon 常驻由 goroutine 自然 flush,进程退出时同样 Close。

## 模块与边界

| 单元 | 职责 | 依赖 |
|---|---|---|
| `internal/retry`(新,很小) | `Do(attempts int, interval time.Duration, fn func() error) error`,纯重试循环 | 标准库 |
| `internal/notify`(新) | 聚合 notifier + 企业微信 markdown 发送 | 标准库 |
| `internal/config` | 新增 `webhook_url`、`retry` 段解析/默认值/校验 | yaml |
| `cmd/bpxup/cmds.go` | run 循环套 retry.Do,失败事件喂 notifier,退出前 notify.Close() | retry, notify |
| `internal/scheduler/scheduler.go` | 有界重试 + 当日放弃标记 + 失败事件喂 notifier | retry, notify |

Engine(`internal/backup`)不动:重试策略归调用方,通知归 notify 包,保持边界。

## 数据流(run 模式)

```
RunTask 失败 ──retry.Do 退避重试──> 仍失败
    │
    ├─ 日志:备份失败(含尝试次数)
    └─ notifier.Failure(项目, 任务, 错误摘要)
           │
           ├─ 首条:立即 POST webhook(markdown)
           └─ 60s 内后续:累计 → 静默满 60s → POST 汇总
run 退出前:notify.Close() → flush 未发汇总 → exit code 1
```

daemon 模式相同,只是间隔为 daemon_interval、终止任务当日不再调度。

## 错误处理

- 重试耗尽:任务级终止,**不影响同项目其他任务、其他项目**(现有 continue 语义保留)
- webhook 发送失败:日志 warn,重试 1 次,不阻塞备份
- run 整体退出码:有任务最终失败 → 1(现状不变)
- cleanup 失败:维持现状(仅日志,不重试不通知)

## 测试策略

按全局规则不主动添加测试用例;以 `go vet` + 手动验证为主。手动验证点:

1. 不配 webhook:失败重试后正常退出,无通知路径
2. 配测试 webhook:模拟失败,观察立即通知 + 60s 聚合汇总
3. daemon:任务耗尽后当日不再重试,次日恢复调度
