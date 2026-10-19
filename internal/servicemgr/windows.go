//go:build windows

package servicemgr

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const svcName = "bpxup"

// serviceArgs 返回 SCM 存储的命令行可变参数部分(仅用于 UpdateConfig 的
// BinaryPathName 与幂等比对);CreateService 走可变参数(exec argv 语义,
// SCM 自行处理带空格参数),不加引号。
func serviceArgs(cfgPath string) string {
	return `daemon --service-run --config "` + cfgPath + `"`
}

func waitState(s *mgr.Service, to svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
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

func ensure(cfgPath string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	m, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("连接服务管理器(需管理员权限): %w", err)
	}
	defer m.Disconnect()
	want := serviceArgs(cfgPath)

	s, openErr := m.OpenService(svcName)
	if openErr == nil {
		defer s.Close()
		c, err := s.Config()
		if err != nil {
			return "", err
		}
		st, err := s.Query()
		if err != nil {
			return "", fmt.Errorf("查询服务状态: %w", err)
		}
		// 期望的完整命令;SCM 存储的 BinaryPathName 可能带引号,比较时两边都去掉首尾引号
		expected := `"` + exe + `" ` + want
		match := strings.Trim(c.BinaryPathName, `"`) == strings.Trim(expected, `"`)
		if match && st.State == svc.Running {
			return "服务已在运行", nil
		}
		if !match {
			c.BinaryPathName = expected
			if err := s.UpdateConfig(c); err != nil {
				return "", fmt.Errorf("更新服务配置: %w", err)
			}
			if st.State == svc.Running {
				// 配置已变更,重启使新配置生效
				if _, err := s.Control(svc.Stop); err != nil {
					return "", fmt.Errorf("停止服务: %w", err)
				}
				if err := waitState(s, svc.Stopped, 60*time.Second); err != nil {
					return "", err
				}
				st.State = svc.Stopped
			}
		}
		if st.State != svc.Running {
			if err := s.Start(); err != nil {
				return "", fmt.Errorf("启动服务: %w", err)
			}
		}
		if err := waitState(s, svc.Running, 60*time.Second); err != nil {
			return "", err
		}
		return "服务已启动(开机自启)", nil
	}
	// 未注册:创建(自动启动+故障恢复)并启动;失败回滚删除
	s, err = m.CreateService(svcName, exe, mgr.Config{
		DisplayName:  "bpxup 百度网盘备份",
		Description:  "基于百度网盘开放平台的定时备份守护进程",
		StartType:    mgr.StartAutomatic,
		Dependencies: []string{"Tcpip"},
	}, "daemon", "--service-run", "--config", cfgPath)
	if err != nil {
		return "", err
	}
	defer s.Close()
	rollback := func() {
		_ = eventlog.Remove(svcName)
		_ = s.Delete()
	}
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400); err != nil {
		rollback()
		return "", err
	}
	_ = eventlog.InstallAsEventCreate(svcName, eventlog.Error|eventlog.Warning|eventlog.Info)
	if err := s.Start(); err != nil {
		rollback()
		return "", fmt.Errorf("启动服务: %w", err)
	}
	if err := waitState(s, svc.Running, 60*time.Second); err != nil {
		rollback()
		return "", err
	}
	return "服务已注册并启动(开机自启)", nil
}

func stop() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器(需管理员权限): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return errors.New("服务未注册")
	}
	defer s.Close()
	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("停止服务: %w", err)
	}
	return waitState(s, svc.Stopped, 60*time.Second)
}

func uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器(需管理员权限): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return errors.New("服务未注册")
	}
	defer s.Close()
	// 先停服务再删除;Control(Stop) 对已停止服务返回错误 1062,视为已停止
	if st, err := s.Query(); err != nil {
		return fmt.Errorf("查询服务状态: %w", err)
	} else if st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, syscall.Errno(1062)) {
			return fmt.Errorf("停止服务: %w", err)
		}
		if err := waitState(s, svc.Stopped, 60*time.Second); err != nil {
			return err
		}
	}
	_ = eventlog.Remove(svcName)
	return s.Delete()
}
