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

// Root 返回远端根目录:remote_root 优先,否则 /apps/<app_name>。
func (c *Config) Root() string {
	if c.RemoteRoot != "" {
		return strings.TrimSuffix(c.RemoteRoot, "/")
	}
	return "/apps/" + c.AppName
}

// ParseDailyAt 解析 24 小时制 "HH:MM"。
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
		if !idRe.MatchString(p.ID) {
			return nil, fmt.Errorf("项目 id %q 仅允许字母数字下划线连字符", p.ID)
		}
		if len(p.Tasks) == 0 {
			return nil, fmt.Errorf("项目 %s 至少需要一个任务", p.ID)
		}
		for j := range p.Tasks {
			t := &p.Tasks[j]
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
				return nil, err
			}
		}
	}
	return c, nil
}
