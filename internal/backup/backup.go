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
	"github.com/zivyuan/baidu-backup-store/internal/retention"
)

// Engine 备份引擎:执行单个任务的备份并打完成标志。
type Engine struct {
	Pan             *baidupan.Client
	Root            string
	Log             *slog.Logger
	FingerprintFile string            // 本机识别码存放路径(与 token 同目录)
	dirCache        map[string]bool // 已确认存在的远端目录,避免重复 MkdirAll 触发限流
}

func New(pan *baidupan.Client, root string, log *slog.Logger) *Engine {
	return &Engine{Pan: pan, Root: root, Log: log, dirCache: map[string]bool{}}
}

func (e *Engine) dateDir(p config.Project, date string) string {
	return e.Root + "/" + p.ID + "/" + date
}

// ensureDir 带内存缓存的 MkdirAll:同一远端目录只创建一次。
func (e *Engine) ensureDir(remote string) error {
	if e.dirCache[remote] {
		return nil
	}
	if err := e.Pan.MkdirAll(remote); err != nil {
		return err
	}
	e.dirCache[remote] = true
	return nil
}

// IsDone 检查本任务当日完成标志(任务级,不受同项目其他任务影响)。
func (e *Engine) IsDone(p config.Project, t config.Task, date string) (bool, error) {
	entries, err := e.Pan.List(e.dateDir(p, date))
	if err != nil {
		return false, nil // 目录不存在等,都视为未完成
	}
	for _, en := range entries {
		if t.Mode == "archive" && !en.IsDir && en.Name == t.Name+".tar.gz" {
			return true, nil // 单文件上传成功即完成
		}
		if t.Mode == "raw" && en.IsDir && en.Name == t.Name {
			sub, err := e.Pan.List(en.Path)
			if err != nil {
				return false, nil
			}
			for _, s := range sub {
				if s.Name == "_SUCCESS" {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// RunTask 执行单个任务的备份。
// 完成标志:raw → <日期>/<任务名>/_SUCCESS;archive → <日期>/<任务名>.tar.gz 存在。
func (e *Engine) RunTask(p config.Project, t config.Task, date string) error {
	if err := e.ensureFingerprint(p, t); err != nil {
		return err
	}
	dir := e.dateDir(p, date)
	if done, _ := e.IsDone(p, t, date); done {
		e.Log.Info("当日已备份,跳过", "项目", p.ID, "任务", t.Name)
		return nil
	}
	if err := e.ensureDir(dir); err != nil {
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
		if err := e.markSuccess(dir + "/" + t.Name); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runRaw(p config.Project, t config.Task, dir string) error {
	taskDir := dir + "/" + t.Name
	if err := e.ensureDir(taskDir); err != nil { // 源目录只有文件没有子目录时也要有任务目录
		return err
	}
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
			return e.ensureDir(taskDir + "/" + rel)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		remote := taskDir + "/" + rel
		if idx := strings.LastIndex(rel, "/"); idx >= 0 {
			if err := e.ensureDir(taskDir + "/" + rel[:idx]); err != nil {
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
	if count == 0 {
		e.Log.Warn("raw 源目录为空,未上传任何文件", "项目", p.ID, "任务", t.Name)
	}
	e.Log.Info("raw 上传完成", "项目", p.ID, "任务", t.Name, "文件数", count)
	return nil
}

// markSuccess 在任务目录(仅 raw 模式调用)上传完成标志。
func (e *Engine) markSuccess(taskDir string) error {
	tmp, err := os.CreateTemp("", "bbs-success-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	io.WriteString(tmp, "ok\n")
	tmp.Close()
	defer os.Remove(tmpPath)
	return e.Pan.UploadFile(taskDir+"/_SUCCESS", tmpPath)
}

// Cleanup 透传 retention 清理逻辑(internal/retention)。
func (e *Engine) Cleanup(p config.Project, t config.Task) error {
	return retention.Cleanup(e.Pan, e.Root, p, t, e.Log)
}
