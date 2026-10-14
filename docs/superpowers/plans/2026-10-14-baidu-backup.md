# baidu-backup-store 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 基于百度网盘官方开放平台 API 的定时备份工具(Go 单二进制),支持多项目/多任务、archive/raw 两种形态、按任务保留份数、单次与常驻两种运行模式、macOS/Windows/Linux 跨平台。

**Architecture:** CLI `bbs` 分五层:`internal/config`(配置与路径规范化)→ `internal/baidupan`(官方 API 客户端:OAuth 设备码、三步上传、目录/列表/删除)→ `internal/backup`(备份引擎,幂等)→ `internal/retention`(清理)→ `internal/scheduler`(daemon)+ `internal/service`(Windows 服务,构建标签隔离)。

**Tech Stack:** Go 1.22+(仅标准库 + `gopkg.in/yaml.v3` + `golang.org/x/sys`)。

## Global Constraints

- 项目根:`/Users/zivyuan/GoziLib/baidu-backup-store`
- 依赖白名单:`gopkg.in/yaml.v3`(MIT)、`golang.org/x/sys`(BSD);不引入其他第三方库(命令行用标准库 `flag`,不用 cobra)。
- **不编写测试用例**(用户全局规则);每个任务的验证 = `go build ./... && go vet ./...` 通过 + 该任务的手动检查命令。
- 全部构建 `CGO_ENABLED=0` 静态编译。
- git 提交用中文注释,不加任何署名尾注;git 命令一律带 `--no-pager`。
- 远端目录结构:`<remote_root>/<项目id>/<YYYYMMDD>/<任务名>`。
- 设计文档:`docs/superpowers/specs/2026-10-14-baidu-backup-design.md`(本计划已含其一处修订:`remote_root` 为空时用 `/apps/<app_name>`,新增 `app_name` 配置项)。

---

### Task 1: 脚手架与 spec 修订

**Files:**
- Create: `go.mod`、`cmd/bbs/main.go`、`Makefile`
- Modify: `docs/superpowers/specs/2026-10-14-baidu-backup-design.md`(配置模型一节)

**Interfaces:**
- Produces: 可编译的 `bbs` 二进制,子命令分发骨架 `main.go` 中 `main()` + `run(version/login/run/daemon/service)`;后续任务在包内实现,main.go 在 Task 8 完整接线。

- [ ] **Step 1: 修订 spec 配置模型**

在 spec 的配置 YAML 示例中 `secret_key` 之后加一行 `app_name: "我的备份应用"`,并把 `remote_root: ""` 的注释改为 `# 空 = /apps/<app_name,此时 app_name 必填>`。提交。

- [ ] **Step 2: 初始化模块与 main.go**

```bash
cd /Users/zivyuan/GoziLib/baidu-backup-store
go mod init github.com/zivyuan/baidu-backup-store
go get gopkg.in/yaml.v3@latest
```

`cmd/bbs/main.go`:

```go
package main

import (
	"fmt"
	"os"
)

const version = "0.1.0"

func usage() {
	fmt.Fprintf(os.Stderr, `bbs - 百度网盘自动备份工具

用法:
  bbs <命令> [参数]

命令:
  login                     设备码授权,获取并保存 token
  run                       立即执行备份+清理(适合 cron/Task Scheduler)
  daemon                    常驻调度模式
  service <install|uninstall|start|stop>   Windows 服务管理(仅 Windows)
  version                   显示版本
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println("bbs", version)
	case "login":
		err = cmdLogin(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "daemon":
		err = cmdDaemon(os.Args[2:])
	case "service":
		err = cmdService(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}
```

同目录建 `cmds.go`(占位,Task 8 替换为实现):

```go
package main

import "errors"

func cmdLogin(args []string) error  { return errors.New("未实现") }
func cmdRun(args []string) error    { return errors.New("未实现") }
func cmdDaemon(args []string) error { return errors.New("未实现") }
func cmdService(args []string) error { return errors.New("未实现") }
```

- [ ] **Step 3: Makefile**

```makefile
BINARY := bbs
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)
TARGETS := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64 windows-amd64

build: ## 当前平台
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/bbs

$(TARGETS):
	mkdir -p dist
	CGO_ENABLED=0 GOOS=$(@:-%=%) GOARCH=$($(word 2,$(subst -, ,$@))) ... 

```

(上面展开逻辑容易写错,直接用显式规则,最终 Makefile 如下:)

```makefile
BINARY := bbs
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/bbs

dist:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-arm64 ./cmd/bbs
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-amd64 ./cmd/bbs
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-amd64 ./cmd/bbs
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-arm64 ./cmd/bbs
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-windows-amd64.exe ./cmd/bbs

release: dist
	cd dist && tar czf $(BINARY)-darwin-arm64.tar.gz $(BINARY)-darwin-arm64 && rm -rf $(BINARY)-darwin-arm64
	cd dist && tar czf $(BINARY)-darwin-amd64.tar.gz $(BINARY)-darwin-amd64 && rm -rf $(BINARY)-darwin-amd64
	cd dist && tar czf $(BINARY)-linux-amd64.tar.gz $(BINARY)-linux-amd64 && rm -rf $(BINARY)-linux-amd64
	cd dist && tar czf $(BINARY)-linux-arm64.tar.gz $(BINARY)-linux-arm64 && rm -rf $(BINARY)-linux-arm64
	cd dist && zip -q $(BINARY)-windows-amd64.zip $(BINARY)-windows-amd64.exe && rm -rf $(BINARY)-windows-amd64.exe

.PHONY: build dist release
```

- [ ] **Step 4: 验证构建并提交**

```bash
go build ./... && go vet ./... && make build && ./bbs version
```
预期:输出 `bbs 0.1.0`。

```bash
git add -A && git commit -m "脚手架:go 模块、CLI 入口骨架与 Makefile"
```

---

### Task 2: config 包(加载/校验/路径规范化)

**Files:**
- Create: `internal/config/config.go`、`config.yaml`(示例)
- Modify: 无

**Interfaces:**
- Produces:

```go
type Task struct { Name, Source, Mode string; Retention int; DailyAt string }
type Project struct { ID string; Tasks []Task }
type Config struct { AppKey, SecretKey, AppName, AccessTokenFile, RemoteRoot, LogFile string; Projects []Project }
func Load(path string) (*Config, error)          // 读 YAML、填默认值、规范化路径、校验
func NormalizePath(p string) string              // ~ 展开与 /cygdrive/<c> 转换,跨平台
func (c *Config) Root() string                   // RemoteRoot,空则 "/apps/<AppName>"
func ParseDailyAt(s string) (hour, minute int, err error)  // "HH:MM" → 时分
```

- [ ] **Step 1: 实现 config.go**

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Task struct {
	Name      string `yaml:"name"`
	Source    string `yaml:"source"`
	Mode      string `yaml:"mode"`
	Retention int    `yaml:"retention"`
	DailyAt   string `yaml:"daily_at"`
}

type Project struct {
	ID    string `yaml:"id"`
	Tasks []Task `yaml:"tasks"`
}

type Config struct {
	AppKey          string    `yaml:"app_key"`
	SecretKey       string    `yaml:"secret_key"`
	AppName         string    `yaml:"app_name"`
	AccessTokenFile string    `yaml:"access_token_file"`
	RemoteRoot      string    `yaml:"remote_root"`
	LogFile         string    `yaml:"log_file"`
	Projects        []Project `yaml:"projects"`
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// NormalizePath 展开 ~,并把 Cygwin 风格 /cygdrive/<盘符>/... 转为 Windows 路径。
func NormalizePath(p string) string {
	if p == "" {
		return p
	}
	home, _ := os.UserHomeDir()
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	if rest, ok := strings.CutPrefix(p, "/cygdrive/"); ok && len(rest) >= 1 && rest[0] != '/' {
		drive := strings.ToUpper(rest[:1])
		tail := ""
		if len(rest) > 1 && rest[1] == '/' {
			tail = strings.ReplaceAll(rest[2:], "/", string(os.PathSeparator))
		}
		return drive + `:\` + tail
	}
	return p
}

func (c *Config) Root() string {
	if c.RemoteRoot != "" {
		return strings.TrimSuffix(c.RemoteRoot, "/")
	}
	return "/apps/" + c.AppName
}

func ParseDailyAt(s string) (int, int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, 0, fmt.Errorf("daily_at 格式错误: %q,应为 HH:MM", s)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("daily_at 超出范围: %q", s)
	}
	return h, m, nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置: %w", err)
	}
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("解析配置: %w", err)
	}
	if c.AppKey == "" || c.SecretKey == "" {
		return nil, fmt.Errorf("app_key / secret_key 不能为空")
	}
	if c.RemoteRoot == "" && c.AppName == "" {
		return nil, fmt.Errorf("remote_root 为空时必须提供 app_name(应用目录名为 /apps/<app_name>)")
	}
	if c.AccessTokenFile == "" {
		c.AccessTokenFile = "~/.bbs/token.json"
	}
	c.AccessTokenFile = NormalizePath(c.AccessTokenFile)
	c.LogFile = NormalizePath(c.LogFile)
	if len(c.Projects) == 0 {
		return nil, fmt.Errorf("至少需要一个 project")
	}
	for _, p := range c.Projects {
		if !idRe.MatchString(p.ID) {
			return nil, fmt.Errorf("项目 id %q 仅允许字母数字下划线连字符", p.ID)
		}
		if len(p.Tasks) == 0 {
			return nil, fmt.Errorf("项目 %s 至少需要一个任务", p.ID)
		}
		for _, t := range p.Tasks {
			if !idRe.MatchString(t.Name) {
				return nil, fmt.Errorf("任务名 %q 仅允许字母数字下划线连字符", t.Name)
			}
			if t.Source == "" {
				return nil, fmt.Errorf("任务 %s 缺少 source", t.Name)
			}
			t.Source = NormalizePath(t.Source)
			if t.Mode != "archive" && t.Mode != "raw" {
				return nil, fmt.Errorf("任务 %s mode 必须为 archive 或 raw", t.Name)
			}
			if t.Retention < 1 {
				return nil, fmt.Errorf("任务 %s retention 必须 ≥ 1", t.Name)
			}
			if _, _, err := ParseDailyAt(t.DailyAt); err != nil {
				return err
			}
		}
	}
	return c, nil
}
```

注意:`t.Source = NormalizePath(...)` 等修改发生在值拷贝上,循环需改为索引:`for i := range p.Tasks { t := &p.Tasks[i]; ... }`,`for i := range c.Projects { p := &c.Projects[i]; ... }`(上面代码按此落实)。

- [ ] **Step 2: 示例 config.yaml**

```yaml
app_key: "你的AppKey"
secret_key: "你的SecretKey"
app_name: "你的应用名称"          # remote_root 为空时必填,上传根目录为 /apps/<app_name>
access_token_file: "~/.bbs/token.json"
remote_root: ""                   # 留空 = /apps/<app_name>;也可填 /apps/xxx 指定其它目录
log_file: ""                      # daemon 模式默认 ./bbs.log
projects:
  - id: davidoff
    tasks:
      - name: realdata
        source: /data/realdata
        mode: archive              # archive = tar.gz | raw = 原样递归
        retention: 7
        daily_at: "03:00"
```

- [ ] **Step 3: 验证并提交**

```bash
go build ./... && go vet ./...
```
手动检查(临时,不入库):写个 tmp main 或直接依赖 Task 8 的 `bbs run --config` 后再验;本任务只需编译通过。

```bash
git add -A && git commit -m "config 包:配置加载、校验与跨平台路径规范化"
```

---

### Task 3: baidupan — token 存取、设备码登录、自动刷新

**Files:**
- Create: `internal/baidupan/client.go`、`internal/baidupan/oauth.go`

**Interfaces:**
- Consumes: `config.Config` 的 AppKey/SecretKey/AccessTokenFile 字段。
- Produces:

```go
type Client struct { ... }
func New(appKey, secretKey, tokenFile string) *Client
type Token struct { AccessToken, RefreshToken string; ExpiresAt int64 }
func (c *Client) Login() error        // 设备码流程:打印 URL → 轮询 → 存 token(0600)
func (c *Client) EnsureToken() error  // 无 token 报错提示 login;过期自动 refresh 并回存
```

- [ ] **Step 1: client.go(基础结构)**

```go
package baidupan

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	AppKey     string
	SecretKey  string
	TokenFile  string
	HTTP       *http.Client
	token      *Token
}

func New(appKey, secretKey, tokenFile string) *Client {
	return &Client{
		AppKey: appKey, SecretKey: secretKey, TokenFile: tokenFile,
		HTTP: &http.Client{Timeout: 120 * time.Second},
	}
}

// apiError 统一携带百度 errno
type apiError struct {
	API    string
	Errno  int64  `json:"errno"`
	Msg    string `json:"error_msg"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s 失败: errno=%d %s", e.API, e.Errno, e.Msg)
}

// callGET / callPOST 封装:发起请求,把响应 JSON 解到 out;errno!=0 时返回 *apiError
func (c *Client) callGET(out any, api string, params url.Values) error {
	params.Set("access_token", c.token.AccessToken)
	req, err := http.NewRequest("GET", "https://pan.baidu.com"+api+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	return c.do(out, req, api)
}

func (c *Client) callPOST(out any, api string, params url.Values, form url.Values) error {
	params.Set("access_token", c.token.AccessToken)
	req, err := http.NewRequest("POST", "https://pan.baidu.com"+api+"?"+params.Encode(),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(out, req, api)
}

func (c *Client) do(out any, req *http.Request, api string) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s 请求失败: %w", api, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Errno int64           `json:"errno"`
		ErrorMsg string      `json:"error_msg"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Errno != 0 && env.Errno != -6 {
		return &apiError{API: api, Errno: env.Errno, Msg: env.ErrorMsg}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s 响应解析失败: %s", api, string(body))
	}
	return nil
}
```

(imports 补 `io`、`strings`;`-6` 是身份失效,由 EnsureToken 处理重试,此处放行。)

- [ ] **Step 2: oauth.go(token 存取 + 设备码 + 刷新)**

```go
package baidupan

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // unix 秒
}

func (c *Client) loadToken() (*Token, error) {
	if c.token != nil {
		return c.token, nil
	}
	data, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return nil, err
	}
	t := &Token{}
	return t, json.Unmarshal(data, t)
}

func (c *Client) saveToken(t *Token) error {
	if err := os.MkdirAll(filepath.Dir(c.TokenFile), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(t, "", "  ")
	c.token = t
	return os.WriteFile(c.TokenFile, data, 0o600)
}

// Login 设备码授权:打印验证地址,轮询直到用户确认或超时。
func (c *Client) Login() error {
	q := url.Values{
		"client_id": {c.AppKey},
		"scope":     {"basic,netdisk"},
		"response_type": {"device_code"},
	}
	resp, err := http.PostForm("https://openapi.baidu.com/oauth/2.0/device/code", q)
	if err != nil {
		return fmt.Errorf("获取设备码失败: %w", err)
	}
	defer resp.Body.Close()
	var dc struct {
		Errno             int64  `json:"errno"`
		ErrorMsg          string `json:"error_msg"`
		DeviceCode        string `json:"device_code"`
		UserCode          string `json:"user_code"`
		VerificationURL   string `json:"verification_url"`
		ExpiresIn         int    `json:"expires_in"`
		Interval          int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return err
	}
	if dc.Errno != 0 {
		return fmt.Errorf("获取设备码失败: errno=%d %s", dc.Errno, dc.ErrorMsg)
	}
	fmt.Printf("请在浏览器打开: %s\n输入用户码: %s\n确认授权后本程序将自动继续...\n",
		dc.VerificationURL, dc.UserCode)

	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		q := url.Values{
			"grant_type":    {"device_token"},
			"code":          {dc.DeviceCode},
			"client_id":     {c.AppKey},
			"client_secret": {c.SecretKey},
		}
		tr, err := http.PostForm("https://openapi.baidu.com/oauth/2.0/token", q)
		if err != nil {
			continue
		}
		var tk struct {
			AccessToken      string `json:"access_token"`
			RefreshToken     string `json:"refresh_token"`
			ExpiresIn        int64  `json:"expires_in"`
			Error            string `json:"error"`
		}
		json.NewDecoder(tr.Body).Decode(&tk)
		tr.Body.Close()
		if tk.Error == "authorization_pending" {
			continue
		}
		if tk.AccessToken == "" {
			return fmt.Errorf("获取 token 失败: %s", tk.Error)
		}
		t := &Token{
			AccessToken:  tk.AccessToken,
			RefreshToken: tk.RefreshToken,
			ExpiresAt:    time.Now().Unix() + tk.ExpiresIn - 600,
		}
		return c.saveToken(t)
	}
	return fmt.Errorf("授权超时,请重试 bbs login")
}

// EnsureToken 保证持有有效 access_token,过期自动刷新。
func (c *Client) EnsureToken() error {
	t, err := c.loadToken()
	if err != nil {
		return fmt.Errorf("未找到 token,请先执行 bbs login: %w", err)
	}
	if time.Now().Unix() < t.ExpiresAt {
		return nil
	}
	q := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
		"client_id":     {c.AppKey},
		"client_secret": {c.SecretKey},
	}
	resp, err := http.PostForm("https://openapi.baidu.com/oauth/2.0/token", q)
	if err != nil {
		return fmt.Errorf("刷新 token 失败: %w", err)
	}
	defer resp.Body.Close()
	var tk struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		ErrorMsg     string `json:"error_description"`
	}
	json.NewDecoder(resp.Body).Decode(&tk)
	if tk.AccessToken == "" {
		return fmt.Errorf("刷新 token 失败(%s),请重新 bbs login", tk.ErrorMsg)
	}
	return c.saveToken(&Token{
		AccessToken:  tk.AccessToken,
		RefreshToken: tk.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tk.ExpiresIn - 600,
	})
}
```

- [ ] **Step 3: 验证并提交**

```bash
go build ./... && go vet ./...
```

```bash
git add -A && git commit -m "baidupan:token 存取、OAuth 设备码登录与自动刷新"
```

---

### Task 4: baidupan — 目录创建 / 列表 / 删除

**Files:**
- Create: `internal/baidupan/file.go`

**Interfaces:**
- Consumes: Task 3 的 `Client`、`callGET`、`callPOST`。
- Produces:

```go
type Entry struct { Path, Name string; IsDir bool; Size int64 }
func (c *Client) MkdirAll(remoteDir string) error      // 逐级创建,已存在(-8)视为成功
func (c *Client) List(dir string) ([]Entry, error)     // 分页遍历 method=list
func (c *Client) Delete(paths []string) error          // filemanager method=delete
```

- [ ] **Step 1: 实现 file.go**

```go
package baidupan

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type Entry struct {
	Path string
	Name string
	IsDir bool
	Size int64
}

// MkdirAll 逐级创建远端目录(errno -8 已存在视为成功)。
func (c *Client) MkdirAll(remoteDir string) error {
	parts := strings.Split(strings.Trim(remoteDir, "/"), "/")
	cur := ""
	for _, p := range parts {
		cur += "/" + p
		form := url.Values{
			"path":  {cur},
			"isdir": {"1"},
			"size":  {"0"},
			"rtype": {"0"},
		}
		var out struct {
			Errno int64 `json:"errno"`
		}
		err := c.callPOST(&out, "/rest/2.0/xpan/file",
			url.Values{"method": {"create"}}, form)
		if err != nil {
			if ae, ok := err.(*apiError); ok && ae.Errno == -8 {
				continue // 已存在
			}
			return fmt.Errorf("创建目录 %s: %w", cur, err)
		}
	}
	return nil
}

// List 列目录(自动翻页,每页 1000)。
func (c *Client) List(dir string) ([]Entry, error) {
	var all []Entry
	for start := 0; ; start += 1000 {
		params := url.Values{
			"method": {"list"},
			"dir":    {dir},
			"order":  {"name"},
			"start":  {strconv.Itoa(start)},
			"limit":  {"1000"},
		}
		var out struct {
			Errno int64 `json:"errno"`
			List  []struct {
				Path           string `json:"path"`
				ServerFilename string `json:"server_filename"`
				IsDir          int    `json:"isdir"`
				Size           int64  `json:"size"`
			} `json:"list"`
		}
		if err := c.callGET(&out, "/rest/2.0/xpan/file", params); err != nil {
			return nil, fmt.Errorf("列目录 %s: %w", dir, err)
		}
		for _, it := range out.List {
			all = append(all, Entry{Path: it.Path, Name: it.ServerFilename, IsDir: it.IsDir == 1, Size: it.Size})
		}
		if len(out.List) < 1000 {
			return all, nil
		}
	}
}

// Delete 删除文件/目录(目录整体递归删除)。
func (c *Client) Delete(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	j, _ := json.Marshal(paths)
	var out struct {
		Errno int64 `json:"errno"`
		Info  []struct {
			Path  string `json:"path"`
			Errno int64  `json:"errno"`
		} `json:"info"`
	}
	err := c.callPOST(&out, "/rest/2.0/xpan/filemanager",
		url.Values{"method": {"delete"}}, url.Values{"filelist": {string(j)}})
	if err != nil {
		return fmt.Errorf("删除失败 %v: %w", paths, err)
	}
	for _, it := range out.Info {
		if it.Errno != 0 && it.Errno != 12 && it.Errno != -9 { // 12/-9: 不存在,视为已删
			return fmt.Errorf("删除 %s 失败: errno=%d", it.Path, it.Errno)
		}
	}
	return nil
}
```

- [ ] **Step 2: 验证并提交**

```bash
go build ./... && go vet ./...
```

```bash
git add -A && git commit -m "baidupan:目录创建、列目录与文件删除"
```

---

### Task 5: baidupan — 三步上传(precreate / superfile2 / create)

**Files:**
- Create: `internal/baidupan/upload.go`

**Interfaces:**
- Consumes: Task 3 的 `Client`、`EnsureToken`;Task 4 的 `MkdirAll`。
- Produces:

```go
func (c *Client) UploadFile(remotePath, localPath string) error  // 4MB 分片,重试 3 次指数退避,秒传直接返回
```

- [ ] **Step 1: 实现 upload.go**

```go
package baidupan

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const chunkSize = 4 << 20 // 4MB

// UploadFile 官方三步上传:precreate → superfile2 分片 → create。
func (c *Client) UploadFile(remotePath, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()

	// 1. 计算分片 md5
	var md5s []string
	buf := make([]byte, chunkSize)
	for {
		n, rerr := io.ReadFull(f, buf)
		if n > 0 {
			h := md5.Sum(buf[:n])
			md5s = append(md5s, hex.EncodeToString(h[:]))
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if size == 0 {
		md5s = []string{} // 空文件 block_list 为空数组
	}
	blockJSON, _ := json.Marshal(md5s)

	// 2. precreate
	preForm := url.Values{
		"path":       {remotePath},
		"isdir":      {"0"},
		"size":       {fmt.Sprint(size)},
		"autoinit":   {"1"},
		"rtype":      {"3"}, // 同名覆盖
		"block_list": {string(blockJSON)},
	}
	var pre struct {
		Errno      int64  `json:"errno"`
		ErrorMsg   string `json:"error_msg"`
		UploadID   string `json:"uploadid"`
		ReturnType int    `json:"return_type"`
		BlockList  []int  `json:"block_list"`
	}
	if err := c.callPOST(&pre, "/rest/2.0/xpan/file",
		url.Values{"method": {"precreate"}}, preForm); err != nil {
		return fmt.Errorf("预上传 %s: %w", remotePath, err)
	}
	if pre.ReturnType == 1 { // 秒传命中
		return nil
	}
	need := pre.BlockList // 需要实际上传的分片序号
	if len(need) == 0 && size > 0 {
		for i := range md5s {
			need = append(need, i)
		}
	}

	// 3. superfile2 逐分片上传(带重试)
	for _, seq := range need {
		if err := c.uploadChunk(pre.UploadID, seq, localPath); err != nil {
			return fmt.Errorf("分片 %d 上传 %s: %w", seq, remotePath, err)
		}
	}

	// 4. create
createForm := url.Values{
		"path":       {remotePath},
		"isdir":      {"0"},
		"size":       {fmt.Sprint(size)},
		"uploadid":   {pre.UploadID},
		"rtype":      {"3"},
		"block_list": {string(blockJSON)},
	}
	var cr struct {
		Errno      int64  `json:"errno"`
		ErrorMsg   string `json:"error_msg"`
		ReturnType int    `json:"return_type"`
	}
	if err := c.callPOST(&cr, "/rest/2.0/xpan/file",
		url.Values{"method": {"create"}}, createForm); err != nil {
		return fmt.Errorf("创建文件 %s: %w", remotePath, err)
	}
	return nil
}

// uploadChunk 上传单个分片,失败重试 3 次(1s/2s/4s)。
func (c *Client) uploadChunk(uploadID string, seq int, localPath string) error {
	offset := int64(seq) * chunkSize
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<(attempt-1)) * time.Second)
		}
		f, err := os.Open(localPath)
		if err != nil {
			return err
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		chunk := make([]byte, chunkSize)
		n, _ := io.ReadFull(f, chunk)
		f.Close()

		params := url.Values{
			"access_token": {c.token.AccessToken},
			"method":       {"upload"},
			"type":         {"tmpfile"},
			"uploadid":     {uploadID},
			"partseq":      {fmt.Sprint(seq)},
		}
		req, err := http.NewRequest("PUT",
			"https://d.pcs.baidu.com/rest/2.0/pcs/superfile2?"+params.Encode(),
			bytes.NewReader(chunk[:n]))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var out struct {
			Errno int64  `json:"errno"`
			Error string `json:"error_msg"`
		}
		json.Unmarshal(body, &out)
		if resp.StatusCode == 200 && out.Errno == 0 {
			return nil
		}
		lastErr = fmt.Errorf("superfile2 errno=%d http=%d", out.Errno, resp.StatusCode)
	}
	return lastErr
}
```

- [ ] **Step 2: 验证并提交**

```bash
go build ./... && go vet ./...
```

```bash
git add -A && git commit -m "baidupan:官方三步上传(分片、重试、秒传)"
```

---

### Task 6: backup — 备份引擎(archive / raw / 幂等)

**Files:**
- Create: `internal/backup/backup.go`、`internal/backup/archive.go`

**Interfaces:**
- Consumes: `config.Project`/`config.Task`、`baidupan.Client` 全部方法。
- Produces:

```go
type Engine struct { Pan *baidupan.Client; Root string; Log *slog.Logger }
func New(pan *baidupan.Client, root string, log *slog.Logger) *Engine
func (e *Engine) RunTask(p config.Project, t config.Task, date string) error   // 备份单个任务并打 _SUCCESS
func (e *Engine) IsDone(p config.Project, t config.Task, date string) (bool, error)  // 幂等检查
func (e *Engine) Cleanup(p config.Project, t config.Task) error                // 透传 retention 逻辑(Task 7 实现)
```

- [ ] **Step 1: archive.go(tar.gz 打包)**

```go
package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// TarGz 把 src(文件或目录)打包为 dst。目录时保留相对路径,仅归档普通文件。
func TarGz(src, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return writeFile(tw, src, filepath.Base(src))
	}
	root := filepath.Clean(src)
	return filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Mode().IsDir() || !fi.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return writeFile(tw, path, filepath.ToSlash(rel))
	})
}

func writeFile(tw *tar.Writer, path, name string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Format = tar.FormatPAX
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}
```

(`strings` 若未用到则从 imports 去掉。)

- [ ] **Step 2: backup.go(引擎)**

```go
package backup

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/zivyuan/baidu-backup-store/internal/baidupan"
	"github.com/zivyuan/baidu-backup-store/internal/config"
)

type Engine struct {
	Pan  *baidupan.Client
	Root string
	Log  *slog.Logger
}

func New(pan *baidupan.Client, root string, log *slog.Logger) *Engine {
	return &Engine{Pan: pan, Root: root, Log: log}
}

func (e *Engine) dateDir(p config.Project, date string) string {
	return e.Root + "/" + p.ID + "/" + date
}

// IsDone 当日目录已有 _SUCCESS 则视为已完成。
func (e *Engine) IsDone(p config.Project, t config.Task, date string) (bool, error) {
	entries, err := e.Pan.List(e.dateDir(p, date))
	if err != nil {
		return false, nil // 目录不存在等,都视为未完成
	}
	for _, en := range entries {
		if en.Name == "_SUCCESS" {
			return true, nil
		}
	}
	return false, nil
}

// RunTask 执行单个任务的备份。成功路径:dateDir 下出现任务数据与 _SUCCESS。
func (e *Engine) RunTask(p config.Project, t config.Task, date string) error {
	dir := e.dateDir(p, date)
	if done, _ := e.IsDone(p, t, date); done {
		e.Log.Info("当日已备份,跳过", "项目", p.ID, "任务", t.Name)
		return nil
	}
	if err := e.Pan.MkdirAll(dir); err != nil {
		return err
	}
	switch t.Mode {
	case "archive":
		tmp, err := os.CreateTemp("", "bbs-*.tar.gz")
		if err != nil {
			return err
		}
		tmpPath := tmp.Name()
		tmp.Close()
		defer os.Remove(tmpPath)
		if err := TarGz(t.Source, tmpPath); err != nil {
			return fmt.Errorf("打包: %w", err)
		}
		remote := dir + "/" + t.Name + ".tar.gz"
		if err := e.Pan.UploadFile(remote, tmpPath); err != nil {
			return err
		}
		e.Log.Info("archive 上传完成", "项目", p.ID, "任务", t.Name, "远端", remote)
	case "raw":
		if err := e.runRaw(p, t, dir); err != nil {
			return err
		}
	}
	return e.markSuccess(p, t, date, dir)
}

func (e *Engine) runRaw(p config.Project, t config.Task, dir string) error {
	root := filepath.Clean(t.Source)
	var count int
	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if fi.IsDir() {
			return e.Pan.MkdirAll(dir + "/" + t.Name + "/" + rel)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		remote := dir + "/" + t.Name + "/" + rel
		if idx := strings.LastIndex(rel, "/"); idx >= 0 {
			if err := e.Pan.MkdirAll(dir + "/" + t.Name + "/" + rel[:idx]); err != nil {
				return err
			}
		}
		if err := e.Pan.UploadFile(remote, path); err != nil {
			return fmt.Errorf("上传 %s: %w", rel, err)
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	e.Log.Info("raw 上传完成", "项目", p.ID, "任务", t.Name, "文件数", count)
	return nil
}

func (e *Engine) markSuccess(p config.Project, t config.Task, date, dir string) error {
	tmp, err := os.CreateTemp("", "bbs-success-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	io.WriteString(tmp, date+"\n")
	tmp.Close()
	defer os.Remove(tmpPath)
	return e.Pan.UploadFile(dir+"/_SUCCESS", tmpPath)
}
```

`Cleanup` 先留桩(`return nil`),Task 7 实现后改调用 retention:

```go
func (e *Engine) Cleanup(p config.Project, t config.Task) error {
	return retention.Cleanup(e.Pan, e.Root, p, t, e.Log)
}
```

- [ ] **Step 3: 验证并提交**

```bash
go build ./... && go vet ./...
```

```bash
git add -A && git commit -m "backup:备份引擎(打包/原样上传/幂等标志)"
```

---

### Task 7: retention — 过期清理

**Files:**
- Create: `internal/retention/retention.go`
- Modify: `internal/backup/backup.go`(把 Cleanup 桩改为调用 retention)

**Interfaces:**
- Consumes: `baidupan.Client.List/Delete`、`config.Project/Task`、`*slog.Logger`。
- Produces: `func Cleanup(pan *baidupan.Client, root string, p config.Project, t config.Task, log *slog.Logger) error`

- [ ] **Step 1: 实现 retention.go**

```go
package retention

import (
	"log/slog"
	"sort"

	"github.com/zivyuan/baidu-backup-store/internal/baidupan"
	"github.com/zivyuan/baidu-backup-store/internal/config"
)

// Cleanup 保留任务最近 retention 个备份日期目录,更旧的整体删除。
func Cleanup(pan *baidupan.Client, root string, p config.Project, t config.Task, log *slog.Logger) error {
	projDir := root + "/" + p.ID
	entries, err := pan.List(projDir)
	if err != nil {
		return err
	}
	var dates []string
	for _, en := range entries {
		if !en.IsDir || !isDateDir(en.Name) {
			continue
		}
		has, err := taskHasBackup(pan, en.Path, t)
		if err != nil {
			return err
		}
		if has {
			dates = append(dates, en.Name)
		}
	}
	if len(dates) <= t.Retention {
		return nil
	}
	sort.Strings(dates) // YYYYMMDD 字典序即时间序
	toDelete := dates[:len(dates)-t.Retention]
	var paths []string
	for _, d := range toDelete {
		paths = append(paths, projDir+"/"+d)
	}
	if err := pan.Delete(paths); err != nil {
		return err
	}
	log.Info("清理过期备份", "项目", p.ID, "任务", t.Name, "删除目录", paths, "保留", t.Retention)
	return nil
}

// taskHasBackup 判断日期目录中是否存在该任务的数据。
func taskHasBackup(pan *baidupan.Client, datePath string, t config.Task) (bool, error) {
	entries, err := pan.List(datePath)
	if err != nil {
		return false, err
	}
	for _, en := range entries {
		if t.Mode == "archive" && !en.IsDir && en.Name == t.Name+".tar.gz" {
			return true, nil
		}
		if t.Mode == "raw" && en.IsDir && en.Name == t.Name {
			return true, nil
		}
	}
	return false, nil
}

func isDateDir(name string) bool {
	if len(name) != 8 {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: 接通 backup.Cleanup 并验证提交**

```bash
go build ./... && go vet ./...
```

```bash
git add -A && git commit -m "retention:按任务保留 N 份,过期日期目录整体清理"
```

---

### Task 8: scheduler + CLI 接线(login/run/daemon)

**Files:**
- Create: `internal/scheduler/scheduler.go`
- Modify: `cmd/bbs/cmds.go`(替换全部占位实现)

**Interfaces:**
- Consumes: `config.Load`、`baidupan.New`、`backup.New`、`Engine.RunTask/Cleanup/IsDone`。
- Produces: 完整可用的 `bbs login / run / daemon`;`scheduler.Run(cfg, eng, log)`。

- [ ] **Step 1: scheduler.go(每分钟 tick,失败退避 10 分钟)**

```go
package scheduler

import (
	"log/slog"
	"time"

	"github.com/zivyuan/baidu-backup-store/internal/backup"
	"github.com/zivyuan/baidu-backup-store/internal/config"
)

// Run 常驻调度:每分钟检查各任务是否到达 daily_at,到期执行备份+清理。
// 幂等由远端 _SUCCESS 保证,进程重启安全。
func Run(cfg *config.Config, eng *backup.Engine, log *slog.Logger) error {
	type state struct {
		lastSuccess string    // YYYYMMDD
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
				if now.Before(due) || st.lastSuccess == today || now.Before(st.nextRetry) {
					continue
				}
				log.Info("开始备份", "任务", key)
				if err := eng.RunTask(*p, *t, today); err != nil {
					log.Error("备份失败,10 分钟后重试", "任务", key, "err", err)
					st.nextRetry = now.Add(10 * time.Minute)
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

- [ ] **Step 2: cmds.go 实现**

```go
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/zivyuan/baidu-backup-store/internal/backup"
	"github.com/zivyuan/baidu-backup-store/internal/baidupan"
	"github.com/zivyuan/baidu-backup-store/internal/config"
	"github.com/zivyuan/baidu-backup-store/internal/scheduler"
)

func parseConfigCmd(name string, args []string) (*config.Config, *flag.FlagSet) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	cfgPath := fs.String("config", "config.yaml", "配置文件路径")
	fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置错误:", err)
		os.Exit(1)
	}
	return cfg, fs
}

func newEngine(cfg *config.Config, log *slog.Logger) *backup.Engine {
	pan := baidupan.New(cfg.AppKey, cfg.SecretKey, cfg.AccessTokenFile)
	return backup.New(pan, cfg.Root(), log)
}

func setupLogger(cfg *config.Config, daemon bool) *slog.Logger {
	w := os.Stderr
	if daemon {
		path := cfg.LogFile
		if path == "" {
			path = "bbs.log"
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "打开日志文件失败:", err)
			os.Exit(1)
		}
		w = f
	}
	return slog.New(slog.NewTextHandler(w, nil))
}

func cmdLogin(args []string) error {
	cfg, _ := parseConfigCmd("login", args)
	pan := baidupan.New(cfg.AppKey, cfg.SecretKey, cfg.AccessTokenFile)
	return pan.Login()
}

func cmdRun(args []string) error {
	cfg, fs := parseConfigCmd("run", args)
	proj := fs.String("project", "", "只跑指定项目")
	task := fs.String("task", "", "只跑指定任务")
	log := setupLogger(cfg, false)
	eng := newEngine(cfg, log)
	if err := eng.Pan.EnsureToken(); err != nil {
		return err
	}
	date := time.Now().Format("20060102")
	failed := false
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		if *proj != "" && p.ID != *proj {
			continue
		}
		for j := range p.Tasks {
			t := &p.Tasks[j]
			if *task != "" && t.Name != *task {
				continue
			}
			if err := eng.RunTask(*p, *t, date); err != nil {
				log.Error("备份失败", "项目", p.ID, "任务", t.Name, "err", err)
				failed = true
				continue
			}
			if err := eng.Cleanup(*p, *t); err != nil {
				log.Error("清理失败", "项目", p.ID, "任务", t.Name, "err", err)
			}
		}
	}
	if failed {
		return fmt.Errorf("部分任务失败,详见日志")
	}
	return nil
}

func cmdDaemon(args []string) error {
	cfg, _ := parseConfigCmd("daemon", args)
	log := setupLogger(cfg, true)
	eng := newEngine(cfg, log)
	if err := eng.Pan.EnsureToken(); err != nil {
		return err
	}
	return scheduler.Run(cfg, eng, log)
}

func cmdService(args []string) error {
	return serviceDispatch(args) // Task 9:windows 实现,非 windows 桩
}
```

非 Windows 下 `serviceDispatch` 放 `service_stub.go`:

```go
//go:build !windows

package main

import "errors"

func serviceDispatch(args []string) error {
	return errors.New("service 子命令仅支持 Windows")
}
```

- [ ] **Step 3: 验证并提交**

```bash
go build ./... && go vet ./... && make build
./bbs run --config 不存在的.yaml; echo "exit=$?"
```
预期:打印"配置错误"且 exit=1。

```bash
git add -A && git commit -m "CLI 接线:login/run/daemon 与常驻调度器"
```

---

### Task 9: Windows 服务封装

**Files:**
- Create: `cmd/bbs/service_windows.go`
- 已有: `cmd/bbs/service_stub.go`(非 Windows 桩,Task 8 创建)

**Interfaces:**
- Consumes: Task 8 的 `cmdDaemon` 逻辑(服务内直接调 `scheduler.Run`)、`config.Load`。
- Produces: `bbs service install|uninstall|start|stop`(仅 Windows);服务内运行 daemon。

- [ ] **Step 1: service_windows.go**

```go
//go:build windows

package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/zivyuan/baidu-backup-store/internal/backup"
	"github.com/zivyuan/baidu-backup-store/internal/baidupan"
	"github.com/zivyuan/baidu-backup-store/internal/config"
	"github.com/zivyuan/baidu-backup-store/internal/scheduler"
)

const svcName = "bbs"

type bbsService struct{ cfgPath string }

func (s *bbsService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return true, 1
	}
	elog, _ := eventlog.Open(svcName)
	defer elog.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	pan := baidupan.New(cfg.AppKey, cfg.SecretKey, cfg.AccessTokenFile)
	eng := backup.New(pan, cfg.Root(), log)
	done := make(chan error, 1)
	go func() {
		done <- scheduler.Run(cfg, eng, log)
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
				return false, 0
			}
		case err := <-done:
			return err == nil, 0
		}
	}
}

func installService(cfgPath string) error {
	exe, _ := os.Executable()
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err == nil {
		s.Close()
		return fmt.Errorf("服务 %s 已存在,先 uninstall", svcName)
	}
	s, err = m.CreateService(svcName, exe, mgr.Config{
		DisplayName:  "bbs 百度网盘备份",
		Description: "基于百度网盘开放平台的定时备份守护进程",
		StartType:    mgr.StartAutomatic,
		Dependencies: []string{"Tcpip"},
	}, "-service-run", "--config", cfgPath)
	if err != nil {
		return err
	}
	defer s.Close()
	// 故障自动重启
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400)
	return eventlog.InstallAsEventCreate(svcName, eventlog.Error|eventlog.Warning|eventlog.Info)
}

func removeService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("服务 %s 不存在", svcName)
	}
	defer s.Close()
	eventlog.Remove(svcName)
	return s.Delete()
}

func controlService(c svc.Cmd, to svc.State) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("服务 %s 不存在", svcName)
	}
	defer s.Close()
	if err := s.Control(c); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for s.QueryState() != to {
		if time.Now().After(deadline) {
			return fmt.Errorf("等待服务状态 %v 超时", to)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil
}

func serviceDispatch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: bbs service <install|uninstall|start|stop> [--config 路径]")
	}
	// 服务本体入口:scm 以参数 "-service-run --config xxx" 拉起
	if args[0] == "-service-run" {
		fs := flag.NewFlagSet("service-run", flag.ExitOnError)
		cfgPath := fs.String("config", "config.yaml", "")
		fs.Parse(args[1:])
		return svc.Run(svcName, &bbsService{cfgPath: *cfgPath})
	}
	fs := flag.NewFlagSet("service", flag.ExitOnError)
	cfgPath := fs.String("config", "config.yaml", "")
	fs.Parse(args[1:])
	switch args[0] {
	case "install":
		return installService(*cfgPath)
	case "uninstall":
		return removeService()
	case "start":
		return controlService(svc.Start, svc.Running)
	case "stop":
		return controlService(svc.Stop, svc.Stopped)
	}
	return fmt.Errorf("未知子命令: %s", args[0])
}
```

依赖:`go get golang.org/x/sys@latest`。

注意:`serviceDispatch` 在 `service_windows.go` 与 `service_stub.go` 中同名同签名(构建标签互斥),main.go 不变。

- [ ] **Step 2: 双平台编译验证**

```bash
go get golang.org/x/sys@latest
go build ./... && go vet ./...                    # macOS
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...   # Windows(必须通过)
```

- [ ] **Step 3: 提交**

```bash
git add -A && git commit -m "Windows 服务:真服务封装,故障自动重启"
```

---

### Task 10: README、发布与实机验收

**Files:**
- Create: `README.md`

**Interfaces:**
- Consumes: 全部。
- Produces: dist/ 下 5 个平台产物 + 使用文档。

- [ ] **Step 1: README.md**

内容必须覆盖:功能简介、配置字段说明(对齐 Task 2 示例)、首次授权 `bbs login`、单次运行、daemon、四平台自启动示例:

- cron(通用 Unix):`0 3 * * * /usr/local/bin/bbs run --config /etc/bbs/config.yaml`
- systemd unit(仅 daemon 用):
  ```ini
  [Unit]
  Description=bbs 百度网盘备份守护进程
  After=network-online.target

  [Service]
  ExecStart=/usr/local/bin/bbs daemon --config /etc/bbs/config.yaml
  Restart=always
  RestartSec=30

  [Install]
  WantedBy=multi-user.target
  ```
- launchd(macOS,daemon):说明用 `launchctl load` 一个 plist,`ProgramArguments` 指向 `bbs daemon`;
- Windows:`bbs service install --config C:\bbs\config.yaml`(自动开机自启+故障重启)。
- Cygwin 注意事项:配置里路径写 Windows 风格或 `/cygdrive/c/...` 均可(工具自动转换)。

- [ ] **Step 2: 全平台构建**

```bash
make dist && ls dist/
```
预期:5 个二进制齐全。

- [ ] **Step 3: 实机验收(需要用户的 AppKey,按 spec 验收清单执行)**

1. `./bbs login --config config.yaml` → token.json 生成且 `ls -l` 为 0600。
2. 准备测试源目录,`./bbs run` → 网盘核对 `/apps/<应用名>/<项目id>/<今日>/<任务名>` 结构与内容。
3. 再跑一次 → 日志出现"当日已备份,跳过"。
4. retention 临时设为 1 → 第二天备份后确认旧目录被清理(或手工在网盘造一个更早日期目录后跑 run 验证清理)。
5. `./bbs daemon` 前台启动,daily_at 设为 2 分钟后 → 到点自动执行。
6. Windows:dist 的 exe 在 Cygwin shell 与 cmd 各跑一次 `bbs run`;`bbs service install` 后 services.msc 确认服务在跑。
7. Linux 任一发行版:linux-amd64 产物跑 `bbs run` 与 daemon 前台模式。

- [ ] **Step 4: 提交**

```bash
git add -A && git commit -m "文档与发布:README、全平台构建产物"
```

---

## 自检记录

- **Spec 覆盖**:认证(T3)、三步上传+秒传+重试(T5)、archive/raw(T6)、_SUCCESS 幂等(T6)、按任务清理(T7)、run/daemon(T8)、Windows 服务(T9)、cygdrive 转换(T2)、静态编译与五平台发布(T10)、验收清单(T10)。日志脱敏:代码中 slog 只输出项目/任务/远端路径,不输出 token/secret ✓。
- **占位符**:Task 6 的 Cleanup 桩在 Task 7 明确替换;Task 1 的 cmds.go 占位在 Task 8 替换,均为显式两段式,无 TODO。
- **类型一致性**:`Engine.RunTask/Cleanup/IsDone`、`scheduler.Run`、`serviceDispatch` 各任务间签名一致 ✓。
