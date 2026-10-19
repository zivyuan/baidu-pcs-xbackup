//go:build darwin

package servicemgr

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// loaded 判断 job 已加载且有进程在运行:launchctl list 输出首列为 PID,
// "-" 表示已加载但无进程,视为未运行。
func loaded() bool {
	var stderr bytes.Buffer
	cmd := exec.Command("launchctl", "list", label)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	fields := strings.Fields(strings.SplitN(string(out), "\n", 2)[0])
	return len(fields) > 0 && fields[0] != "-"
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
		return errors.New("服务未注册")
	}
	// 不带 -w:Stop 后仍保持开机自启,不持久化 Disabled
	if _, err := runCmd("launchctl", "unload", plistPath); err != nil {
		return fmt.Errorf("停止服务: %w", err)
	}
	return nil
}

func uninstall() error {
	if _, err := os.Stat(plistPath); err != nil {
		return errors.New("服务未注册")
	}
	runCmd("launchctl", "unload", "-w", plistPath)
	return os.Remove(plistPath)
}
