package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

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

// MarshalYAML 序列化回 "30s"/"10m" 形式(setup 写配置用,与手写格式一致)。
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// RetryConfig 任务级有界重试配置(run 与 daemon 共用 max_attempts,间隔不同)。
type RetryConfig struct {
	MaxAttempts    int      `yaml:"max_attempts"`    // 总尝试次数(含首次)
	Interval       Duration `yaml:"interval"`        // run 模式重试间隔
	DaemonInterval Duration `yaml:"daemon_interval"` // daemon 模式重试间隔
}

type Task struct {
	Name      string `yaml:"name"`
	Source    string `yaml:"source"`
	Mode      string `yaml:"mode"` // archive | raw
	Retention int    `yaml:"retention"`
	DailyAt   string `yaml:"daily_at"`
}

type Project struct {
	ID    string `yaml:"id"`
	Tasks []Task `yaml:"tasks"`
}

type Config struct {
	AppKey          string      `yaml:"app_key"`
	SecretKey       string      `yaml:"secret_key"`
	AppName         string      `yaml:"app_name,omitempty"` // 备份根目录名(建在网盘根下);空 = /apps
	AccessTokenFile string      `yaml:"access_token_file,omitempty"`
	RemoteRoot      string      `yaml:"remote_root,omitempty"` // 显式指定备份根目录,优先于 app_name
	LogFile         string      `yaml:"log_file,omitempty"`
	WebhookURL      string      `yaml:"webhook_url,omitempty"` // 企业微信机器人 webhook;空 = 不通知
	NotifyOn        string      `yaml:"notify_on,omitempty"`   // failure(默认,仅失败) | all(成功和失败)
	Retry           RetryConfig `yaml:"retry,omitempty"`
	Projects        []Project   `yaml:"projects,omitempty"`
}

var dailyAtRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// validName 校验项目 id / 任务名:允许网盘合法目录名(中文、空格、符号等),
// 仅禁止路径分隔符、控制字符、"."/".." 与空名、超长名。
func validName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s 不能为空", kind)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%s %q 非法", kind, name)
	}
	if len(name) > 255 {
		return fmt.Errorf("%s %q 超过 255 字节", kind, name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%s %q 不能包含 / 或 \\", kind, name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s %q 不能包含控制字符", kind, name)
		}
	}
	return nil
}

// NormalizePath 展开 ~,并把 Cygwin 风格 /cygdrive/<盘符>/... 转为 Windows 路径。
// 取不到 HOME 时原样返回(保留 ~/ 前缀,让后续 open 报出可读的错误,
// 而不是静默拼出错误的相对路径——systemd 服务环境默认无 HOME)。
func NormalizePath(p string) string {
	if p == "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
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

// Root 返回远端备份根目录,优先级:remote_root > app_name(建在网盘根下,
// 如 /项目数据备份)> /apps(保留容器目录,已存在无需创建)。
func (c *Config) Root() string {
	if r := strings.TrimSuffix(c.RemoteRoot, "/"); r != "" {
		return r
	}
	if c.AppName != "" {
		return "/" + c.AppName
	}
	return "/apps"
}

// ParseDailyAt 解析 24 小时制 "HH:MM"。
func ParseDailyAt(s string) (int, int, error) {
	if !dailyAtRe.MatchString(s) {
		return 0, 0, fmt.Errorf("daily_at 格式错误: %q,应为 HH:MM(如 03:00)", s)
	}
	var h, m int
	fmt.Sscanf(s, "%d:%d", &h, &m)
	return h, m, nil
}

// Load 读取 YAML、填默认值、规范化路径并校验。
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
	if c.NotifyOn == "" {
		c.NotifyOn = "failure"
	}
	if c.NotifyOn != "failure" && c.NotifyOn != "all" {
		return nil, fmt.Errorf("notify_on 只能为 failure 或 all")
	}
	if c.AppName != "" {
		if err := validName("app_name", c.AppName); err != nil {
			return nil, err
		}
	}
	if c.AccessTokenFile == "" {
		c.AccessTokenFile = "~/.bpxup/token.json"
	}
	c.AccessTokenFile = NormalizePath(c.AccessTokenFile)
	c.LogFile = NormalizePath(c.LogFile)
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
	if len(c.Projects) == 0 {
		// 允许无项目:setup 可跳过项目配置,login 只需 app_key;
		// run / verify 对无任务配置自行报"未找到匹配的任务"。
		return c, nil
	}
	for i := range c.Projects {
		p := &c.Projects[i]
		if err := validName("项目 id", p.ID); err != nil {
			return nil, err
		}
		if len(p.Tasks) == 0 {
			return nil, fmt.Errorf("项目 %s 至少需要一个任务", p.ID)
		}
		for j := range p.Tasks {
			t := &p.Tasks[j]
			if err := validName("任务名", t.Name); err != nil {
				return nil, err
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
				return nil, err
			}
		}
	}
	return c, nil
}
