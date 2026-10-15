# 备份失败重试与企业微信通知 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** run 与 daemon 统一为有界重试(耗尽后终止任务等待人工干预),失败通过企业微信 webhook 聚合通知。

**Architecture:** 新增 `internal/retry`(纯重试循环,仅 run 用)与 `internal/notify`(webhook + 60s 聚合通知器)两个小包;config 增加 `webhook_url` 与 `retry` 段;cmds.go 与 scheduler.go 接入。Engine 不动。scheduler 因不能阻塞调度循环,用自身状态机计次实现同样的有界语义。

**Tech Stack:** Go 1.27 标准库(net/http、log/slog、time),gopkg.in/yaml.v3。**零新增依赖。**

**Spec:** docs/superpowers/specs/2026-10-15-retry-notify-design.md

## Global Constraints

- 按用户全局规则 9.1:**不添加测试用例**。每个任务以 `go build ./... && go vet ./...` 验证,最后一个任务做手动验证。
- commit message 用中文,末尾**不加** Co-Authored-By 等任何署名尾注。
- 注释风格:中文、简洁,与现有代码一致(如 `// MkdirAll 带内存缓存:...`)。
- 日志字段用中文键名(如 `"项目"、`"任务"`),与现有代码一致。
- token 与 secret 永不写入日志;webhook URL 含 key,同样**不写入日志**。

---

### Task 1: config 支持 webhook_url 与 retry 段

**Files:**
- Modify: `internal/config/config.go`
- Modify: `config.example.yaml`

**Interfaces:**
- Consumes: 无
- Produces: 后续任务依赖的配置字段
  - `Config.WebhookURL string`
  - `Config.Retry RetryConfig`,其中 `RetryConfig{MaxAttempts int; Interval Duration; DaemonInterval Duration}`
  - `type Duration time.Duration`,带 `UnmarshalYAML`;使用时转回:`time.Duration(cfg.Retry.Interval)`
  - 默认值(在 `Load` 中填充):`MaxAttempts=3`、`Interval=30s`、`DaemonInterval=10m`

- [ ] **Step 1: 在 config.go 增加 Duration 类型与 RetryConfig 结构**

在 `import` 中加 `"time"`。在 `type Task` 上方加:

```go
// Duration 支持 YAML 中 "30s"/"10m" 形式的时长(gopkg.in/yaml.v3 不原生支持 time.Duration)。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("时长应为字符串: %v", err)
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("时长格式错误: %q,应为 Go duration(如 30s、10m)", s)
	}
	*d = Duration(dur)
	return nil
}

// RetryConfig 任务级有界重试配置(run 与 daemon 共用 max_attempts,间隔不同)。
type RetryConfig struct {
	MaxAttempts    int      `yaml:"max_attempts"`    // 总尝试次数(含首次)
	Interval       Duration `yaml:"interval"`        // run 模式重试间隔
	DaemonInterval Duration `yaml:"daemon_interval"` // daemon 模式重试间隔
}
```

- [ ] **Step 2: Config 结构体加字段**

`Config` 结构体中,`LogFile` 之后加:

```go
	WebhookURL      string    `yaml:"webhook_url"` // 企业微信机器人 webhook;空 = 不通知
	Retry           RetryConfig `yaml:"retry"`
```

- [ ] **Step 3: Load 中填默认值并校验**

在 `Load` 的 `c.LogFile = NormalizePath(c.LogFile)` 之后加:

```go
	if c.Retry.MaxAttempts == 0 {
		c.Retry.MaxAttempts = 3
	}
	if c.Retry.MaxAttempts < 0 {
		return nil, fmt.Errorf("retry.max_attempts 必须 ≥ 1")
	}
	if c.Retry.Interval < 0 || c.Retry.DaemonInterval < 0 {
		return nil, fmt.Errorf("retry 间隔不能为负")
	}
	if c.Retry.Interval == 0 {
		c.Retry.Interval = Duration(30 * time.Second)
	}
	if c.Retry.DaemonInterval == 0 {
		c.Retry.DaemonInterval = Duration(10 * time.Minute)
	}
```

- [ ] **Step 4: 更新 config.example.yaml**

在 `log_file` 行之后加:

```yaml
webhook_url: ""                   # 企业微信机器人 webhook;留空 = 不通知
retry:
  max_attempts: 3                 # 总尝试次数(含首次),重试耗尽后终止任务待人工干预
  interval: 30s                   # run 模式重试间隔
  daemon_interval: 10m            # daemon 模式重试间隔
```

- [ ] **Step 5: 构建与静态检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出,退出码 0

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go config.example.yaml
git commit -m "config 新增 webhook_url 与 retry 有界重试配置段"
```

---

### Task 2: internal/retry 包

**Files:**
- Create: `internal/retry/retry.go`

**Interfaces:**
- Consumes: 无(纯标准库)
- Produces: `retry.Do(attempts int, interval time.Duration, fn func() error) error` —— fn 共执行 attempts 次,间隔 interval,全部失败返回最后一次错误;attempts < 1 视为 1;fn 返回 nil 立即返回 nil。**注意:内部 sleep,会阻塞调用方,仅适合 run 这类串行一次性执行;scheduler 不得使用。**

- [ ] **Step 1: 创建 internal/retry/retry.go**

```go
package retry

import "time"

// Do 固定间隔有界重试:fn 共执行 attempts 次,全部失败返回最后一次错误。
// attempts < 1 视为 1。内部 sleep 会阻塞调用方,仅适合 run 等串行一次性执行,
// 常驻调度器(scheduler)应用自身状态机计次,避免卡住其他任务。
func Do(attempts int, interval time.Duration, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(interval)
		}
		if err = fn(); err == nil {
			return nil
		}
	}
	return err
}
```

- [ ] **Step 2: 构建与静态检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出,退出码 0

- [ ] **Step 3: Commit**

```bash
git add internal/retry/retry.go
git commit -m "新增 internal/retry 固定间隔有界重试"
```

---

### Task 3: internal/notify 包(企业微信聚合通知)

**Files:**
- Create: `internal/notify/notify.go`

**Interfaces:**
- Consumes: 无(纯标准库)
- Produces(后续任务依赖的完整 API):
  - `notify.New(webhook string, log *slog.Logger) *Notifier` —— webhook 为空返回 no-op(不启动 goroutine,`Failure`/`Close` 均为空操作)
  - `(*Notifier).Failure(project, task string, attempts int, errStr string)` —— 非阻塞上报失败事件;周期首条立即发送,60s 内后续累计,静默满 60s 发汇总;channel 满时丢弃事件
  - `(*Notifier).Close()` —— 关闭并同步 flush 未发送的累计,可重复调用安全

- [ ] **Step 1: 创建 internal/notify/notify.go**

```go
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	aggWindow   = 60 * time.Second // 聚合窗口:自最后一条失败事件起静默满 60s 发汇总
	sendTimeout = 5 * time.Second
	sendRetries = 2 // 发送失败重试 1 次(共 2 次)
	maxErrLen   = 200
	maxAggItems = 20 // 汇总消息最多列出条数(企业微信 markdown 有长度限制)
)

type event struct {
	project  string
	task     string
	attempts int
	err      string
}

// Notifier 企业微信 webhook 通知器。
// 聚合规则:周期内首条失败立即发送;60s 内后续失败静默累计;
// 自最后一条事件起静默满 60s 发送汇总,之后周期重置(下一条再立即发送)。
// webhook 为空时为 no-op,完全不启动后台 goroutine。
type Notifier struct {
	ch  chan event
	wg  sync.WaitGroup
	log *slog.Logger
}

// New 创建通知器。webhook 为空返回 no-op。
func New(webhook string, log *slog.Logger) *Notifier {
	n := &Notifier{log: log}
	if webhook == "" {
		return n
	}
	n.ch = make(chan event, 64)
	n.wg.Add(1)
	go n.loop(webhook)
	return n
}

// Failure 上报一条失败事件(非阻塞,队列满时丢弃,绝不影响备份流程)。
func (n *Notifier) Failure(project, task string, attempts int, errStr string) {
	if n.ch == nil {
		return // webhook 未配置
	}
	ev := event{project: project, task: task, attempts: attempts, err: truncate(errStr)}
	select {
	case n.ch <- ev:
	default:
	}
}

// Close 停止后台 goroutine 并同步 flush 未发送的累计。
func (n *Notifier) Close() {
	if n.ch == nil {
		return
	}
	close(n.ch)
	n.wg.Wait()
	n.ch = nil // 重复 Close 安全
}

func (n *Notifier) loop(webhook string) {
	defer n.wg.Done()
	var buffer []event
	immediateSent := false // 本周期是否已发过立即通知
	var timerC <-chan time.Time
	var t *time.Timer
	for {
		select {
		case ev, ok := <-n.ch:
			if !ok { // Close:flush 后退出
				if len(buffer) > 0 {
					n.post(webhook, aggregateMsg(buffer))
				}
				return
			}
			if !immediateSent {
				n.post(webhook, singleMsg(ev))
				immediateSent = true
			} else {
				buffer = append(buffer, ev)
			}
			if t != nil {
				t.Stop()
			}
			t = time.NewTimer(aggWindow)
			timerC = t.C
		case <-timerC:
			if len(buffer) > 0 {
				n.post(webhook, aggregateMsg(buffer))
				buffer = nil
			}
			immediateSent = false // 周期重置:下一条失败再立即发送
			timerC = nil
		}
	}
}

// post 发送 markdown 消息,失败重试 1 次;仍失败仅记日志。
func (n *Notifier) post(webhook, content string) {
	payload, _ := json.Marshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": content},
	})
	client := &http.Client{Timeout: sendTimeout}
	var lastErr error
	for attempt := 0; attempt < sendRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		req, err := http.NewRequest(http.MethodPost, webhook, bytes.NewReader(payload))
		if err != nil {
			n.log.Warn("通知请求构造失败", "err", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		var out struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
		}
		json.Unmarshal(body, &out)
		if resp.StatusCode == http.StatusOK && out.ErrCode == 0 {
			return
		}
		lastErr = fmt.Errorf("http=%d errcode=%d %s", resp.StatusCode, out.ErrCode, out.ErrMsg)
	}
	n.log.Warn("webhook 通知发送失败", "err", lastErr)
}

func hostName() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func singleMsg(ev event) string {
	return fmt.Sprintf("**bpxup 备份失败**\n> 主机:%s\n> 时间:%s\n> 项目:%s / 任务:%s\n> 尝试:%d 次\n> 错误:%s",
		hostName(), time.Now().Format("2006-01-02 15:04:05"), ev.project, ev.task, ev.attempts, ev.err)
}

func aggregateMsg(evs []event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**bpxup 备份失败汇总(共 %d 条)**\n> 主机:%s\n> 时间:%s\n",
		len(evs), hostName(), time.Now().Format("2006-01-02 15:04:05"))
	for i, ev := range evs {
		if i >= maxAggItems {
			fmt.Fprintf(&b, "> ……其余 %d 条略\n", len(evs)-maxAggItems)
			break
		}
		fmt.Fprintf(&b, "> %d. %s / %s(%d 次):%s\n", i+1, ev.project, ev.task, ev.attempts, ev.err)
	}
	return b.String()
}

// truncate 截断错误摘要,防止超长错误撑爆消息。
func truncate(s string) string {
	r := []rune(s)
	if len(r) > maxErrLen {
		return string(r[:maxErrLen]) + "…"
	}
	return s
}
```

- [ ] **Step 2: 构建与静态检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出,退出码 0

- [ ] **Step 3: Commit**

```bash
git add internal/notify/notify.go
git commit -m "新增 internal/notify 企业微信 webhook 聚合通知器"
```

---

### Task 4: run 命令接入有界重试与通知

**Files:**
- Modify: `cmd/bpxup/cmds.go`(cmdRun 函数,约 77-122 行)

**Interfaces:**
- Consumes:
  - `retry.Do(attempts int, interval time.Duration, fn func() error) error`(Task 2)
  - `notify.New(webhook string, log *slog.Logger) *Notifier`、`(*Notifier).Failure(project, task string, attempts int, errStr string)`、`(*Notifier).Close()`(Task 3)
  - `cfg.WebhookURL`、`cfg.Retry.MaxAttempts`、`time.Duration(cfg.Retry.Interval)`(Task 1)
- Produces: 无(终端节点)

- [ ] **Step 1: cmdRun 中创建 notifier**

`cmdRun` 中 `eng := newEngine(cfg, log)` 之后、`if err := eng.Pan.EnsureToken(); err != nil` 之前加:

```go
	notif := notify.New(cfg.WebhookURL, log)
	defer notif.Close() // 退出前 flush 未发送的聚合汇总
```

import 增加 `"github.com/zivyuan/baidu-pcs-xbackup/internal/notify"` 与 `"github.com/zivyuan/baidu-pcs-xbackup/internal/retry"`。

- [ ] **Step 2: 任务执行处套 retry.Do 并喂通知**

将 cmdRun 中的:

```go
			matched++
			if err := eng.RunTask(*p, *t, date); err != nil {
				log.Error("备份失败", "项目", p.ID, "任务", t.Name, "err", err)
				failed = true
				continue
			}
```

替换为:

```go
			matched++
			runErr := retry.Do(cfg.Retry.MaxAttempts, time.Duration(cfg.Retry.Interval), func() error {
				err := eng.RunTask(*p, *t, date)
				if err != nil {
					log.Warn("备份失败", "项目", p.ID, "任务", t.Name, "err", err)
				}
				return err
			})
			if runErr != nil {
				log.Error("备份重试耗尽,任务终止,等待人工干预",
					"项目", p.ID, "任务", t.Name, "尝试", cfg.Retry.MaxAttempts, "err", runErr)
				notif.Failure(p.ID, t.Name, cfg.Retry.MaxAttempts, runErr.Error())
				failed = true
				continue
			}
```

- [ ] **Step 3: 构建与静态检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出,退出码 0

- [ ] **Step 4: Commit**

```bash
git add cmd/bpxup/cmds.go
git commit -m "run 命令接入有界重试与企业微信失败通知"
```

---

### Task 5: scheduler 改有界重试 + 当日放弃 + 通知

**Files:**
- Modify: `internal/scheduler/scheduler.go`(整体重写 Run 函数)
- Modify: `cmd/bpxup/cmds.go`(cmdDaemon 函数,约 124-135 行)

**Interfaces:**
- Consumes:
  - `notify.Notifier` 全套 API(Task 3)
  - `cfg.Retry.MaxAttempts`、`time.Duration(cfg.Retry.DaemonInterval)`(Task 1)
- Produces: `scheduler.Run` 新签名 `Run(cfg *config.Config, eng *backup.Engine, log *slog.Logger, notif *notify.Notifier) error`(cmdDaemon 调用)

**关键语义**(与 spec 一致):
- 到期且当日未成功、未放弃、不在退避中 → 执行一次 `RunTask`
- 失败且尝试次数未达 `MaxAttempts` → 记 `nextRetry = now + DaemonInterval`,继续调度
- 失败且达 `MaxAttempts` → `notif.Failure` + 记 `givenUp = today`,**当日不再调度**;次日自然恢复
- 跨天重置尝试计数

- [ ] **Step 1: 重写 scheduler.go**

整体替换为:

```go
package scheduler

import (
	"log/slog"
	"time"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/backup"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/notify"
)

// Run 常驻调度:每分钟检查各任务是否到达 daily_at,到期执行备份+清理。
// 幂等由远端 _SUCCESS 保证,进程重启安全。
// 有界重试:失败按 daemon_interval 退避,达 max_attempts 后当日放弃并通知,
// 等待人工干预(手动 run 或次日自动恢复)。不复用 retry.Do——其内部 sleep
// 会阻塞调度循环,卡住其他任务。
func Run(cfg *config.Config, eng *backup.Engine, log *slog.Logger, notif *notify.Notifier) error {
	type state struct {
		day         string    // 尝试计数所属日期(YYYYMMDD),跨天重置
		lastSuccess string    // YYYYMMDD
		givenUp     string     // 重试耗尽当日放弃的日期(YYYYMMDD)
		attempts    int        // 今日已尝试次数
		nextRetry   time.Time // 失败退避
	}
	states := map[string]*state{} // key: 项目id/任务名
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	log.Info("daemon 已启动")
	for now := range ticker.C {
		for i := range cfg.Projects {
			p := &cfg.Projects[i]
			for j := range p.Tasks {
				t := &p.Tasks[j]
				key := p.ID + "/" + t.Name
				st, ok := states[key]
				if !ok {
					st = &state{}
					states[key] = st
				}
				h, m, err := config.ParseDailyAt(t.DailyAt)
				if err != nil {
					log.Error("daily_at 配置错误", "任务", key, "err", err)
					continue
				}
				today := now.Format("20060102")
				due := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
				if now.Before(due) || st.lastSuccess == today || st.givenUp == today || now.Before(st.nextRetry) {
					continue
				}
				if st.day != today {
					st.day = today
					st.attempts = 0
				}
				st.attempts++
				log.Info("开始备份", "任务", key, "尝试", st.attempts, "共", cfg.Retry.MaxAttempts)
				if err := eng.RunTask(*p, *t, today); err != nil {
					if st.attempts >= cfg.Retry.MaxAttempts {
						log.Error("备份重试耗尽,当日放弃,等待人工干预",
							"任务", key, "尝试", st.attempts, "err", err)
						notif.Failure(p.ID, t.Name, st.attempts, err.Error())
						st.givenUp = today
					} else {
						next := now.Add(time.Duration(cfg.Retry.DaemonInterval))
						log.Error("备份失败,稍后重试", "任务", key, "下次", next.Format("15:04:05"), "err", err)
						st.nextRetry = next
					}
					continue
				}
				st.lastSuccess = today
				if err := eng.Cleanup(*p, *t); err != nil {
					log.Error("清理失败", "任务", key, "err", err)
				}
			}
		}
	}
	return nil
}
```

- [ ] **Step 2: cmdDaemon 接入 notifier**

将 cmdDaemon 中的:

```go
	log := setupLogger(cfg, true)
	eng := newEngine(cfg, log)
	if err := eng.Pan.EnsureToken(); err != nil {
		return err
	}
	return scheduler.Run(cfg, eng, log)
```

替换为:

```go
	log := setupLogger(cfg, true)
	eng := newEngine(cfg, log)
	notif := notify.New(cfg.WebhookURL, log)
	defer notif.Close()
	if err := eng.Pan.EnsureToken(); err != nil {
		return err
	}
	return scheduler.Run(cfg, eng, log, notif)
```

(notify 已在 Task 4 加入 import,无需重复。)

- [ ] **Step 3: 构建与静态检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出,退出码 0

- [ ] **Step 4: Windows 交叉编译检查**

Run: `GOOS=windows GOARCH=amd64 go build ./...`
Expected: 无输出,退出码 0

- [ ] **Step 5: Commit**

```bash
git add internal/scheduler/scheduler.go cmd/bpxup/cmds.go
git commit -m "daemon 改有界重试,耗尽当日放弃并通知,等待人工干预"
```

---

### Task 6: README 更新与手动验证

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: 前 5 个任务的全部产物
- Produces: 无

- [ ] **Step 1: README 配置示例与功能列表更新**

`## 功能` 列表末尾加一行:

```markdown
- 失败有界重试(默认 3 次),耗尽后任务终止并(可选)企业微信 webhook 告警,等待人工干预
```

`## 配置` 的 yaml 示例中,`log_file` 行之后加三行:

```yaml
webhook_url: ""                   # 企业微信机器人 webhook;留空 = 不通知
retry:
  max_attempts: 3                 # 总尝试次数;interval=30s(run)/10m(daemon)可配
```

- [ ] **Step 2: 完整构建检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出,退出码 0

- [ ] **Step 3: 手动验证(需要用户配合,开发机执行)**

用测试 webhook(用户提供,勿写入任何文件):
`https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=41811419-4544-4486-b5d1-1031566f7392`

1. 写临时配置 `retry: {max_attempts: 2, interval: 5s}`,source 指向不存在目录制造失败,`webhook_url` 填测试地址
2. `go build -o /tmp/bpxup ./cmd/bpxup && /tmp/bpxup run --config /tmp/test-config.yaml`
3. 预期:日志出现 2 次"备份失败"+ 1 次"备份重试耗尽";企业微信立即收到首条 markdown 告警(含主机/时间/项目/任务/尝试次数/错误摘要);若多任务失败,60s 后收到汇总;进程退出码 1
4. 再用 `webhook_url: ""` 跑一次:重试照常,无任何通知请求
5. 验证后删除临时文件

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "README 补充失败重试与企业微信告警说明"
```
