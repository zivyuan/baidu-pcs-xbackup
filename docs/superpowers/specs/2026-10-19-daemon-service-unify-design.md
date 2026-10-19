# daemon 服务化统一 + 热重载 + validate 设计

日期：2026-10-19
状态：已与用户确认设计，待实施

## 背景

2026-10-19 在新主机（srv-meljen-web）部署时暴露三个问题：

1. **终端静默误导**：`bpxup daemon` 前台常驻且所有日志写入文件，终端零输出，
   用户误以为卡死。用户期望：一条命令注册为系统服务并后台运行。
2. **空文件上传必现失败**：百度 xpan API 不支持创建空文件，size=0 时
   precreate 恒返回 errno=2（参数错误）。`upload.go` 对空文件发空
   `block_list`，导致 raw 任务整批失败重试直到当日放弃。
   （实证：`/www/backup/file_history/.../1770728421` 为 0 字节）
3. **配置无热重载**：daemon 启动后修改 config 不生效，必须重启；
   且无手段在启动前验证配置正确性。

另有一个隐患：`oauth.go` 的 `http.PostForm` 等调用无超时，网络异常时
无限挂起。

## 目标

- `bpxup daemon` 一条命令：注册系统服务（开机自启）+ 后台运行，全平台统一语义，幂等。
- `bpxup stop` 停止后台服务；`bpxup daemon --uninstall` 卸载。
- daemon 运行中自动热重载配置。
- `bpxup validate` 启动前校验配置（静态 + 连通性）。
- 修复空文件上传失败。
- 所有 HTTP 调用有超时。

## 非目标

- 不做 `reload` 子命令 / SIGHUP 通知机制（自动检测替代）。
- 不再保留旧 `service` 子命令（Windows 能力并入 daemon）。

## 设计

### 1. `bpxup daemon`：注册并启动系统服务

交互式运行 `bpxup daemon --config <路径>` 时，按平台注册并启动服务：

| 平台 | 服务机制 | 注册产物 | 权限要求 |
|---|---|---|---|
| Linux | systemd | `/etc/systemd/system/bpxup.service` | root |
| macOS | launchd（系统级 LaunchDaemon） | `/Library/LaunchDaemons/com.zivyuan.bpxup.plist` | root |
| Windows | SCM（复用现有代码） | 服务 `bpxup`，自动启动+故障恢复 | 管理员 |

**共同行为**：

- **无服务管理器兜底（Unix）**：检测不到 systemd（`systemctl` 不存在或
  未运行）/ launchd 时，打印提示：无服务管理器，以前台模式运行（Ctrl+C 退出），
  随后保持现有前台调度逻辑直接运行（不注册、不退出）。

- 服务启动命令统一为 `<exe> daemon --service-run --config <绝对路径>`。
  `--service-run` 为内部标志（不在帮助文档展示）：收到后跳过注册逻辑，
  直接进入调度循环；Windows 下进入现有 `svc.Run` 服务交互。
- **幂等**（重复执行结果一致，无副作用堆积）：
  - 已注册且 `--config` 路径一致且服务 active → 打印"已在运行"，退出 0，不重启；
  - 已注册但 `--config` 路径变化 → 更新注册参数（改写 unit/plist 或服务配置）→ 重启；
  - 已注册但未运行 → 启动；
  - 未注册 → 注册 + enable + 启动。
- **就绪确认与失败报错**：启动后轮询服务状态（`systemctl is-active` /
  SCM 查询 / launchd），超时（60s）或 failed → 打印服务日志尾部与错误，
  退出 1，并回滚本次注册（新注册的卸载；已存在的不动）。
  服务进程自身启动失败（配置坏、token 无效）同样被此机制捕获。
- 成功输出：`daemon 已作为服务运行，开机自启已配置，日志: <log_file>`。

**`bpxup stop`**：按平台停止服务（`systemctl stop` / SCM stop /
launchd unload）。服务仍保持 registered + enabled（下次开机/手动启动恢复）。

**`bpxup daemon --uninstall`**：停止 + disable + 删除注册产物。

**旧 `service` 子命令删除**：`main.go` 的 case 与 `service_windows.go`
的 dispatch 入口移除；install/remove/control 逻辑改造为 daemon 内部函数。

### 2. 配置自动热重载

- `cmdDaemon` 把配置文件路径传入 `scheduler.Run`。
- 每分钟 tick 先 `stat` 配置文件，mtime 变化 → `config.Load` 重读：
  - 成功 → 替换内存配置，记 Info `配置已重载`；webhook_url 变化时
    重建 Notifier（旧的 Close flush 后换新）。
  - 失败 → 记 Error，沿用旧配置，daemon 不退出。
- 任务状态 map 以 `项目id/任务名` 为键跨重载保留：删任务遗留无害，
  改 daily_at 下一 tick 生效。
- Notifier 创建从 cmdDaemon 移入 scheduler.Run（服务模式与自启动路径共用）。

### 3. `bpxup validate [--config <路径>]`

- **静态**（不联网）：
  - 复用 `config.Load` 全部校验（解析/格式/取值/默认值）；
  - 补充：每个任务 source 目录存在且可读；webhook_url 非空时为合法 http(s) URL。
- **连通性**（联网）：
  - `EnsureToken()`（必要时自动刷新，token 缺失时报"先 bpxup login"）；
  - `List` 网盘备份根目录（`cfg.Root()`），验证网盘侧可访问。
- 输出逐项 `✓/✗` 与失败原因；任一失败退出码非 0。

### 4. 空文件上传修复

- `walkLocal`（internal/backup/reconcile.go）跳过 `size==0` 的常规文件，
  任务结束汇总一条 Warn：`跳过 N 个空文件(百度 API 不支持上传空文件)`。
- 放在 walkLocal 层使上传、复核、verify 对账天然一致：空文件不上传、
  也不被误报缺失。
- archive 模式不受影响（tar 打包空文件无问题）。

### 5. HTTP 超时

- internal/baidupan 统一使用共享 `http.Client`：
  - JSON REST 调用（list/precreate/create/oauth 等）：30s 总超时；
  - 分片上传（superfile2，4MB/片）：10 分钟超时（慢上行链路保护），
    失败重试沿用现有退避逻辑。
- 效果：网络异常从"无限挂起"变为"报错进入重试/失败路径"，
  配合服务管理器的失败检测。

### 6. 通知类型配置（成功通知）

- 配置新增 `notify_on`：`failure`（默认，仅失败，兼容现有行为）| `all`（成功和失败）。
- notify 包 event 增加事件类型（失败/成功）：
  - 失败事件维持现有聚合逻辑（立即 + 60s 汇总）不变；
  - 成功事件不走聚合，直接发送：`bpxup 备份成功`，含 项目/任务/文件数。
- `cmdRun` 与 `scheduler` 在任务成功处按 `notify_on` 调用上报；
  `Notifier.New` 接收通知模式，热重载时模式变化随 Notifier 重建生效。
- `setup` 交互配置增加该项询问（默认 failure）。

### 7. 优雅退出

- `scheduler.Run` 增加 context.Context 参数：
  - 服务模式：Windows SCM Stop/Shutdown、systemd SIGTERM 触发 cancel；
  - 退出前 `notif.Close()` flush 聚合通知，之后 return。

## 错误处理汇总

| 场景 | 行为 |
|---|---|
| 服务注册权限不足 | 明确报错（Linux root / Windows 管理员 / macOS root），退出 1 |
| systemd/launchd 不存在 | 打印提示（无服务管理器，以前台模式运行，Ctrl+C 退出），保持现有前台调度逻辑继续运行 |
| 服务启动超时/failed | 打印日志尾部，回滚本次新注册，退出 1 |
| 配置热重载失败 | Error 日志，沿用旧配置 |
| 网络调用超时 | 报错进入既有重试链路 |
| daemon --service-run 被直接手跑（Unix） | 正常进入调度循环（等同旧行为），日志提示 |
| daemon --service-run 被直接手跑（Windows） | svc.Run 检测非 SCM 环境直接报错退出 |

## 验证策略（手动）

1. Linux 主机（srv-meljen-web）：`bpxup daemon` → systemd active +
   开机自启；重复执行 → "已在运行"；改 config → 1 分钟内日志出现"配置已重载"。
2. `bpxup validate`：正确配置 → 全 ✓；改坏 daily_at → ✗ 且退出码非 0。
3. raw 备份含空文件 → Warn 跳过，任务成功，verify 一致。
4. 断网/错误 token → 启动在超时内报错退出，不再无限挂起。
5. `bpxup stop` / `daemon --uninstall` → 服务停止/移除。
6. Windows 交叉构建通过（service 代码并入 daemon 后）。
