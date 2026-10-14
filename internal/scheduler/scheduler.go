package scheduler

import (
	"log/slog"
	"time"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/backup"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
)

// Run 常驻调度:每分钟检查各任务是否到达 daily_at,到期执行备份+清理。
// 幂等由远端 _SUCCESS 保证,进程重启安全;失败任务 10 分钟退避重试。
func Run(cfg *config.Config, eng *backup.Engine, log *slog.Logger) error {
	type state struct {
		lastSuccess string    // YYYYMMDD
		nextRetry   time.Time // 失败退避
	}
	states := map[string]*state{} // key: 项目id/任务名
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	log.Info("daemon 已启动")
	for now := range ticker.C {
		for i := range cfg.Projects {
			p := &cfg.Projects[i]
			for j := range p.Tasks {
				t := &p.Tasks[j]
				key := p.ID + "/" + t.Name
				st, ok := states[key]
				if !ok {
					st = &state{}
					states[key] = st
				}
				h, m, err := config.ParseDailyAt(t.DailyAt)
				if err != nil {
					log.Error("daily_at 配置错误", "任务", key, "err", err)
					continue
				}
				today := now.Format("20060102")
				due := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
				if now.Before(due) || st.lastSuccess == today || now.Before(st.nextRetry) {
					continue
				}
				log.Info("开始备份", "任务", key)
				if err := eng.RunTask(*p, *t, today); err != nil {
					log.Error("备份失败,10 分钟后重试", "任务", key, "err", err)
					st.nextRetry = now.Add(10 * time.Minute)
					continue
				}
				st.lastSuccess = today
				if err := eng.Cleanup(*p, *t); err != nil {
					log.Error("清理失败", "任务", key, "err", err)
				}
			}
		}
	}
	return nil
}
