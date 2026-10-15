package main

import (
	"fmt"
	"os"
)

var version = "0.1.0"

func usage() {
	fmt.Fprintf(os.Stderr, `bpxup - 百度网盘自动备份工具

用法:
  bpxup <命令> [参数]

命令:
  login                     设备码授权,获取并保存 token
  run                       立即执行备份+清理(适合 cron/Task Scheduler)
  daemon                    常驻调度模式
  service <install|uninstall|start|stop>   Windows 服务管理(仅 Windows)
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
		fmt.Println("bpxup", version)
	case "login":
		err = cmdLogin(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "daemon":
		err = cmdDaemon(os.Args[2:])
	case "service":
		err = cmdService(os.Args[2:])
	case "-service-run": // 服务本体入口:SCM 以此参数拉起,必须先于 default 分支路由到 svc.Run
		err = serviceDispatch(os.Args[1:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}
