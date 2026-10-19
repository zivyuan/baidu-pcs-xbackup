package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/backup"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/notify"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/retry"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/scheduler"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/servicemgr"
)

// newFlagSet 为子命令建 FlagSet:--config 必有,额外标志由 extra 回调注册。
func newFlagSet(name string, extra func(fs *flag.FlagSet)) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.String("config", "", "配置文件路径(默认: BPXUP_CONFIG 环境变量,再默认 ./config.yaml)")
	if extra != nil {
		extra(fs)
	}
	return fs
}

// configPath 配置路径优先级:--config 参数 > BPXUP_CONFIG 环境变量 > ./config.yaml
func configPath(fs *flag.FlagSet) string {
	path := fs.Lookup("config").Value.String()
	if path == "" {
		path = os.Getenv("BPXUP_CONFIG")
	}
	if path == "" {
		path = "config.yaml"
	}
	return path
}

// loadConfig 按优先级解析配置路径并加载配置,失败直接退出。
func loadConfig(fs *flag.FlagSet) *config.Config {
	cfg, err := config.Load(configPath(fs))
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置错误:", err)
		os.Exit(1)
	}
	return cfg
}

func newEngine(cfg *config.Config, log *slog.Logger) (*backup.Engine, *baidupan.Client) {
	pan := baidupan.New(cfg.AppKey, cfg.SecretKey, cfg.AccessTokenFile)
	eng := backup.New(pan, cfg.Root(), log)
	// 机器识别码与 token 同目录(默认 ~/.bpxup/fingerprint)
	eng.FingerprintFile = filepath.Join(filepath.Dir(cfg.AccessTokenFile), "fingerprint")
	return eng, pan
}

func setupLogger(cfg *config.Config, daemon bool) *slog.Logger {
	w := os.Stderr
	if daemon {
		path := cfg.LogFile
		if path == "" {
			path = "bpxup.log"
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "打开日志文件失败:", err)
			os.Exit(1)
		}
		w = f
	}
	return slog.New(newConciseHandler(w))
}

func cmdLogin(args []string) error {
	fs := newFlagSet("login", nil)
	fs.Parse(args)
	cfg := loadConfig(fs)
	pan := baidupan.New(cfg.AppKey, cfg.SecretKey, cfg.AccessTokenFile)
	return pan.Login()
}

func cmdRun(args []string) error {
	var proj, task string
	fs := newFlagSet("run", func(fs *flag.FlagSet) {
		fs.StringVar(&proj, "project", "", "只跑指定项目")
		fs.StringVar(&task, "task", "", "只跑指定任务")
	})
	fs.Parse(args)
	cfg := loadConfig(fs)

	log := setupLogger(cfg, false)
	eng, pan := newEngine(cfg, log) // run 模式不经 scheduler,自行 EnsureToken
	notif := notify.New(cfg.WebhookURL, cfg.NotifyOn, log)
	defer notif.Close() // 退出前 flush 未发送的聚合汇总
	if err := pan.EnsureToken(); err != nil {
		return err
	}
	date := time.Now().Format("20060102")
	failed := false
	matched := 0
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		if proj != "" && p.ID != proj {
			continue
		}
		for j := range p.Tasks {
			t := &p.Tasks[j]
			if task != "" && t.Name != task {
				continue
			}
			matched++
			files := 0
			runErr := retry.Do(cfg.Retry.MaxAttempts, time.Duration(cfg.Retry.Interval), func() error {
				var err error
				files, err = eng.RunTask(*p, *t, date)
				if err != nil {
					log.Warn("备份失败", "项目", p.ID, "任务", t.Name, "err", err)
				}
				return err
			})
			if runErr != nil {
				log.Error("备份重试耗尽,任务终止,等待人工干预",
					"项目", p.ID, "任务", t.Name, "尝试", cfg.Retry.MaxAttempts, "err", runErr)
				notif.Failure(p.ID, t.Name, cfg.Retry.MaxAttempts, runErr.Error())
				failed = true
				continue
			}
			if err := eng.Cleanup(*p, *t); err != nil {
				log.Error("清理失败", "项目", p.ID, "任务", t.Name, "err", err)
			}
			notif.Success(p.ID, t.Name, files)
		}
	}
	if matched == 0 {
		return fmt.Errorf("未找到匹配的任务(project=%q task=%q)", proj, task)
	}
	if failed {
		return fmt.Errorf("部分任务失败,详见日志")
	}
	return nil
}

// cmdVerify 手动对账远端备份:默认只报告差异,--fix 才重传修复。
// 退出码非 0 表示有差异(或 --fix 后仍修不完)/校验失败,便于脚本巡检。
func cmdVerify(args []string) error {
	var proj, task, date string
	var fix bool
	fs := newFlagSet("verify", func(fs *flag.FlagSet) {
		fs.StringVar(&proj, "project", "", "只校验指定项目")
		fs.StringVar(&task, "task", "", "只校验指定任务")
		fs.StringVar(&date, "date", "", "校验指定日期(YYYYMMDD),默认每个任务最新一份")
		fs.BoolVar(&fix, "fix", false, "发现不一致时重传修复")
	})
	fs.Parse(args)
	cfg := loadConfig(fs)

	log := setupLogger(cfg, false)
	eng, pan := newEngine(cfg, log)
	if err := pan.EnsureToken(); err != nil {
		return err
	}
	matched, bad := 0, false
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		if proj != "" && p.ID != proj {
			continue
		}
		d := date
		if d == "" {
			latest, err := eng.LatestDate(*p)
			if err != nil {
				log.Error("获取最新备份日期失败", "项目", p.ID, "err", err)
				bad = true
				continue
			}
			if latest == "" {
				log.Warn("无历史备份,跳过", "项目", p.ID)
				continue
			}
			d = latest
		}
		for j := range p.Tasks {
			t := &p.Tasks[j]
			if task != "" && t.Name != task {
				continue
			}
			matched++
			var rels []string
			err := retry.Do(cfg.Retry.MaxAttempts, time.Duration(cfg.Retry.Interval), func() error {
				var verr error
				rels, verr = eng.VerifyTask(*p, *t, d, fix)
				return verr
			})
			if err != nil {
				log.Error("校验失败", "项目", p.ID, "任务", t.Name, "日期", d, "err", err)
				bad = true
				continue
			}
			if len(rels) == 0 {
				fmt.Printf("[%s/%s] %s 一致\n", p.ID, t.Name, d)
				continue
			}
			bad = true
			fmt.Printf("[%s/%s] %s 不一致 %d 个:\n", p.ID, t.Name, d, len(rels))
			for _, r := range rels {
				fmt.Println("  " + r)
			}
		}
	}
	if matched == 0 {
		return fmt.Errorf("未找到匹配的任务(project=%q task=%q)", proj, task)
	}
	if bad {
		return fmt.Errorf("存在不一致或校验失败,详见输出")
	}
	return nil
}

// splitServiceRun 分离内部标志 --service-run(服务管理器拉起时传入,
// 不注册到 FlagSet,避免污染帮助)。
func splitServiceRun(args []string) (bool, []string) {
	rest := make([]string, 0, len(args))
	hit := false
	for _, a := range args {
		if a == "--service-run" {
			hit = true
			continue
		}
		rest = append(rest, a)
	}
	return hit, rest
}

// runForeground 前台调度循环(无服务管理器回退 / --service-run 的 Unix 路径)。
func runForeground(cfgPath string, cfg *config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := setupLogger(cfg, true)
	return scheduler.Run(ctx, cfgPath, cfg,
		func(c *config.Config) (*backup.Engine, *baidupan.Client) { return newEngine(c, log) }, log)
}

func cmdDaemon(args []string) error {
	serviceRun, args := splitServiceRun(args)
	var uninstall bool
	fs := newFlagSet("daemon", func(fs *flag.FlagSet) {
		fs.BoolVar(&uninstall, "uninstall", false, "停止并取消注册系统服务")
	})
	fs.Parse(args)
	cfgPath := configPath(fs)

	if serviceRun {
		return runService(cfgPath) // windows: svc.Run;其余平台: 前台循环
	}
	if uninstall {
		return servicemgr.Uninstall()
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("配置错误(可先执行 bpxup validate 排查): %w", err)
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	action, err := servicemgr.Ensure(abs)
	if errors.Is(err, servicemgr.ErrNoServiceManager) {
		fmt.Println("未检测到 systemd/launchd,以前台模式运行(Ctrl+C 退出)...")
		return runForeground(cfgPath, cfg)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s;日志:%s\n", action, logFilePath(cfg))
	return nil
}

// logFilePath daemon 日志路径(与 setupLogger 同规则)。
func logFilePath(cfg *config.Config) string {
	if cfg.LogFile != "" {
		return cfg.LogFile
	}
	return "bpxup.log"
}

func cmdStop(args []string) error {
	fs := newFlagSet("stop", nil)
	fs.Parse(args)
	if err := servicemgr.Stop(); err != nil {
		return fmt.Errorf("%w;前台模式运行的 daemon 请在对应终端 Ctrl+C", err)
	}
	fmt.Println("daemon 已停止(注册与开机自启保留)")
	return nil
}
