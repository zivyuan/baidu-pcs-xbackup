# baidu-pcs-xbackup(bpxup)- 百度网盘自动备份工具

基于百度网盘官方开放平台 API 的定时备份工具。Go 静态编译,单文件部署,支持 macOS、Linux(Ubuntu/CentOS/Mint 等)、Windows(含 Cygwin、Windows Server)。

## 功能

- 多项目 / 多任务,每个任务可指定本地文件或目录
- 两种备份形态:`archive`(打包 tar.gz 上传)/ `raw`(原样递归上传)
- 备份频率最高每天一次,远端目录结构:`<根目录>/<项目id>/<YYYYMMDD>/<任务名>`
- 按任务保留最近 N 份,过期日期目录自动整体删除
- 任务防冲突:项目目录下 `<任务名>.fingerprint` 记录机器识别码(本地 `~/.bpxup/fingerprint`),识别码不一致(其他机器同项目同任务)时拒绝执行
- 幂等:当日已备份则跳过,cron / Task Scheduler / daemon 混跑安全
- 常驻模式(`bpxup daemon`):自动注册系统服务(systemd / launchd / Windows 服务,故障自动重启),配置热重载免重启;`bpxup stop` 停止,`bpxup daemon --uninstall` 卸载
- 失败有界重试(默认 3 次),耗尽后任务终止并(可选)企业微信 webhook 告警,等待人工干预;`notify_on` 可选仅失败(默认)或成功+失败都通知
- `bpxup validate` 启动前校验:配置静态检查 + 网盘连通性探测
- 完整性对账:上传前与远端比对(存在 + 大小一致),只补缺失/不一致的文件——中断重跑即续传;上传完成后再复核一轮,全一致才打完成标志,防接口假成功误判
- `bpxup verify` 手动巡检:对账远端备份完整性,默认只报告,`--fix` 重传修复
- 大文件分片断点续传:uploadid 与分片清单持久化( `~/.bpxup/uploads.json` ),任务级重试复用同一会话只补缺失分片;分片遇网关 500/504 按 30s 起指数退避重试 6 次(封顶 5 分钟)

## 配置

复制 `config.example.yaml` 为 `config.yaml`:

```yaml
app_key: "你的AppKey"
secret_key: "你的SecretKey"
app_name: "项目数据备份"           # 备份根目录名(建在网盘根下);留空 = /apps
access_token_file: "~/.bpxup/token.json"
log_file: ""                      # daemon 模式默认 ./bpxup.log
webhook_url: ""                   # 企业微信机器人 webhook;留空 = 不通知
notify_on: failure                # failure(默认,仅失败) | all(成功和失败)
retry:
  max_attempts: 3                 # 总尝试次数(含首次)
  interval: 30s                   # run 模式重试间隔
  daemon_interval: 10m            # daemon 模式重试间隔
projects:
  - id: davidoff
    tasks:
      - name: realdata
        source: /data/realdata    # 文件或目录
        mode: archive             # archive | raw
        retention: 7              # 保留份数
        daily_at: "03:00"         # 每日执行时刻(daemon 用)
```

## 使用

```bash
# 1. 交互式引导(推荐):配置 AppKey/SecretKey、第一个项目(可跳过)、
#    设备授权一步完成;结束后可选继续配置 webhook/重试/日志
./bpxup setup

# 手动方式:
# 1. 首次授权(OAuth 设备码,浏览器确认一次,之后自动续期)
./bpxup login --config config.yaml

# 2. 单次执行(适合 cron / Task Scheduler)
./bpxup run --config config.yaml [--project davidoff] [--task realdata]

# 3. 常驻调度(自动注册系统服务并启动;无 systemd/launchd 时前台运行)
./bpxup daemon --config config.yaml
./bpxup stop                     # 停止(注册与开机自启保留)
./bpxup daemon --uninstall       # 停止并取消注册

# 4. 备份完整性巡检(默认校验每个任务最新一份;--fix 重传修复差异)
./bpxup verify --config config.yaml [--project davidoff] [--task realdata] [--date 20261015] [--fix]

# 5. 启动前校验(配置静态检查 + 网盘连通性)
./bpxup validate --config config.yaml
```

配置路径优先级:`--config` 参数 > `BPXUP_CONFIG` 环境变量 > `./config.yaml`。
daemon 运行中自动热重载配置(含通知模式),无需重启。

## 各平台自启动

### cron(macOS / Linux,单次模式)

```cron
0 3 * * * /usr/local/bin/bpxup run --config /etc/bpxup/config.yaml
```

### systemd / launchd / Windows 服务(daemon 模式,自动注册)

`bpxup daemon --config <路径>` 自动完成:写入 systemd unit / launchd plist / 注册 Windows 服务 → 设置开机自启 → 启动。服务名统一 `bpxup`,故障自动重启,日志见启动时提示的路径。

停止:`bpxup stop`(注册与自启保留);卸载:`bpxup daemon --uninstall`。
无 systemd/launchd 的环境(如部分容器)自动回退前台运行,Ctrl+C 退出。

**Windows 注意**:服务以 LocalSystem 身份运行,与用户账户隔离——
- `access_token_file` 与 `log_file` 必须写**绝对路径**(不要 `~`、不要相对路径),且 LocalSystem 可访问;
- 在用户账户下 `bpxup login` 生成的 token 文件,需复制到服务可读的位置(或直接把服务配置指向该绝对路径)。

## Cygwin 说明

产物为原生 Windows 二进制,不依赖 cygwin1.dll,Cygwin shell 直接运行即可。
配置/参数中的路径写 Windows 风格(`C:\data\x`)或 Cygwin 风格(`/cygdrive/c/data/x`)均可,工具自动转换。

## 构建

```bash
make build    # 当前平台
make dist     # 五平台:darwin-arm64/amd64、linux-amd64/arm64、windows-amd64
make release  # dist + 打包压缩
```

### 发布

```bash
make publish VERSION=0.1.0
```

一键完成:打 `vX.Y.Z` tag 推送 → GitHub Actions 自动五平台打包 → 创建 GitHub Release(含 SHA256SUMS),进度见 Actions 页面。
规范:版本号语义化;产物包名 `bpxup-<os>-<arch>.tar.gz|.zip`,**包内统一为 `bpxup`(.exe),解压即用**;重发同一版本需先删 GitHub Release 与远端 tag。

## 注意事项

- 目录结构:`/<app_name>/<项目id>/<日期>/<任务名>`;`app_name` 留空则用 `/apps`,显式配置 `remote_root` 时优先。
- access_token 约 30 天过期,工具自动用 refresh_token 续期;refresh_token 失效时重新 `bpxup login`。
- 日志为结构化文本,token 与 secret 永不写入日志。

## 许可证

[Apache-2.0](LICENSE):免费开源,可自由使用、修改、分发(含商用),保留版权与许可声明即可。
