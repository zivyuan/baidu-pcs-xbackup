// Package servicemgr 把 bpxup 注册为各平台原生服务并管理生命周期:
// Linux=systemd,macOS=launchd,Windows=SCM;其余环境返回 ErrNoServiceManager。
// 服务启动命令统一为 <exe> daemon --service-run --config <绝对路径>。
package servicemgr

import "errors"

var ErrNoServiceManager = errors.New("当前环境无 systemd/launchd 服务管理器")

// Ensure 注册(如需)并启动服务,幂等:
// 已注册、--config 一致且已运行 → 返回 "服务已在运行",不做任何变更。
func Ensure(cfgPath string) (string, error) { return ensure(cfgPath) }

// Stop 停止服务(保持注册与开机自启)。
func Stop() error { return stop() }

// Uninstall 停止、取消自启并删除注册产物。
func Uninstall() error { return uninstall() }
