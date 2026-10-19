# daemon 服务化统一 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `bpxup daemon` 一条命令注册系统服务（systemd/launchd/SCM）并后台运行，含配置热重载、validate 指令、成功通知、空文件上传修复与 oauth 超时修复。

**Architecture:** 新增 `internal/servicemgr` 包负责三平台服务注册/停止/卸载（构建标签分文件）；`cmdDaemon` 变为三态分发（服务本体 `--service-run` / 卸载 / 交互式注册）；调度器接管 Notifier 与配置热重载。

**Tech Stack:** Go 标准库（os/exec、signal、golang.org/x/sys/windows/svc 已有依赖）。不引入新依赖。

**Spec:** docs/superpowers/specs/2026-10-19-daemon-service-unify-design.md

## Global Constraints

- 按用户全局规则：**不新增测试用例**；每个任务验证方式为 `go build ./... && go vet ./...`（轻量检查），收尾任务统一跑完整五平台构建与既有测试。
- commit message 用中文，不加任何署名尾注。
- 注释与日志文案用中文，风格与现有代码一致（句号可省、简短）。
- 不访问 `dist/` 目录。
- 服务名统一 `bpxup`；服务启动命令统一 `<exe> daemon --service-run --config <绝对路径>`。
- `--service-run` 为内部标志，不写入 usage 帮助。

---

### Task 1: 空文件上传修复（errno=2）

**Files:**
- Modify: `internal/backup/reconcile.go:25-44`（walkLocal）
- Modify: `internal/backup/reconcile.go:91-123`（syncRaw）
- Modify: `internal/backup/reconcile.go:191-216`（VerifyTask 中两处 walkLocal 调用）

**Interfaces:**
- Produces: `func walkLocal(root string) (map[string]localFile, int, error)` —— 第二返回值为跳过的空文件数。后续无任务依赖它，仅本文件内使用。

- [ ] **Step 1: 修改 walkLocal 签名与实现**

```go
// walkLocal 遍历本地源,收集常规文件(跳过软链等非常规文件,与上传规则一致)。
// 返回第二个值为跳过的空文件数(百度 API 不支持上传空文件,size=0 一律跳过,
// 使上传/复核/verify 对账天然一致)。
func walkLocal(root string) (map[string]localFile, int, error) {
	root = filepath.Clean(root)
	out := map[string]localFile{}
	empty := 0
	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." || fi.IsDir() || !fi.Mode().IsRegular() {
			return nil
		}
		if fi.Size() == 0 {
			empty++
			return nil
		}
		rel = filepath.ToSlash(rel)
		out[rel] = localFile{Rel: rel, Path: path, Size: fi.Size()}
		return nil
	})
	return out, empty, err
}
```

- [ ] **Step 2: syncRaw 适配并输出汇总 Warn**

syncRaw 中 `expect, err := walkLocal(source)` 改为：

```go
	expect, empty, err := walkLocal(source)
	if err != nil {
		return 0, 0, fmt.Errorf("遍历源 %s: %w", source, err)
	}
	if empty > 0 {
		e.Log.Warn("跳过空文件(百度 API 不支持上传空文件)", "数量", empty)
	}
```

- [ ] **Step 3: VerifyTask 适配**

VerifyTask 的 raw 分支中 `expect, err := walkLocal(t.Source)` 改为 `expect, _, err := walkLocal(t.Source)`。

- [ ] **Step 4: 轻量检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出（通过）

- [ ] **Step 5: Commit**

```bash
git add internal/backup/reconcile.go
git commit -m "fix: raw 模式跳过空文件,修复百度 API errno=2 上传失败"
```

---

### Task 2: oauth 三处 HTTP 调用补超时

**Files:**
- Modify: `internal/baidupan/client.go`（新增 httpClient 辅助）
- Modify: `internal/baidupan/oauth.go:77` 、 `oauth.go:113` 、 `oauth.go:156`

**Interfaces:**
- Produces: `func (c *Client) httpClient() *http.Client` —— 惰性初始化共享客户端；后续任务不依赖，仅本包使用。

- [ ] **Step 1: client.go 增加 httpClient**

在 `New` 函数之后新增：

```go
// httpClient 返回共享 HTTP 客户端;测试直接构造 Client{} 时惰性补默认值。
func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 120 * time.Second}
	}
	return c.HTTP
}
```

- [ ] **Step 2: oauth.go 三处替换**

`http.PostForm(...)` → `c.httpClient().PostForm(...)`，共三处（device/code、轮询 token、刷新 token）。URL 与参数不变。

- [ ] **Step 3: 轻量检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出（通过）

- [ ] **Step 4: Commit**

```bash
git add internal/baidupan/client.go internal/baidupan/oauth.go
git commit -m "fix: oauth 三处请求改走共享超时客户端,消除无限挂起"
```

---

### Task 3: notify_on 配置与成功通知

**Files:**
- Modify: `internal/config/config.go`（Config 结构 + Load 校验）
- Modify: `internal/notify/notify.go`（New 签名、Success 方法）
- Modify: `cmd/bpxup/cmds.go:90`（cmdRun 调用点）
- Modify: `cmd/bpxup/cmds.go:220`（cmdDaemon 调用点）
- Modify: `cmd/bpxup/service_windows.go:37`（服务模式调用点）

**Interfaces:**
- Produces: `config.Config.NotifyOn string`（`""`/`"failure"`/`"all"`，Load 后保证非空且合法）
- Produces: `func New(webhook, mode string, log *slog.Logger) *Notifier`
- Produces: `func (n *Notifier) Success(project, task string, files int)` —— mode != "all" 或 webhook 为空时 no-op
- Consumes: Task 5（scheduler）与 Task 9（setup）使用上述签名。

- [ ] **Step 1: config.go 增加字段**

Config 结构体 `WebhookURL` 字段之后新增：

```go
	NotifyOn         string      `yaml:"notify_on,omitempty"` // failure(默认,仅失败) | all(成功和失败)
```

Load 中 `app_key / secret_key` 校验之后新增：

```go
	if c.NotifyOn == "" {
		c.NotifyOn = "failure"
	}
	if c.NotifyOn != "failure" && c.NotifyOn != "all" {
		return nil, fmt.Errorf("notify_on 只能为 failure 或 all")
	}
```

- [ ] **Step 2: notify.go 改造**

Notifier 结构体增加两个字段：

```go
type Notifier struct {
	ch     chan event
	wg     sync.WaitGroup
	log    *slog.Logger
	webhook string
	mode    string
}
```

`New` 改为（`go n.loop(webhook)` 前保存字段）：

```go
// New 创建通知器。webhook 为空返回 no-op;mode: failure 仅失败,all 成功和失败。
func New(webhook, mode string, log *slog.Logger) *Notifier {
	n := &Notifier{log: log, webhook: webhook, mode: mode}
	if webhook == "" {
		return n
	}
	n.ch = make(chan event, 64)
	n.wg.Add(1)
	go n.loop(webhook)
	return n
}
```

`Failure` 方法之后新增：

```go
// Success 上报一条成功事件。不走失败聚合,直接异步发送,不阻塞调度。
func (n *Notifier) Success(project, task string, files int) {
	if n.ch == nil || n.mode != "all" {
		return // webhook 未配置或仅失败模式
	}
	go n.post(n.webhook, successMsg(project, task, files))
}
```

`singleMsg` 之后新增：

```go
func successMsg(project, task string, files int) string {
	return fmt.Sprintf("**bpxup 备份成功**\n> 主机:%s\n> 时间:%s\n> 项目:%s / 任务:%s\n> 文件数:%d",
		hostName(), time.Now().Format("2006-01-02 15:04:05"), project, task, files)
}
```

- [ ] **Step 3: 调用点适配（三处）**

`cmdRun`：`notif := notify.New(cfg.WebhookURL, log)` → `notif := notify.New(cfg.WebhookURL, cfg.NotifyOn, log)`
`cmdDaemon`：同上。
`service_windows.go` Execute 内：同上。

- [ ] **Step 4: 轻量检查（含 Windows 交叉编译）**

Run: `go build ./... && go vet ./... && GOOS=windows go build ./...`
Expected: 无输出（通过）

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/notify/notify.go cmd/bpxup/cmds.go cmd/bpxup/service_windows.go
git commit -m "feat: notify_on 配置(failure/all)与备份成功通知"
```

---

### Task 4: RunTask 返回文件数 + cmdRun 成功通知

**Files:**
- Modify: `internal/backup/backup.go:80-139`（RunTask）
- Modify: `internal/scheduler/scheduler.go:55-56`（调用点，仅改接收，逻辑不动）
- Modify: `cmd/bpxup/cmds.go:109-115`（cmdRun retry 闭包）

**Interfaces:**
- Produces: `func (e *Engine) RunTask(p config.Project, t config.Task, date string) (int, error)` —— 成功返回文件数（archive=1，raw=期望文件总数，含"当日已备份跳过"返回 0 与 nil）
- Consumes: Task 5 scheduler 成功通知使用文件数。

- [ ] **Step 1: RunTask 签名与返回值**

archive 分支成功结尾 `e.Log.Info(...)` 后 `return 1, nil`；"当日已备份,跳过"处 `return 0, nil`；raw 分支 `return total, nil`；所有 `return err` → `return 0, err`。

- [ ] **Step 2: scheduler 调用点适配（仅编译通过，逻辑本任务不动）**

scheduler.go:55-56 改为：

```go
				_, runErr := eng.RunTask(*p, *t, today)
				if runErr != nil {
```

（原 `if err := eng.RunTask(...); err != nil` 结构中 err 变量名与外层重名处按现状保留原名 runErr。）

- [ ] **Step 3: cmdRun 成功通知**

cmdRun 中 retry 闭包改为捕获文件数：

```go
				files := 0
				runErr := retry.Do(cfg.Retry.MaxAttempts, time.Duration(cfg.Retry.Interval), func() error {
					var err error
					files, err = eng.RunTask(*p, *t, date)
					if err != nil {
						log.Warn("备份失败", "项目", p.ID, "任务", t.Name, "err", err)
					}
					return err
				})
```

Cleanup 成功后新增：

```go
				notif.Success(p.ID, t.Name, files)
```

- [ ] **Step 4: 轻量检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出（通过）

- [ ] **Step 5: Commit**

```bash
git add internal/backup/backup.go internal/scheduler/scheduler.go cmd/bpxup/cmds.go
git commit -m "feat: RunTask 返回文件数,run 模式成功通知"
```

---

### Task 5: scheduler 接管 Notifier、ctx 退出与配置热重载

**Files:**
- Modify: `internal/scheduler/scheduler.go`（Run 重写）
- Modify: `cmd/bpxup/cmds.go`（cmdDaemon 适配新签名、configPath 辅助）

**Interfaces:**
- Produces: `func Run(ctx context.Context, cfgPath string, cfg *config.Config, newEngine func(*config.Config) (*backup.Engine, *baidupan.Client), log *slog.Logger) error`
- Produces: `func configPath(fs *flag.FlagSet) string`（cmd 包内，Task 7/8 复用）
- Consumes: Task 3 的 `notify.New(webhook, mode, log)`、`Success`；Task 4 的 `RunTask (int, error)`；cmd 包现有 `newEngine(cfg, log) (*backup.Engine, *baidupan.Client)`。

- [ ] **Step 1: cmds.go 提取 configPath 并适配 cmdDaemon**

loadConfig 上方新增（loadConfig 内改为调用它）：

```go
// configPath 配置路径优先级:--config 参数 > BPXUP_CONFIG 环境变量 > ./config.yaml
func configPath(fs *flag.FlagSet) string {
	path := fs.Lookup("config").Value.String()
	if path == "" {
		path = os.Getenv("BPXUP_CONFIG")
	}
	if path == "" {
		path = "config.yaml"
	}
	return path
}
```

loadConfig 改为 `cfg, err := config.Load(configPath(fs))`，删掉原内联逻辑。

cmdDaemon 改为（本任务为过渡版本，Task 7 重写）：

```go
func cmdDaemon(args []string) error {
	fs := newFlagSet("daemon", nil)
	fs.Parse(args)
	cfg := loadConfig(fs)
	log := setupLogger(cfg, true)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return scheduler.Run(ctx, configPath(fs), cfg,
		func(c *config.Config) (*backup.Engine, *baidupan.Client) { return newEngine(c, log) }, log)
}
```

cmds.go 需要 import `context` 、 `os/signal` 、 `syscall`。同时 cmdDaemon 原来的 `notif := notify.New(...)` / `defer notif.Close()` / `pan.EnsureToken()` 全部删除（已由 scheduler 接管）；cmdRun 的 `eng, pan := newEngine(cfg, log)` 改回 `eng, _ := newEngine(cfg, log)`（run 模式仍需自己的 EnsureToken，保留）。

- [ ] **Step 2: scheduler.Run 重写**

```go
package scheduler

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/backup"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/notify"
)

// Run 常驻调度:每分钟检查各任务是否到达 daily_at,到期执行备份+清理。
// 幂等由远端 _SUCCESS 保证,进程重启安全。
// 配置热重载:每 tick 比对配置文件 mtime,变化则重读;失败沿用旧配置。
// ctx 取消(SIGTERM/SIGINT/服务停止)时优雅退出,退出前 flush 聚合通知。
func Run(ctx context.Context, cfgPath string, cfg *config.Config,
	newEngine func(*config.Config) (*backup.Engine, *baidupan.Client),
	log *slog.Logger) error {

	notif := notify.New(cfg.WebhookURL, cfg.NotifyOn, log)
	defer func() { notif.Close() }() // 闭包引用:热重载重建后关新实例
	eng, pan := newEngine(cfg)
	if err := pan.EnsureToken(); err != nil {
		return err
	}
	log.Info("daemon 已启动")

	type state struct {
		day         string
		lastSuccess string
		givenUp     string
		attempts    int
		nextRetry   time.Time
	}
	states := map[string]*state{}
	lastMod := time.Time{}
	if fi, err := os.Stat(cfgPath); err == nil {
		lastMod = fi.ModTime()
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("daemon 退出")
			return nil
		case now := <-ticker.C:
		}
		if fi, err := os.Stat(cfgPath); err == nil && !fi.ModTime().Equal(lastMod) {
			lastMod = fi.ModTime()
			if nc, lerr := config.Load(cfgPath); lerr != nil {
				log.Error("配置重载失败,沿用旧配置", "err", lerr)
			} else {
				rebuildNotif := nc.WebhookURL != cfg.WebhookURL || nc.NotifyOn != cfg.NotifyOn
				cfg = nc
				eng, pan = newEngine(cfg)
				if rebuildNotif {
					notif.Close()
					notif = notify.New(cfg.WebhookURL, cfg.NotifyOn, log)
				}
				log.Info("配置已重载")
				if terr := pan.EnsureToken(); terr != nil {
					log.Error("token 失效", "err", terr)
				}
			}
		}
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
				files, err := eng.RunTask(*p, *t, today)
				if err != nil {
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
				notif.Success(p.ID, t.Name, files)
				if err := eng.Cleanup(*p, *t); err != nil {
					log.Error("清理失败", "任务", key, "err", err)
				}
			}
		}
	}
}
```

- [ ] **Step 3: 轻量检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出（通过）

- [ ] **Step 4: Commit**

```bash
git add internal/scheduler/scheduler.go cmd/bpxup/cmds.go
git commit -m "feat: 调度器接管通知与配置热重载,支持 ctx 优雅退出"
```

---

### Task 6: internal/servicemgr 包（三平台服务管理）

**Files:**
- Create: `internal/servicemgr/servicemgr.go`
- Create: `internal/servicemgr/systemd.go`（//go:build linux）
- Create: `internal/servicemgr/launchd.go`（//go:build darwin）
- Create: `internal/servicemgr/other.go`（//go:build !windows && !linux && !darwin）
- Create: `internal/servicemgr/windows.go`（//go:build windows）

**Interfaces:**
- Produces:
  - `var ErrNoServiceManager = errors.New("当前环境无 systemd/launchd 服务管理器")`
  - `func Ensure(cfgPath string) (string, error)` —— cfgPath 必须绝对路径；返回人类可读动作（如"服务已注册并启动"）。幂等。
  - `func Stop() error`
  - `func Uninstall() error`
- Consumes: Task 7 的 cmdDaemon/cmdStop 调用。Windows 实现迁移自 `cmd/bpxup/service_windows.go`（该文件 Task 7 才删除）。

- [ ] **Step 1: servicemgr.go 公共入口**

```go
// Package servicemgr 把 bpxup 注册为各平台原生服务并管理生命周期:
// Linux=systemd,macOS=launchd,Windows=SCM;其余环境返回 ErrNoServiceManager。
// 服务启动命令统一为 <exe> daemon --service-run --config <绝对路径>。
package servicemgr

import "errors"

var ErrNoServiceManager = errors.New("当前环境无 systemd/launchd 服务管理器")

// Ensure 注册(如需)并启动服务,幂等:
// 已注册、--config 一致且已运行 → 返回 "服务已在运行",不做任何变更。
func Ensure(cfgPath string) (string, error) { return ensure(cfgPath) }

// Stop 停止服务(保持注册与开机自启)。
func Stop() error { return stop() }

// Uninstall 停止、取消自启并删除注册产物。
func Uninstall() error { return uninstall() }
```

- [ ] **Step 2: systemd.go**

```go
//go:build linux

package servicemgr

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const unitPath = "/etc/systemd/system/bpxup.service"

const unitTmpl = `[Unit]
Description=bpxup 百度网盘备份守护进程
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s daemon --service-run --config %s
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
`

func systemdAvailable() bool {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	_, err := exec.LookPath("systemctl")
	return err == nil
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func isActive() bool {
	out, err := runCmd("systemctl", "is-active", "bpxup")
	return err == nil && strings.TrimSpace(out) == "active"
}

func ensure(cfgPath string) (string, error) {
	if !systemdAvailable() {
		return "", ErrNoServiceManager
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	want := fmt.Sprintf(unitTmpl, exe, cfgPath)
	old, readErr := os.ReadFile(unitPath)
	existed := readErr == nil
	if existed && string(old) == want && isActive() {
		return "服务已在运行", nil
	}
	if err := os.WriteFile(unitPath, []byte(want), 0o644); err != nil {
		return "", fmt.Errorf("写入 unit(需要 root): %w", err)
	}
	rollback := func() {
		if existed {
			os.WriteFile(unitPath, old, 0o644)
		} else {
			os.Remove(unitPath)
		}
		runCmd("systemctl", "daemon-reload")
	}
	if _, err := runCmd("systemctl", "daemon-reload"); err != nil {
		rollback()
		return "", fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if _, err := runCmd("systemctl", "enable", "bpxup"); err != nil {
		rollback()
		return "", fmt.Errorf("设置开机自启(需要 root): %w", err)
	}
	if _, err := runCmd("systemctl", "restart", "bpxup"); err != nil {
		rollback()
		return "", fmt.Errorf("启动服务: %w", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if isActive() {
			return "服务已注册并启动(开机自启)", nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	if out, _ := runCmd("journalctl", "-u", "bpxup", "-n", "20", "--no-pager"); out != "" {
		fmt.Println("服务日志尾部:\n" + out)
	}
	rollback()
	return "", fmt.Errorf("服务启动超时(60s),已回滚本次注册变更")
}

func stop() error {
	if _, err := os.Stat(unitPath); err != nil {
		return fmt.Errorf("服务未注册")
	}
	if _, err := runCmd("systemctl", "stop", "bpxup"); err != nil {
		return fmt.Errorf("停止服务: %w", err)
	}
	return nil
}

func uninstall() error {
	if _, err := os.Stat(unitPath); err != nil {
		return fmt.Errorf("服务未注册")
	}
	runCmd("systemctl", "disable", "--now", "bpxup")
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	_, err := runCmd("systemctl", "daemon-reload")
	return err
}
```

- [ ] **Step 3: launchd.go**

```go
//go:build darwin

package servicemgr

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

const plistPath = "/Library/LaunchDaemons/com.zivyuan.bpxup.plist"
const label = "com.zivyuan.bpxup"

const plistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>--service-run</string>
    <string>--config</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>Crashed</key><true/></dict>
</dict>
</plist>
`

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func loaded() bool {
	return exec.Command("launchctl", "list", label).Run() == nil
}

func ensure(cfgPath string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	want := fmt.Sprintf(plistTmpl, label, exe, cfgPath)
	old, readErr := os.ReadFile(plistPath)
	existed := readErr == nil
	if existed && string(old) == want && loaded() {
		return "服务已在运行", nil
	}
	if err := os.WriteFile(plistPath, []byte(want), 0o644); err != nil {
		return "", fmt.Errorf("写入 plist(需要 root): %w", err)
	}
	rollback := func() {
		runCmd("launchctl", "unload", "-w", plistPath)
		if existed {
			os.WriteFile(plistPath, old, 0o644)
		} else {
			os.Remove(plistPath)
		}
	}
	runCmd("launchctl", "unload", "-w", plistPath) // 已加载时先卸载再载入新配置
	if _, err := runCmd("launchctl", "load", "-w", plistPath); err != nil {
		rollback()
		return "", fmt.Errorf("加载服务: %w", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if loaded() {
			return "服务已注册并启动(开机自启)", nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	rollback()
	return "", fmt.Errorf("服务启动超时(60s),已回滚本次注册变更")
}

func stop() error {
	if _, err := os.Stat(plistPath); err != nil {
		return fmt.Errorf("服务未注册")
	}
	if _, err := runCmd("launchctl", "unload", "-w", plistPath); err != nil {
		return fmt.Errorf("停止服务: %w", err)
	}
	return nil
}

func uninstall() error {
	if _, err := os.Stat(plistPath); err != nil {
		return fmt.Errorf("服务未注册")
	}
	runCmd("launchctl", "unload", "-w", plistPath)
	return os.Remove(plistPath)
}
```

- [ ] **Step 4: other.go**

```go
//go:build !windows && !linux && !darwin

package servicemgr

import "errors"

func ensure(string) (string, error) { return "", ErrNoServiceManager }
func stop() error                   { return errors.New("服务未注册") }
func uninstall() error              { return errors.New("服务未注册") }
```

- [ ] **Step 5: windows.go（迁移自 cmd/bpxup/service_windows.go 的 install/remove/control）**

```go
//go:build windows

package servicemgr

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const svcName = "bpxup"

func serviceArgs(cfgPath string) string {
	return "daemon --service-run --config " + cfgPath
}

func waitState(s *mgr.Service, to svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == to {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待服务状态 %v 超时", to)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func ensure(cfgPath string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	m, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("连接服务管理器(需管理员权限): %w", err)
	}
	defer m.Disconnect()
	want := serviceArgs(cfgPath)

	s, openErr := m.OpenService(svcName)
	if openErr == nil {
		defer s.Close()
		c, err := s.Config()
		if err != nil {
			return "", err
		}
		match := strings.HasSuffix(c.BinaryPathName, want)
		st, _ := s.Query()
		if match && st.State == svc.Running {
			return "服务已在运行", nil
		}
		if !match {
			c.BinaryPathName = `"` + exe + `" ` + want
			if err := s.UpdateConfig(c); err != nil {
				return "", fmt.Errorf("更新服务配置: %w", err)
			}
		}
		if st.State != svc.Running {
			if err := s.Start(); err != nil {
				return "", fmt.Errorf("启动服务: %w", err)
			}
		}
		if err := waitState(s, svc.Running, 60*time.Second); err != nil {
			return "", err
		}
		return "服务已启动(开机自启)", nil
	}
	// 未注册:创建(自动启动+故障恢复)并启动;失败回滚删除
	s, err = m.CreateService(svcName, exe, mgr.Config{
		DisplayName:  "bpxup 百度网盘备份",
		Description:  "基于百度网盘开放平台的定时备份守护进程",
		StartType:    mgr.StartAutomatic,
		Dependencies: []string{"Tcpip"},
	}, "daemon", "--service-run", "--config", cfgPath)
	if err != nil {
		return "", err
	}
	defer s.Close()
	rollback := func() {
		_ = eventlog.Remove(svcName)
		_ = s.Delete()
	}
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400); err != nil {
		rollback()
		return "", err
	}
	_ = eventlog.InstallAsEventCreate(svcName, eventlog.Error|eventlog.Warning|eventlog.Info)
	if err := s.Start(); err != nil {
		rollback()
		return "", fmt.Errorf("启动服务: %w", err)
	}
	if err := waitState(s, svc.Running, 60*time.Second); err != nil {
		rollback()
		return "", err
	}
	return "服务已注册并启动(开机自启)", nil
}

func stop() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器(需管理员权限): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("服务未注册")
	}
	defer s.Close()
	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("停止服务: %w", err)
	}
	return waitState(s, svc.Stopped, 60*time.Second)
}

func uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器(需管理员权限): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("服务未注册")
	}
	defer s.Close()
	_ = eventlog.Remove(svcName)
	return s.Delete()
}
```

- [ ] **Step 6: 轻量检查（本机 + 三平台交叉编译）**

Run: `go build ./... && go vet ./... && GOOS=windows go build ./... && GOOS=linux go build ./... && GOOS=darwin go build ./...`
Expected: 无输出（通过）

- [ ] **Step 7: Commit**

```bash
git add internal/servicemgr
git commit -m "feat: servicemgr 包,三平台系统服务注册/停止/卸载"
```

---

### Task 7: cmdDaemon 重写 + stop 指令 + 删除 service 子命令

**Files:**
- Modify: `cmd/bpxup/cmds.go`（cmdDaemon 重写、新增 cmdStop、删除 cmdService）
- Create: `cmd/bpxup/daemon_windows.go`（//go:build windows，SCM 服务体）
- Delete: `cmd/bpxup/service_windows.go`
- Modify: `cmd/bpxup/main.go`（路由与 usage）

**Interfaces:**
- Consumes: Task 5 的 `scheduler.Run(ctx, cfgPath, cfg, newEngineFn, log)`、`configPath(fs)`；Task 6 的 `servicemgr.Ensure/Stop/Uninstall/ErrNoServiceManager`；Task 3 的 `notify.New(webhook, mode, log)`。
- Produces: `func cmdStop(args []string) error`；`func cmdDaemon(args []string) error` 新行为。

- [ ] **Step 1: cmds.go 重写 cmdDaemon、新增 cmdStop、删除 cmdService**

删除 `cmdService` 函数。cmdDaemon 替换为：

```go
// splitServiceRun 分离内部标志 --service-run(服务管理器拉起时传入,
// 不注册到 FlagSet,避免污染帮助)。
func splitServiceRun(args []string) (bool, []string) {
	rest := make([]string, 0, len(args))
	hit := false
	for _, a := range args {
		if a == "--service-run" {
			hit = true
			continue
		}
		rest = append(rest, a)
	}
	return hit, rest
}

// runForeground 前台调度循环(无服务管理器回退 / --service-run 的 Unix 路径)。
func runForeground(cfgPath string, cfg *config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := setupLogger(cfg, true)
	return scheduler.Run(ctx, cfgPath, cfg,
		func(c *config.Config) (*backup.Engine, *baidupan.Client) { return newEngine(c, log) }, log)
}

func cmdDaemon(args []string) error {
	serviceRun, args := splitServiceRun(args)
	var uninstall bool
	fs := newFlagSet("daemon", func(fs *flag.FlagSet) {
		fs.BoolVar(&uninstall, "uninstall", false, "停止并取消注册系统服务")
	})
	fs.Parse(args)
	cfgPath := configPath(fs)

	if serviceRun {
		return runService(cfgPath) // windows: svc.Run;其余平台: 前台循环
	}
	if uninstall {
		return servicemgr.Uninstall()
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("配置错误(可先执行 bpxup validate 排查): %w", err)
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	action, err := servicemgr.Ensure(abs)
	if errors.Is(err, servicemgr.ErrNoServiceManager) {
		fmt.Println("未检测到 systemd/launchd,以前台模式运行(Ctrl+C 退出)...")
		return runForeground(cfgPath, cfg)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s;日志:%s\n", action, logFilePath(cfg))
	return nil
}

// logFilePath daemon 日志路径(与 setupLogger 同规则)。
func logFilePath(cfg *config.Config) string {
	if cfg.LogFile != "" {
		return cfg.LogFile
	}
	return "bpxup.log"
}

func cmdStop(args []string) error {
	fs := newFlagSet("stop", nil)
	fs.Parse(args)
	if err := servicemgr.Stop(); err != nil {
		return fmt.Errorf("%w;前台模式运行的 daemon 请在对应终端 Ctrl+C", err)
	}
	fmt.Println("daemon 已停止(注册与开机自启保留)")
	return nil
}
```

cmds.go 增加 import `errors` 、 `path/filepath`（若缺）。

- [ ] **Step 2: daemon_windows.go（服务体,迁移自 service_windows.go）**

```go
//go:build windows

package main

import (
	"context"
	"flag"
	"os/signal"
	"syscall"

	"golang.org/x/sys/windows/svc"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/backup"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/scheduler"
)

const svcName = "bpxup"

type bpxupService struct{ cfgPath string }

// runService Windows:由 SCM 拉起,走服务交互;手动运行 --service-run 时
// svc.Run 检测到非 SCM 环境会直接报错。
func runService(cfgPath string) error {
	return svc.Run(svcName, &bpxupService{cfgPath: cfgPath})
}

func (s *bpxupService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return true, 1
	}
	log := setupLogger(cfg, true)
	// 立刻上报 Running,token 校验放进工作协程:
	// EnsureToken 可能发起网络请求,放在上报前会拖过 SCM 的 30 秒连接窗口(1053)。
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := scheduler.Run(ctx, s.cfgPath, cfg,
			func(c *config.Config) (*backup.Engine, *baidupan.Client) { return newEngine(c, log) }, log)
		cancel() // scheduler 内部未再监听信号,这里释放
		done <- err
	}()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done // 等调度器 flush 通知后退出
				return false, 0
			}
		case err := <-done:
			return err == nil, 0
		}
	}
}
```

注意：scheduler.Run 内部没有用 `signal.NotifyContext`（由调用方传 ctx），Task 5 的 runForeground 自行 NotifyContext，此处由 SCM Stop 触发 cancel，语义一致。`flag` 与 `signal`/`syscall` import 如未用到则删掉。

- [ ] **Step 3: 删除 service_windows.go、更新 main.go**

`git rm cmd/bpxup/service_windows.go`

main.go：

```go
	case "daemon":
		err = cmdDaemon(os.Args[2:])
	case "stop":
		err = cmdStop(os.Args[2:])
	case "validate":
		err = cmdValidate(os.Args[2:])
```

删除 `case "service"` 与 `case "-service-run"`（服务本体入口现在走 `daemon --service-run`，由 cmdDaemon 内部分流）。

usage() 命令清单更新为：

```
  login                     设备码授权,获取并保存 token
  setup                     交互式引导:配置 AppKey、第一个项目并完成设备授权
  run                       立即执行备份+清理(适合 cron/Task Scheduler)
  verify                    对账远端备份完整性(--fix 重传修复)
  daemon                    注册为系统服务并后台运行(开机自启;--uninstall 卸载)
  stop                      停止后台 daemon(注册与自启保留)
  validate                  校验配置(静态 + 网盘连通性)
  version                   显示版本
```

（删除 `service` 行。）

- [ ] **Step 4: 轻量检查（本机 + Windows 交叉编译）**

注：cmdValidate 在 Task 8 才实现，本任务 main.go 先不加 `case "validate"`（避免编译错误），usage 中 validate 行也留到 Task 8。仅加 `case "stop"`。

Run: `go build ./... && go vet ./... && GOOS=windows go build ./... && GOOS=linux go build ./...`
Expected: 无输出（通过）

- [ ] **Step 5: Commit**

```bash
git add -A cmd/bpxup
git commit -m "feat: daemon 注册系统服务并后台运行,新增 stop,移除 service 子命令"
```

---

### Task 8: validate 指令

**Files:**
- Create: `cmd/bpxup/validate.go`
- Modify: `cmd/bpxup/main.go`（加 case "validate" 与 usage 行）

**Interfaces:**
- Consumes: `configPath(fs)`、`baidupan.New(appKey, secretKey, tokenFile)`、`pan.EnsureToken()`、`pan.List(dir) ([]Entry, error)`、`cfg.Root()`。
- Produces: `func cmdValidate(args []string) error`

- [ ] **Step 1: validate.go**

```go
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
)

// cmdValidate 校验配置:静态(解析/source 可访问/webhook 格式)+ 连通性
// (token 有效、网盘根目录可访问)。任一失败退出码非 0。
func cmdValidate(args []string) error {
	fs := newFlagSet("validate", nil)
	fs.Parse(args)
	cfgPath := configPath(fs)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Println("✗ 配置解析:", err)
		return err
	}
	fmt.Printf("✓ 配置解析(%s)\n", cfgPath)

	ok := true
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		for j := range p.Tasks {
			t := p.Tasks[j]
			fi, err := os.Stat(t.Source)
			switch {
			case err != nil:
				ok = false
				fmt.Printf("✗ [%s/%s] source 不可访问: %v\n", p.ID, t.Name, err)
			case t.Mode == "raw" && !fi.IsDir():
				ok = false
				fmt.Printf("✗ [%s/%s] raw 模式 source 必须是目录: %s\n", p.ID, t.Name, t.Source)
			default:
				fmt.Printf("✓ [%s/%s] source 可访问\n", p.ID, t.Name)
			}
		}
	}
	if cfg.WebhookURL != "" {
		u, err := url.Parse(cfg.WebhookURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			ok = false
			fmt.Println("✗ webhook_url 不是合法的 http(s) URL")
		} else {
			fmt.Println("✓ webhook_url 格式")
		}
	}

	pan := baidupan.New(cfg.AppKey, cfg.SecretKey, cfg.AccessTokenFile)
	if err := pan.EnsureToken(); err != nil {
		fmt.Println("✗ 网盘授权:", err)
		return errors.New("存在校验失败项")
	}
	fmt.Println("✓ 网盘授权(token 有效)")
	if _, err := pan.List(cfg.Root()); err != nil {
		fmt.Println("✗ 网盘根目录访问:", err)
		return errors.New("存在校验失败项")
	}
	fmt.Printf("✓ 网盘根目录可访问(%s)\n", cfg.Root())

	if !ok {
		return errors.New("存在校验失败项")
	}
	fmt.Println("配置校验全部通过")
	return nil
}
```

- [ ] **Step 2: main.go 路由与 usage**

switch 增加：

```go
	case "validate":
		err = cmdValidate(os.Args[2:])
```

usage() 命令清单中 `stop` 行后加：`  validate                  校验配置(静态 + 网盘连通性)`（Task 7 已按此写则跳过）。

- [ ] **Step 3: 轻量检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出（通过）

- [ ] **Step 4: Commit**

```bash
git add cmd/bpxup/validate.go cmd/bpxup/main.go
git commit -m "feat: validate 指令,启动前校验配置与网盘连通性"
```

---

### Task 9: setup 增加 notify_on 询问

**Files:**
- Modify: `cmd/bpxup/setup.go:246-252`（可选配置块）

**Interfaces:**
- Consumes: Task 3 的 `cfg.NotifyOn` 字段。

- [ ] **Step 1: 可选配置块中 webhook 之后插入**

```go
		for {
			cfg.NotifyOn = p.ask("notify_on(失败才通知=failure | 成功和失败=all)", defStr(cfg.NotifyOn, "failure"))
			if cfg.NotifyOn == "failure" || cfg.NotifyOn == "all" {
				break
			}
			fmt.Println("  只能是 failure 或 all")
		}
```

- [ ] **Step 2: 轻量检查**

Run: `go build ./... && go vet ./...`
Expected: 无输出（通过）

- [ ] **Step 3: Commit**

```bash
git add cmd/bpxup/setup.go
git commit -m "feat: setup 引导增加 notify_on 配置项"
```

---

### Task 10: 收尾完整构建与既有测试

**Files:**
- 无新增修改；仅验证。

- [ ] **Step 1: 五平台交叉编译**

Run:

```bash
cd /Users/zivyuan/GoziLib/baidu-pcs-xbackup && go build ./... && go vet ./... && go test ./...
for t in "linux amd64" "linux arm64" "darwin amd64" "darwin arm64" "windows amd64"; do
  set -- $t; GOOS=$1 GOARCH=$2 go build -o /dev/null ./cmd/bpxup || exit 1
done && echo ALL-PLATFORMS-OK
```

Expected: `ALL-PLATFORMS-OK`，`go test` 全部通过（既有测试）。

- [ ] **Step 2: 人工验收（在 srv-meljen-web,root@10.121.18.201）**

1. 上传新二进制后 `bpxup validate --config ~/.bpxup/config.yaml` → 全 ✓；
2. `bpxup daemon --config ~/.bpxup/config.yaml` → 输出"服务已注册并启动(开机自启)"；`systemctl status bpxup` active；
3. 再跑一次 daemon → "服务已在运行"；
4. 改配置（如 daily_at）→ 1 分钟内日志出现"配置已重载"；
5. `bpxup stop` → 服务停止；`bpxup daemon --uninstall` → unit 移除；
6. 含空文件的 raw 任务 → Warn 跳过空文件，任务成功。

- [ ] **Step 3: 收尾提交（如有零星修正）**

```bash
git add -A && git commit -m "chore: daemon 服务化收尾修正"
```

---

## Self-Review 记录

- spec 覆盖：服务注册三平台(Task 6/7)、幂等与服务内命令(Task 6/7)、无管理器回退(Task 7 runForeground)、stop/uninstall(Task 6/7)、热重载(Task 5)、validate(Task 8)、notify_on/成功通知(Task 3/4/5/9)、空文件(Task 1)、HTTP 超时(Task 2)、优雅退出(Task 5/7)——全覆盖。
- Task 7 与 Task 8 的 main.go 路由依赖已拆开（validate 的 case 在 Task 8 加），每任务均可独立编译。
- Task 6 windows.go 迁移原 installService 的恢复动作与提示语义；`svcName` 常量移入 servicemgr，daemon_windows.go 各自持有（同值）。
