package main

// bpxup setup 交互式引导:必选集(app_key/secret_key/app_name + 设备授权 +
// 第一个项目,项目可跳)→ 可选完整配置(webhook/重试/日志路径)→ 写盘。

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"gopkg.in/yaml.v3"
)

type prompter struct {
	in *bufio.Reader
}

func newPrompter() *prompter {
	return &prompter{in: bufio.NewReader(os.Stdin)}
}

// ask 问一个问题,回车取默认值(默认值为空则要求非空输入,空则重问)。
func (p *prompter) ask(label, def string) string {
	for {
		if def != "" {
			fmt.Printf("%s [%s]: ", label, def)
		} else {
			fmt.Printf("%s: ", label)
		}
		line, err := p.in.ReadString('\n')
		if err != nil && line == "" {
			fmt.Println("\n输入中断,已取消")
			os.Exit(1)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if def != "" {
				return def
			}
			fmt.Println("  不能为空,请重新输入")
			continue
		}
		return line
	}
}

// askSecret 敏感项:不回显已有值(防泄露到屏幕/终端记录);
// 已有值时回车保留原值,否则必须输入。
func (p *prompter) askSecret(label, existing string) string {
	for {
		if existing != "" {
			fmt.Printf("%s(回车保留原值): ", label)
		} else {
			fmt.Printf("%s: ", label)
		}
		line, err := p.in.ReadString('\n')
		if err != nil && line == "" {
			fmt.Println("\n输入中断,已取消")
			os.Exit(1)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if existing != "" {
				return existing
			}
			fmt.Println("  不能为空,请重新输入")
			continue
		}
		return line
	}
}

// askOptional 同 ask,但允许留空(返回空串)。
func (p *prompter) askOptional(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s(可空): ", label)
	}
	line, _ := p.in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// askConfirm 是/否问题,回车取默认。
func (p *prompter) askConfirm(label string, defYes bool) bool {
	hint := "y/N"
	if defYes {
		hint = "Y/n"
	}
	fmt.Printf("%s (%s): ", label, hint)
	line, _ := p.in.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return defYes
	}
	return line == "y" || line == "yes"
}

func (p *prompter) askInt(label string, def int) int {
	for {
		s := p.ask(label, strconv.Itoa(def))
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			fmt.Println("  需要不小于 1 的整数,请重新输入")
			continue
		}
		return n
	}
}

func (p *prompter) askDuration(label, def string) config.Duration {
	for {
		s := p.ask(label, def)
		d, err := time.ParseDuration(s)
		if err != nil {
			fmt.Println("  格式错误,应为 Go duration(如 30s、10m),请重新输入")
			continue
		}
		return config.Duration(d)
	}
}

// askChoice 选项式提问:列出编号选项,输入序号或选项值,回车取默认项。
func (p *prompter) askChoice(label string, options [][2]string, def string) string {
	defIdx := 0
	fmt.Println(label + ":")
	for i, opt := range options {
		if opt[0] == def {
			defIdx = i
		}
	}
	for i, opt := range options {
		mark := "  "
		if i == defIdx {
			mark = " >"
		}
		fmt.Printf("%s %d) %s\n", mark, i+1, opt[1])
	}
	for {
		fmt.Printf("请选择 [回车=%s]: ", options[defIdx][0])
		line, err := p.in.ReadString('\n')
		if err != nil && line == "" {
			fmt.Println("\n输入中断,已取消")
			os.Exit(1)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return options[defIdx][0]
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(options) {
			return options[n-1][0]
		}
		for _, opt := range options {
			if opt[0] == line {
				return opt[0]
			}
		}
		fmt.Println("  无效选择,请输入序号或选项值")
	}
}

// askPath 问本地路径并展开 ~,校验存在;不存在时确认是否仍使用。
func (p *prompter) askPath(label, def string) string {
	for {
		s := p.ask(label, def)
		path := config.NormalizePath(s)
		if _, err := os.Stat(path); err == nil {
			return path
		}
		fmt.Printf("  路径 %s 不存在\n", path)
		if p.askConfirm("  仍使用该路径?", false) {
			return path
		}
	}
}

// resolveConfigPath 与 loadConfig 同优先级,但不要求文件存在(setup 要创建它)。
func resolveConfigPath(fs *flag.FlagSet) string {
	path := fs.Lookup("config").Value.String()
	if path == "" {
		path = os.Getenv("BPXUP_CONFIG")
	}
	if path == "" {
		path = "config.yaml"
	}
	return path
}

func writeConfig(path string, c *config.Config) error {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return err
	}
	enc.Close()
	return os.WriteFile(path, []byte(b.String()), 0o600) // 含 secret_key,限属主读写
}

func cmdSetup(args []string) error {
	fs := newFlagSet("setup", nil)
	fs.Parse(args)
	p := newPrompter()
	cfgPath := resolveConfigPath(fs)

	cfg := &config.Config{}
	if _, err := os.Stat(cfgPath); err == nil {
		// 已有配置:宽松读取作为默认值(不强校验,允许半成品配置继续引导)
		if b, rerr := os.ReadFile(cfgPath); rerr == nil {
			yaml.Unmarshal(b, cfg)
		}
		if !p.askConfirm(fmt.Sprintf("检测到 %s 已存在,是否重新配置?", cfgPath), false) {
			fmt.Println("已取消,原配置未改动")
			return nil
		}
	}

	fmt.Println("== 必选配置 ==")
	cfg.AppKey = p.ask("AppKey(百度网盘开放平台应用 Key)", cfg.AppKey)
	cfg.SecretKey = p.askSecret("SecretKey(应用 Secret)", cfg.SecretKey)
	cfg.AppName = p.askOptional("app_name(网盘里的备份根目录名,留空用 /apps)", defStr(cfg.AppName, "项目数据备份"))

	// 第一个项目(可跳)
	if p.askConfirm("现在配置第一个备份项目?(之后也可手改配置文件)", true) {
		var proj config.Project
		proj.ID = p.ask("项目 id", defStr(firstProjectID(cfg), ""))
		fmt.Println("任务配置:")
		var t config.Task
		t.Name = p.ask("  任务名", defStr(firstTaskName(cfg), "backup"))
		t.Source = p.askPath("  备份源(本地文件或目录)", firstSource(cfg))
		for {
			t.Mode = p.ask("  mode(archive 打包 tar.gz | raw 原样递归)", defStr(t.Mode, "archive"))
			if t.Mode == "archive" || t.Mode == "raw" {
				break
			}
			fmt.Println("  mode 只能是 archive 或 raw")
		}
		t.Retention = p.askInt("  retention(保留最近 N 份)", defInt(firstRetention(cfg), 7))
		for {
			t.DailyAt = p.ask("  daily_at(每日执行时刻 HH:MM,daemon 用)", defStr(firstDailyAt(cfg), "03:00"))
			if _, _, err := config.ParseDailyAt(t.DailyAt); err == nil {
				break
			}
			fmt.Println("  格式应为 HH:MM(如 03:00)")
		}
		proj.Tasks = append(proj.Tasks, t)
		if len(cfg.Projects) > 0 {
			cfg.Projects[0] = proj
		} else {
			cfg.Projects = append(cfg.Projects, proj)
		}
	}

	if err := writeConfig(cfgPath, cfg); err != nil {
		return fmt.Errorf("写配置: %w", err)
	}
	fmt.Printf("配置已写入 %s\n", cfgPath)

	// 设备授权:配置已落盘,失败可单独重跑 bpxup login,setup 也可重跑(有默认值)
	if p.askConfirm("现在进行设备授权?(需要浏览器确认一次)", true) {
		tokenFile := cfg.AccessTokenFile
		if tokenFile == "" {
			tokenFile = "~/.bpxup/token.json"
		}
		pan := baidupan.New(cfg.AppKey, cfg.SecretKey, config.NormalizePath(tokenFile))
		if err := pan.Login(); err != nil {
			return fmt.Errorf("授权失败(配置已保存): %w", err)
		}
		fmt.Println("授权完成")
	} else {
		fmt.Println("已跳过授权,之后执行 bpxup login 完成设备码授权")
	}

	// 可选完整配置
	if p.askConfirm("继续完整配置?(webhook 告警、重试参数、日志路径,均有默认值)", false) {
		fmt.Println("== 可选配置(回车用默认值)==")
		cfg.WebhookURL = p.askOptional("webhook_url(企业微信机器人,留空不通知)", cfg.WebhookURL)
		cfg.NotifyOn = p.askChoice("notify_on(通知时机)", [][2]string{
			{"failure", "failure — 仅失败时通知(默认)"},
			{"all", "all — 成功和失败都通知"},
		}, defStr(cfg.NotifyOn, "failure"))
		cfg.LogFile = p.askOptional("log_file(daemon 日志路径,留空默认 ./bpxup.log)", cfg.LogFile)
		cfg.Retry.MaxAttempts = p.askInt("retry.max_attempts(总尝试次数)", defInt(cfg.Retry.MaxAttempts, 3))
		cfg.Retry.Interval = p.askDuration("retry.interval(run 模式重试间隔)", durStr(cfg.Retry.Interval, "30s"))
		cfg.Retry.DaemonInterval = p.askDuration("retry.daemon_interval(daemon 模式重试间隔)", durStr(cfg.Retry.DaemonInterval, "10m"))
		if err := writeConfig(cfgPath, cfg); err != nil {
			return fmt.Errorf("写配置: %w", err)
		}
		fmt.Printf("配置已更新 %s\n", cfgPath)
	}

	fmt.Println("\n下一步:")
	fmt.Printf("  单次执行:%s run --config %s\n", os.Args[0], cfgPath)
	fmt.Printf("  常驻调度:%s daemon --config %s\n", os.Args[0], cfgPath)
	if len(cfg.Projects) == 0 {
		fmt.Println("  尚未配置项目:编辑配置文件添加 projects 后再执行")
	}
	return nil
}

// ---------- 默认值辅助(沿用已有配置中第一项的值) ----------

func defStr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func defInt(v, def int) int {
	if v >= 1 {
		return v
	}
	return def
}

func durStr(v config.Duration, def string) string {
	if v > 0 {
		return time.Duration(v).String()
	}
	return def
}

func firstProjectID(c *config.Config) string {
	if len(c.Projects) > 0 {
		return c.Projects[0].ID
	}
	return ""
}

func firstTask(c *config.Config) *config.Task {
	if len(c.Projects) > 0 && len(c.Projects[0].Tasks) > 0 {
		return &c.Projects[0].Tasks[0]
	}
	return nil
}

func firstTaskName(c *config.Config) string {
	if t := firstTask(c); t != nil {
		return t.Name
	}
	return ""
}

func firstSource(c *config.Config) string {
	if t := firstTask(c); t != nil {
		return t.Source
	}
	return ""
}

func firstRetention(c *config.Config) int {
	if t := firstTask(c); t != nil {
		return t.Retention
	}
	return 7
}

func firstDailyAt(c *config.Config) string {
	if t := firstTask(c); t != nil && t.DailyAt != "" {
		return t.DailyAt
	}
	return ""
}
