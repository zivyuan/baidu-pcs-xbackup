package scheduler

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/backup"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/notify"
)

// Run 常驻调度:每分钟检查各任务是否到达 daily_at,到期执行备份+清理。
// 幂等由远端 _SUCCESS 保证,进程重启安全。
// 配置热重载:每 tick 比对配置文件 mtime,变化则重读;失败沿用旧配置。
// ctx 取消(SIGTERM/SIGINT/服务停止)时优雅退出,退出前 flush 聚合通知。
func Run(ctx context.Context, cfgPath string, cfg *config.Config,
	newEngine func(*config.Config) (*backup.Engine, *baidupan.Client),
	log *slog.Logger) error {

	notif := notify.New(cfg.WebhookURL, cfg.NotifyOn, log)
	defer func() { notif.Close() }() // 闭包引用:热重载重建后关新实例
	eng, pan := newEngine(cfg)
	if err := pan.EnsureToken(); err != nil {
		return err
	}
	log.Info("daemon 已启动")

	type state struct {
		day         string
		lastSuccess string
		givenUp     string
		attempts    int
		nextRetry   time.Time
	}
	states := map[string]*state{}
	lastMod := time.Time{}
	if fi, err := os.Stat(cfgPath); err == nil {
		lastMod = fi.ModTime()
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var now time.Time
	for {
		select {
		case <-ctx.Done():
			log.Info("daemon 退出")
			return nil
		case now = <-ticker.C:
		}
		if fi, err := os.Stat(cfgPath); err == nil && !fi.ModTime().Equal(lastMod) {
			lastMod = fi.ModTime()
			if nc, lerr := config.Load(cfgPath); lerr != nil {
				log.Error("配置重载失败,沿用旧配置", "err", lerr)
			} else {
				rebuildNotif := nc.WebhookURL != cfg.WebhookURL || nc.NotifyOn != cfg.NotifyOn
				cfg = nc
				eng, pan = newEngine(cfg)
				if rebuildNotif {
					notif.Close()
					notif = notify.New(cfg.WebhookURL, cfg.NotifyOn, log)
				}
				log.Info("配置已重载")
				if terr := pan.EnsureToken(); terr != nil {
					log.Error("token 失效", "err", terr)
				}
			}
		}
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
				files, err := eng.RunTask(*p, *t, today)
				if err != nil {
					if st.attempts >= cfg.Retry.MaxAttempts {
						log.Error("备份重试耗尽,当日放弃,等待人工干预",
							"任务", key, "尝试", st.attempts, "err", err)
						notif.Failure(p.ID, t.Name, st.attempts, err.Error())
						st.givenUp = today
					} else {
						next := now.Add(time.Duration(cfg.Retry.DaemonInterval))
						log.Error("备份失败,稍后重试", "任务", key, "下次", next.Format("15:04:05"), "err", err)
						st.nextRetry = next
					}
					continue
				}
				st.lastSuccess = today
				notif.Success(p.ID, t.Name, files)
				if err := eng.Cleanup(*p, *t); err != nil {
					log.Error("清理失败", "任务", key, "err", err)
				}
			}
		}
	}
}
