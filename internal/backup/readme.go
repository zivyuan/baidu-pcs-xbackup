package backup

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/zivyuan/baidu-backup-store/internal/config"
)

// WriteAppReadme 在应用根目录生成 README.md(未配置 app_description 则跳过)。
func (e *Engine) WriteAppReadme(appName, desc string) error {
	if desc == "" {
		return nil
	}
	title := appName
	if title == "" {
		title = path.Base(e.Root) // remote_root 自定义时取末段作为标题
	}
	content := fmt.Sprintf(
		"# %s\n\n%s\n\n本目录由 bbs(baidu-backup-store)自动备份工具管理。\n\n"+
			"目录结构:`/<项目id>/<YYYYMMDD>/<任务名>`,每个任务按配置保留最近 N 份历史备份。\n",
		title, desc)
	return e.uploadReadme(e.Root, content)
}

// WriteProjectReadme 在项目目录生成 README.md,含各任务说明(未配置 description 则跳过)。
func (e *Engine) WriteProjectReadme(p config.Project) error {
	if p.Description == "" {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n## 任务\n\n", p.ID, p.Description)
	for _, t := range p.Tasks {
		fmt.Fprintf(&b, "- **%s**:源 `%s`,%s,保留 %d 份,每日 %s\n",
			t.Name, t.Source, modeText(t.Mode), t.Retention, t.DailyAt)
	}
	return e.uploadReadme(e.Root+"/"+p.ID, b.String())
}

func modeText(mode string) string {
	if mode == "archive" {
		return "打包 tar.gz"
	}
	return "原样上传"
}

func (e *Engine) uploadReadme(dir, content string) error {
	tmp, err := os.CreateTemp("", "bbs-readme-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	return e.Pan.UploadFile(dir+"/README.md", tmpPath)
}
