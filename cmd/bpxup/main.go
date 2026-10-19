package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

// version 由构建注入:make build 默认取 git describe --tags --always --dirty,
// make publish VERSION=x.y.z 注入干净的发布版本;直接 go build 时为 dev。
var version = "dev"

// versionString 组合完整版本信息:版本号 + git 提交 + 构建时间。
// 提交与时间来自 Go 内建 VCS 打标(debug.ReadBuildInfo),二进制拷贝到
// 服务器后仍可追溯;非 git 环境构建时仅有版本号。
func versionString() string {
	s := "bpxup " + version
	var commit, built, dirty string
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range info.Settings {
			switch kv.Key {
			case "vcs.revision":
				if len(kv.Value) >= 12 {
					commit = kv.Value[:12]
				}
			case "vcs.time":
				if t, err := time.Parse(time.RFC3339, kv.Value); err == nil {
					built = t.Local().Format("2006-01-02 15:04")
				}
			case "vcs.modified":
				if kv.Value == "true" {
					dirty = "*"
				}
			}
		}
	}
	var extra []string
	// 开发版版本号(git describe)已含 g<sha>(及 -dirty),不再重复 commit
	if commit != "" && !strings.Contains(version, commit[:7]) {
		extra = append(extra, "commit "+commit+dirty)
	}
	if built != "" {
		extra = append(extra, "built "+built)
	}
	if len(extra) > 0 {
		s += " (" + strings.Join(extra, ", ") + ")"
	}
	return s
}

func usage() {
	fmt.Fprintf(os.Stderr, `bpxup - 百度网盘自动备份工具

用法:
  bpxup <命令> [参数]

命令:
  login                     设备码授权,获取并保存 token
  setup                     交互式引导:配置 AppKey、第一个项目并完成设备授权
  run                       立即执行备份+清理(适合 cron/Task Scheduler)
  verify                    对账远端备份完整性(--fix 重传修复)
  daemon                    注册为系统服务并后台运行(开机自启;--uninstall 卸载)
  stop                      停止后台 daemon(注册与自启保留)
  validate                  校验配置(静态 + 网盘连通性)
  version                   显示版本

全局参数:
  --config 路径             指定配置文件,默认 ./config.yaml
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(versionString())
	case "login":
		err = cmdLogin(os.Args[2:])
	case "setup":
		err = cmdSetup(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "daemon":
		err = cmdDaemon(os.Args[2:])
	case "stop":
		err = cmdStop(os.Args[2:])
	case "validate":
		err = cmdValidate(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}
