//go:build !windows

package main

import "github.com/zivyuan/baidu-pcs-xbackup/internal/config"

// runService 非 Windows 平台无 SCM 服务体:systemd/launchd 以
// `daemon --service-run` 拉起,进程直接跑前台调度循环(退出靠 SIGTERM)。
func runService(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	return runForeground(cfgPath, cfg)
}
