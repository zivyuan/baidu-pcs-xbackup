//go:build darwin

package servicemgr

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

const plistPath = "/Library/LaunchDaemons/com.zivyuan.bpxup.plist"
const label = "com.zivyuan.bpxup"

const plistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>--service-run</string>
    <string>--config</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>Crashed</key><true/></dict>
</dict>
</plist>
`

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func loaded() bool {
	return exec.Command("launchctl", "list", label).Run() == nil
}

func ensure(cfgPath string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	want := fmt.Sprintf(plistTmpl, label, exe, cfgPath)
	old, readErr := os.ReadFile(plistPath)
	existed := readErr == nil
	if existed && string(old) == want && loaded() {
		return "服务已在运行", nil
	}
	if err := os.WriteFile(plistPath, []byte(want), 0o644); err != nil {
		return "", fmt.Errorf("写入 plist(需要 root): %w", err)
	}
	rollback := func() {
		runCmd("launchctl", "unload", "-w", plistPath)
		if existed {
			os.WriteFile(plistPath, old, 0o644)
		} else {
			os.Remove(plistPath)
		}
	}
	runCmd("launchctl", "unload", "-w", plistPath) // 已加载时先卸载再载入新配置
	if _, err := runCmd("launchctl", "load", "-w", plistPath); err != nil {
		rollback()
		return "", fmt.Errorf("加载服务: %w", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if loaded() {
			return "服务已注册并启动(开机自启)", nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	rollback()
	return "", fmt.Errorf("服务启动超时(60s),已回滚本次注册变更")
}

func stop() error {
	if _, err := os.Stat(plistPath); err != nil {
		return fmt.Errorf("服务未注册")
	}
	if _, err := runCmd("launchctl", "unload", "-w", plistPath); err != nil {
		return fmt.Errorf("停止服务: %w", err)
	}
	return nil
}

func uninstall() error {
	if _, err := os.Stat(plistPath); err != nil {
		return fmt.Errorf("服务未注册")
	}
	runCmd("launchctl", "unload", "-w", plistPath)
	return os.Remove(plistPath)
}
