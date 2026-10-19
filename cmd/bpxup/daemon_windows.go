//go:build windows

package main

import (
	"context"

	"golang.org/x/sys/windows/svc"

	"github.com/zivyuan/baidu-pcs-xbackup/internal/backup"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/baidupan"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/config"
	"github.com/zivyuan/baidu-pcs-xbackup/internal/scheduler"
)

const svcName = "bpxup"

type bpxupService struct{ cfgPath string }

// runService Windows:由 SCM 拉起,走服务交互;手动运行 --service-run 时
// svc.Run 检测到非 SCM 环境会直接报错。
func runService(cfgPath string) error {
	return svc.Run(svcName, &bpxupService{cfgPath: cfgPath})
}

func (s *bpxupService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return true, 1
	}
	log := setupLogger(cfg, true)
	// 立刻上报 Running,token 校验放进工作协程:
	// EnsureToken 可能发起网络请求,放在上报前会拖过 SCM 的 30 秒连接窗口(1053)。
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := scheduler.Run(ctx, s.cfgPath, cfg,
			func(c *config.Config) (*backup.Engine, *baidupan.Client) { return newEngine(c, log) }, log)
		cancel() // scheduler 内部未再监听信号,这里释放
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
				cancel()
				<-done // 等调度器 flush 通知后退出
				return false, 0
			}
		case err := <-done:
			return err == nil, 0
		}
	}
}
