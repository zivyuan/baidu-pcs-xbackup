# baidu-backup-store 设计文档

日期:2026-10-14

## 背景与目标

基于百度网盘官方开放平台 API( pan.baidu.com/union )实现自动备份工具:定期备份本地指定文件/目录到百度网盘,支持保留份数与备份频率。

- 远端目录结构:`/<远程根目录>/<项目id>/<YYYYMMDD>/<任务名>`,如 `davidoff/20261014/realdata`。
- 一个项目包含多个任务;备份频率最高每天一次,日期目录名即 `YYYYMMDD`。
- 保留份数按任务设置,清理以日期目录为粒度。
- 单二进制(Go)部署,支持 macOS、Windows(含 Cygwin 环境、Windows Server)、Linux(Ubuntu、CentOS、Linux Mint)。
- 不自动编写测试用例,验收方式见文末。

### 非目标

- 增量备份/去重(每次全量上传,依赖官方秒传机制自然去重)。
- 下载/恢复功能(后续可加)。
- 一天多次备份。

## 路线决策记录

- **官方开放平台 API**,不用 BaiduPCS-Go(逆向接口,风控风险)与旧 PCS API(已废弃)。前提:已有开放平台个人应用 AppKey。
- **Go 实现**:纯标准库覆盖 HTTP/tar.gz/调度,单文件交叉编译。
- 上传默认目标为个人应用目录 `/apps/<应用名>/`(个人应用权限范围,可在配置中覆盖远程根目录)。

## 项目结构

```
baidu-backup-store/
├── cmd/bbs/main.go          # CLI 入口
├── internal/config/         # 配置加载、校验、路径规范化(含 cygdrive 转换)
├── internal/baidupan/       # 官方 API 客户端(oauth/上传/列目录/删除)
├── internal/backup/         # 备份引擎(打包、遍历、单次运行)
├── internal/retention/      # 过期备份清理
├── internal/scheduler/      # daemon 调度器
├── internal/service/        # Windows 服务封装(x/sys/windows/svc,构建标签隔离)
├── config.yaml              # 示例配置
├── Makefile                 # 构建发布
└── go.mod
```

## 配置模型

`config.yaml`(CLI 用 `--config` 指定,默认 `./config.yaml`):

```yaml
app_key: "xxx"
secret_key: "xxx"
app_name: "我的备份应用"                   # remote_root 为空时必填
access_token_file: "~/.bbs/token.json"   # 0600 权限
remote_root: ""                          # 空 = /apps/<app_name,此时 app_name 必填>
log_file: ""                             # daemon 模式默认 ./bbs.log,可覆盖
projects:
  - id: davidoff
    tasks:
      - name: realdata
        source: /data/realdata           # 文件或目录
        mode: archive                    # archive=tar.gz | raw=原样递归
        retention: 7                     # 保留份数(按任务)
        daily_at: "03:00"                # 每日执行时刻,daemon 用;run 模式忽略
```

校验规则:项目 id/任务名为网盘合法目录名(允许中文、空格、符号,禁止 `/` `\`、控制字符、`.`/`..`、空名与超 255 字节);retention ≥ 1;mode ∈ {archive, raw};daily_at 为 24 小时制 HH:MM。

## 认证

OAuth 2.0 设备码流程(`bbs login`):

1. 请求 device code,打印 `verification_url` 与用户码,用户浏览器确认。
2. 轮询换取 access_token + refresh_token,存 `access_token_file`(JSON,0600)。
3. scope:`basic,netdisk`。
4. 每次 API 调用前检查 access_token 有效期,过期自动用 refresh_token 续期(refresh_token 长期有效),无需人工介入——daemon/服务模式长期运行同样适用。

## 上传流程(官方三步接口)

公共步骤:逐级创建远端目录(`xpan/file?method=create`,`isdir=1`,已存在则忽略错误)。

**archive 模式**:
1. 源文件/目录流式打包为 tar.gz(临时文件,成功后删除)。
2. 按 4MB 分片计算 MD5 列表。
3. 预上传 precreate(path/size/block_list,rtype=3 覆盖同名)→ 分片上传 superfile2 → 创建 create。秒传命中直接完成。

**raw 模式**:递归遍历源目录,按相对路径逐级建目录,逐文件走上述三步(小文件即单片)。

**完成标志**(任务级,避免同项目多任务互相干扰):
- raw 模式:全部文件成功后,在 `<日期>/<任务名>/` 下上传 `_SUCCESS` 文件。
- archive 模式:单文件上传要么成功要么失败,以 `<日期>/<任务名>.tar.gz` 存在即为完成,无需额外标志。

## 清理策略

每次备份任务成功后执行,删除粒度为**任务级**(不删日期目录中其他任务的数据):

1. 列出 `/<根>/<项目id>/` 下全部日期目录(`xpan/file?method=list`)。
2. 筛选出包含该任务数据的日期目录(raw:存在 `任务名` 子目录;archive:存在 `任务名.tar.gz`)。
3. 按日期降序排序,保留前 retention 个,其余仅删除该任务的数据:`/<日期>/<任务名>`(raw)或 `/<日期>/<任务名>.tar.gz`(archive)。
4. 删除后若日期目录已空,顺带删除该空日期目录(失败忽略)。
5. 删除仅在上传全量成功后进行,避免误删唯一备份。

## 幂等与调度

- **幂等**:任务执行前检查本任务当日完成标志(见"完成标志"),已完成则跳过(单次与 daemon 共用)。因此 cron/Task Scheduler 与 daemon 混跑也安全,崩溃重启自然续上。
- **配置路径**:`--config` 参数 > `BBS_CONFIG` 环境变量 > `./config.yaml`。
- **单次模式** `bbs run [--project X] [--task Y]`:立即执行全部(或指定)项目任务,适合 cron / Task Scheduler。
- **daemon 模式** `bbs daemon`:常驻进程。启动时为每个任务计算"下一次到期时刻"(今天 daily_at,已过则明天),到点逐个执行,随后休眠至最近到期点。时间比较基于本地时区。

## CLI

```
bbs login [--config F]              # 设备码授权
bbs run [--config F] [--project P] [--task T]   # 单次备份+清理
bbs daemon [--config F]             # 常驻调度
bbs service install|uninstall|start|stop        # 仅 Windows:注册/管理服务
bbs version
```

## 跨平台兼容(macOS / Windows / Linux)

- 纯 Go 标准库,无 cgo,静态编译(`CGO_ENABLED=0`),各平台产物均为单文件、零运行时依赖。
- **Linux(Ubuntu / CentOS / Linux Mint)**:同一份 `linux/amd64` 产物通用(另出 `linux/arm64`);静态链接不依赖 glibc 版本,CentOS 7 等老发行版可直接运行。daemon 自启动用 systemd(随文档提供 unit 示例),单次模式用 cron。
- **macOS**:`darwin/arm64` 与 `darwin/amd64`;daemon 自启动用 launchd,单次模式用 cron/launchd。
- **Windows / Cygwin**:`GOOS=windows GOARCH=amd64 go build` 产出单 exe,Win10 ~ Server 2022 通用。
- 产物为原生 PE 二进制,不依赖 cygwin1.dll,可在 Cygwin shell 直接运行。
- 路径规范化:配置与参数中出现 `/cygdrive/<盘符>/...` 时自动转为 `<盘符>:\...`;其余按原样处理。macOS/Windows 路径均可。
- `~` 展开基于 `os.UserHomeDir()`(Windows 下为用户目录)。
- **Windows 服务**:`bbs service install` 用 `golang.org/x/sys/windows/svc` 注册为真服务(服务名 `bbs`),服务进程内执行 daemon 逻辑,日志写文件;支持系统服务管理器控制与故障自动重启(恢复策略随 install 设置)。service 代码用 `//go:build windows` 隔离,非 Windows 构建不受影响。
- 非 Windows 平台 daemon 为前台进程,自启动交给用户(launchd/cron)。

## 错误处理与日志

- 单任务失败:记录错误,继续下一个任务;进程退出码非 0。
- 分片上传失败:重试 3 次,指数退避(1s/2s/4s)。
- HTTP 层对 4xx/5xx 统一解析 errno 错误码并输出可读信息。
- 日志:结构化(时间/级别/项目/任务/事件),单次模式输出 stderr(daemon/服务模式写 log_file)。token、secret_key 永不落日志。
- access_token 过期自动刷新失败时报错并提示重新 login。

## 发布

Makefile 目标:`build-darwin`(arm64/amd64)、`build-linux`(amd64/arm64)、`build-windows`(amd64)、`release`(按平台打包 zip/tar.gz 到 dist/)。

## 验收方式

1. `bbs login` 完成授权,token 文件生成且权限 0600。
2. 手动 `bbs run`,在网盘核对 `/<根>/<项目id>/<日期>/<任务名>` 结构与内容。
3. 再次 `bbs run` 验证幂等跳过。
4. 将 retention 临时设为 1,连续两天备份后确认最旧日期目录被清理。
5. `bbs daemon` 前台运行,把某任务 daily_at 设为 2 分钟后,验证到点自动执行。
6. Windows:交叉编译 exe,在 Cygwin shell 与 cmd 各跑一次 `bbs run`;`bbs service install` 后在 services.msc 确认服务运行并完成一次备份。
7. Linux(Ubuntu/CentOS/Mint 任一):`linux/amd64` 产物直接运行 `bbs run` 与 `bbs daemon` 前台模式,systemd unit 拉起 daemon 验证常驻。
