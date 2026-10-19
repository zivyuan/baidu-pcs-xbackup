package scheduler

import (
	"log/slog"
	"time"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/backup"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/notify"
)

// Run 常驻调度:每分钟检查各任务是否到达 daily_at,到期执行备份+清理。
// 幂等由远端 _SUCCESS 保证,进程重启安全。
// 有界重试:失败按 daemon_interval 退避,达 max_attempts 后当日放弃并通知,
// 等待人工干预(手动 run 或次日自动恢复)。不复用 retry.Do——其内部 sleep
// 会阻塞调度循环,卡住其他任务。
func Run(cfg *config.Config, eng *backup.Engine, log *slog.Logger, notif *notify.Notifier) error {
	type state struct {
		day         string    // 尝试计数所属日期(YYYYMMDD),跨天重置
		lastSuccess string    // YYYYMMDD
		givenUp     string    // 重试耗尽当日放弃的日期(YYYYMMDD)
		attempts    int       // 今日已尝试次数
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
				if now.Before(due) || st.lastSuccess == today || st.givenUp == today || now.Before(st.nextRetry) {
					continue
				}
				if st.day != today {
					st.day = today
					st.attempts = 0
				}
				st.attempts++
				log.Info("开始备份", "任务", key, "尝试", st.attempts, "共", cfg.Retry.MaxAttempts)
				_, runErr := eng.RunTask(*p, *t, today)
				if runErr != nil {
					if st.attempts >= cfg.Retry.MaxAttempts {
						log.Error("备份重试耗尽,当日放弃,等待人工干预",
							"任务", key, "尝试", st.attempts, "err", runErr)
						notif.Failure(p.ID, t.Name, st.attempts, runErr.Error())
						st.givenUp = today
					} else {
						next := now.Add(time.Duration(cfg.Retry.DaemonInterval))
						log.Error("备份失败,稍后重试", "任务", key, "下次", next.Format("15:04:05"), "err", runErr)
						st.nextRetry = next
					}
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
