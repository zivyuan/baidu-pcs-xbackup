//go:build linux

package servicemgr

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const unitPath = "/etc/systemd/system/bpxup.service"

const unitTmpl = `[Unit]
Description=bpxup 百度网盘备份守护进程
After=network-online.target
Wants=network-online.target

[Service]
ExecStart="%s" daemon --service-run --config "%s"
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
`

func systemdAvailable() bool {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	_, err := exec.LookPath("systemctl")
	return err == nil
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func isActive() bool {
	out, err := runCmd("systemctl", "is-active", "bpxup")
	return err == nil && strings.TrimSpace(out) == "active"
}

func ensure(cfgPath string) (string, error) {
	if !systemdAvailable() {
		return "", ErrNoServiceManager
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	want := fmt.Sprintf(unitTmpl, exe, cfgPath)
	old, readErr := os.ReadFile(unitPath)
	existed := readErr == nil
	if existed && string(old) == want && isActive() {
		return "服务已在运行", nil
	}
	if err := os.WriteFile(unitPath, []byte(want), 0o644); err != nil {
		return "", fmt.Errorf("写入 unit(需要 root): %w", err)
	}
	rollback := func() {
		if existed {
			os.WriteFile(unitPath, old, 0o644)
		} else {
			// 新建分支:enable 已写入 wants 悬空符号链接,先取消再删 unit
			runCmd("systemctl", "disable", "bpxup")
			os.Remove(unitPath)
		}
		runCmd("systemctl", "daemon-reload")
	}
	if _, err := runCmd("systemctl", "daemon-reload"); err != nil {
		rollback()
		return "", fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if _, err := runCmd("systemctl", "enable", "bpxup"); err != nil {
		rollback()
		return "", fmt.Errorf("设置开机自启(需要 root): %w", err)
	}
	if _, err := runCmd("systemctl", "restart", "bpxup"); err != nil {
		rollback()
		return "", fmt.Errorf("启动服务: %w", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if isActive() {
			return "服务已注册并启动(开机自启)", nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	if out, _ := runCmd("journalctl", "-u", "bpxup", "-n", "20", "--no-pager"); out != "" {
		fmt.Println("服务日志尾部:\n" + out)
	}
	rollback()
	return "", fmt.Errorf("服务启动超时(60s),已回滚本次注册变更")
}

func stop() error {
	if _, err := os.Stat(unitPath); err != nil {
		return errors.New("服务未注册")
	}
	if _, err := runCmd("systemctl", "stop", "bpxup"); err != nil {
		return fmt.Errorf("停止服务: %w", err)
	}
	return nil
}

func uninstall() error {
	if _, err := os.Stat(unitPath); err != nil {
		return errors.New("服务未注册")
	}
	runCmd("systemctl", "disable", "--now", "bpxup")
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	_, err := runCmd("systemctl", "daemon-reload")
	return err
}
