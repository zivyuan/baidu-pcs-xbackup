//go:build windows

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/notify"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/scheduler"
)

const svcName = "bpxup"

type bpxupService struct{ cfgPath string }

func (s *bpxupService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return true, 1
	}
	// 服务模式下日志一律走配置的 log_file(daemon 默认 ./bpxup.log,即服务工作目录)
	log := setupLogger(cfg, true)
	eng, pan := newEngine(cfg, log)
	// 立刻上报 Running,token 校验放进工作协程:
	// EnsureToken 可能发起网络请求(刷新 token),放在上报前会拖过 SCM 的 30 秒连接窗口(1053)。
	done := make(chan error, 1)
	notif := notify.New(cfg.WebhookURL, cfg.NotifyOn, log)
	go func() {
		if err := pan.EnsureToken(); err != nil {
			log.Error("token 无效", "err", err)
			done <- err
			notif.Close()
			return
		}
		// scheduler.Run 返回后事件源已停,再 Close 满足"Close 与 Failure 不并发"契约
		err := scheduler.Run(cfg, eng, log, notif)
		notif.Close()
		done <- err
	}()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				return false, 0
			}
		case err := <-done:
			return err == nil, 0
		}
	}
}

func installService(cfgPath string) error {
	exe, _ := os.Executable()
	absCfg, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器(需管理员权限): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err == nil {
		s.Close()
		return fmt.Errorf("服务 %s 已存在,先 uninstall", svcName)
	}
	s, err = m.CreateService(svcName, exe, mgr.Config{
		DisplayName:  "bpxup 百度网盘备份",
		Description: "基于百度网盘开放平台的定时备份守护进程",
		StartType:    mgr.StartAutomatic,
		Dependencies: []string{"Tcpip"},
	}, "-service-run", "--config", absCfg)
	if err != nil {
		return err
	}
	defer s.Close()
	// 故障自动重启(三次以内 30s,之后 60s,一天后重置)
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400); err != nil {
		return err
	}
	_ = eventlog.InstallAsEventCreate(svcName, eventlog.Error|eventlog.Warning|eventlog.Info)
	fmt.Println("服务已安装并设为开机自启:", svcName)
	fmt.Println("注意:服务以 LocalSystem 运行,请确保配置中 access_token_file 与 log_file")
	fmt.Println("      为绝对路径且 LocalSystem 可访问(不要用 ~ 或相对路径);")
	fmt.Println("      若 token 是在用户账户下 login 的,请把 token 文件复制到服务可读位置。")
	return nil
}

func removeService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("服务 %s 不存在", svcName)
	}
	defer s.Close()
	_ = eventlog.Remove(svcName)
	return s.Delete()
}

// controlService start=true 时启动服务,否则发送 stop 控制码,并等待到达目标状态。
func controlService(start bool, to svc.State) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("服务 %s 不存在", svcName)
	}
	defer s.Close()
	if start {
		if err := s.Start(); err != nil {
			return err
		}
	} else if _, err := s.Control(svc.Stop); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == to {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待服务状态 %v 超时", to)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func serviceDispatch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: bpxup service <install|uninstall|start|stop> [--config 路径]")
	}
	// 服务本体入口:SCM 以参数 "-service-run --config xxx" 拉起
	if args[0] == "-service-run" {
		fs := flag.NewFlagSet("service-run", flag.ExitOnError)
		cfgPath := fs.String("config", "config.yaml", "")
		fs.Parse(args[1:])
		return svc.Run(svcName, &bpxupService{cfgPath: *cfgPath})
	}
	fs := newFlagSet("service", nil)
	fs.Parse(args[1:])
	cfgPath := fs.Lookup("config").Value.String()
	switch args[0] {
	case "install":
		return installService(cfgPath)
	case "uninstall":
		return removeService()
	case "start":
		return controlService(true, svc.Running)
	case "stop":
		return controlService(false, svc.Stopped)
	}
	return fmt.Errorf("未知子命令: %s", args[0])
}
