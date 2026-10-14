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
