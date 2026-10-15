package retention

import (
	"log/slog"
	"sort"
	"time"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
)

// PanAPI retention 所需的最小网盘接口(便于测试注入假实现)。
type PanAPI interface {
	List(dir string) ([]baidupan.Entry, error)
	Delete(paths []string) error
}

// Cleanup 保留任务最近 retention 个备份,删除粒度为任务级:
// 仅删 /<日期>/<任务名>(raw)或 /<日期>/<任务名>.tar.gz(archive),不动同日期其他任务的数据。
func Cleanup(pan PanAPI, root string, p config.Project, t config.Task, log *slog.Logger) error {
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
		if t.Mode == "archive" {
			paths = append(paths, projDir+"/"+d+"/"+t.Name+".tar.gz")
		} else {
			paths = append(paths, projDir+"/"+d+"/"+t.Name)
		}
	}
	if err := pan.Delete(paths); err != nil {
		return err
	}
	log.Info("清理过期备份", "项目", p.ID, "任务", t.Name, "删除", paths, "保留", t.Retention)
	pruneEmptyDateDirs(pan, projDir, toDelete) // 顺带清理已空的日期目录,失败忽略
	return nil
}

// taskHasBackup 判断日期目录中是否存在该任务的数据(存在即算一份,含未完成的残留)。
func taskHasBackup(pan PanAPI, datePath string, t config.Task) (bool, error) {
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

// pruneEmptyDateDirs 删除已空的日期目录,失败静默(不影响主流程)。
// 删除任务数据后网盘列表存在短暂最终一致,故带重试。
func pruneEmptyDateDirs(pan PanAPI, projDir string, dates []string) {
	for _, d := range dates {
		dir := projDir + "/" + d
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(2 * time.Second)
			}
			entries, err := pan.List(dir)
			if err != nil || len(entries) > 0 {
				continue
			}
			if pan.Delete([]string{dir}) == nil {
				break
			}
		}
	}
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
