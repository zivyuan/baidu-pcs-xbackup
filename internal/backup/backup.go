package backup

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/retention"
)

// PanClient Engine 所需的最小网盘接口(便于测试注入假实现);
// *baidupan.Client 天然满足。
type PanClient interface {
	List(dir string) ([]baidupan.Entry, error)
	MkdirAll(dir string) error
	UploadFile(remotePath, localPath string) error
	Download(fsID int64) ([]byte, error)
	Delete(paths []string) error
}

// Engine 备份引擎:执行单个任务的备份并打完成标志。
type Engine struct {
	Pan             PanClient
	Root            string
	Log             *slog.Logger
	FingerprintFile string            // 本机识别码存放路径(与 token 同目录)
	dirCache        map[string]bool // 已确认存在的远端目录,避免重复 MkdirAll 触发限流
}

func New(pan PanClient, root string, log *slog.Logger) *Engine {
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

// RunTask 执行单个任务的备份,成功返回本次备份的文件数
// (archive=1,raw=期望文件总数;当日已备份跳过时返回 0)。
// 完成标志:raw → <日期>/<任务名>/_SUCCESS;archive → <日期>/<任务名>.tar.gz 存在。
func (e *Engine) RunTask(p config.Project, t config.Task, date string) (int, error) {
	if err := e.ensureFingerprint(p, t); err != nil {
		return 0, err
	}
	dir := e.dateDir(p, date)
	if done, _ := e.IsDone(p, t, date); done {
		e.Log.Info("当日已备份,跳过", "项目", p.ID, "任务", t.Name)
		return 0, nil
	}
	if err := e.ensureDir(dir); err != nil {
		return 0, err
	}
	switch t.Mode {
	case "archive":
		tmp, err := os.CreateTemp("", "bpxup-*.tar.gz")
		if err != nil {
			return 0, err
		}
		tmpPath := tmp.Name()
		tmp.Close()
		defer os.Remove(tmpPath)
		if err := TarGz(t.Source, tmpPath); err != nil {
			return 0, fmt.Errorf("打包: %w", err)
		}
		st, err := os.Stat(tmpPath)
		if err != nil {
			return 0, err
		}
		remote := dir + "/" + t.Name + ".tar.gz"
		ok, err := e.remoteSizeMatches(remote, st.Size())
		if err != nil {
			return 0, err
		}
		if !ok { // 已存在且大小一致则跳过(重跑续传),否则上传
			if err := e.Pan.UploadFile(remote, tmpPath); err != nil {
				return 0, err
			}
			// 复核:防 create 假成功
			if ok, err := e.remoteSizeMatches(remote, st.Size()); err != nil {
				return 0, err
			} else if !ok {
				return 0, fmt.Errorf("archive 复核不一致: %s", remote)
			}
		}
		e.Log.Info("archive 上传完成", "项目", p.ID, "任务", t.Name, "远端", remote)
		return 1, nil
	case "raw":
		uploaded, total, err := e.syncRaw(dir+"/"+t.Name, t.Source)
		if err != nil {
			return 0, err
		}
		if total == 0 {
			e.Log.Warn("raw 源目录为空,未上传任何文件", "项目", p.ID, "任务", t.Name)
		}
		if err := e.markSuccess(dir + "/" + t.Name); err != nil {
			return 0, err
		}
		e.Log.Info("raw 上传完成", "项目", p.ID, "任务", t.Name, "文件数", total, "本次上传", uploaded)
		return total, nil
	}
	return 0, nil
}

// markSuccess 在任务目录(仅 raw 模式调用)上传完成标志。
func (e *Engine) markSuccess(taskDir string) error {
	tmp, err := os.CreateTemp("", "bpxup-success-*")
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
