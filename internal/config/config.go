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
	Mode      string `yaml:"mode"` // archive | raw
	Retention int    `yaml:"retention"`
	DailyAt   string `yaml:"daily_at"`
}

type Project struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"` // 写入项目目录 README.md,便于在网盘里了解用途
	Tasks       []Task `yaml:"tasks"`
}

type Config struct {
	AppKey          string    `yaml:"app_key"`
	SecretKey       string    `yaml:"secret_key"`
	AppName         string    `yaml:"app_name"`
	AppDescription  string    `yaml:"app_description"` // 写入应用目录 README.md
	AccessTokenFile string    `yaml:"access_token_file"`
	RemoteRoot      string    `yaml:"remote_root"`
	LogFile         string    `yaml:"log_file"`
	Projects        []Project `yaml:"projects"`
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

// Root 返回远端根目录:remote_root 优先,否则 /apps/<app_name>。
func (c *Config) Root() string {
	if c.RemoteRoot != "" {
		return strings.TrimSuffix(c.RemoteRoot, "/")
	}
	return "/apps/" + c.AppName
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
