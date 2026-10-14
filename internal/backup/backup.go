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
	return e.markSuccess(date, dir)
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

func (e *Engine) markSuccess(date, dir string) error {
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

// Cleanup 透传 retention 清理逻辑(internal/retention)。
func (e *Engine) Cleanup(p config.Project, t config.Task) error {
	return retention.Cleanup(e.Pan, e.Root, p, t, e.Log)
}
