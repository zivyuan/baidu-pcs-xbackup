package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/backup"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/scheduler"
)

// newFlagSet 为子命令建 FlagSet:--config 必有,额外标志由 extra 回调注册。
func newFlagSet(name string, extra func(fs *flag.FlagSet)) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.String("config", "", "配置文件路径(默认: BBS_CONFIG 环境变量,再默认 ./config.yaml)")
	if extra != nil {
		extra(fs)
	}
	return fs
}

// loadConfig 配置路径优先级:--config 参数 > BBS_CONFIG 环境变量 > ./config.yaml
func loadConfig(fs *flag.FlagSet) *config.Config {
	cfgPath := fs.Lookup("config").Value.String()
	if cfgPath == "" {
		cfgPath = os.Getenv("BBS_CONFIG")
	}
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置错误:", err)
		os.Exit(1)
	}
	return cfg
}

func newEngine(cfg *config.Config, log *slog.Logger) *backup.Engine {
	pan := baidupan.New(cfg.AppKey, cfg.SecretKey, cfg.AccessTokenFile)
	eng := backup.New(pan, cfg.Root(), log)
	// 机器识别码与 token 同目录(默认 ~/.bpxup/fingerprint)
	eng.FingerprintFile = filepath.Join(filepath.Dir(cfg.AccessTokenFile), "fingerprint")
	return eng
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
	return slog.New(slog.NewTextHandler(w, nil))
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
	eng := newEngine(cfg, log)
	if err := eng.Pan.EnsureToken(); err != nil {
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
			if err := eng.RunTask(*p, *t, date); err != nil {
				log.Error("备份失败", "项目", p.ID, "任务", t.Name, "err", err)
				failed = true
				continue
			}
			if err := eng.Cleanup(*p, *t); err != nil {
				log.Error("清理失败", "项目", p.ID, "任务", t.Name, "err", err)
			}
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

func cmdDaemon(args []string) error {
	fs := newFlagSet("daemon", nil)
	fs.Parse(args)
	cfg := loadConfig(fs)

	log := setupLogger(cfg, true)
	eng := newEngine(cfg, log)
	if err := eng.Pan.EnsureToken(); err != nil {
		return err
	}
	return scheduler.Run(cfg, eng, log)
}

func cmdService(args []string) error {
	return serviceDispatch(args)
}
