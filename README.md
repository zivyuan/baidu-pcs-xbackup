# baidu-pcs-xbackup(bpxup)- 百度网盘自动备份工具

基于百度网盘官方开放平台 API 的定时备份工具。Go 静态编译,单文件部署,支持 macOS、Linux(Ubuntu/CentOS/Mint 等)、Windows(含 Cygwin、Windows Server)。

## 功能

- 多项目 / 多任务,每个任务可指定本地文件或目录
- 两种备份形态:`archive`(打包 tar.gz 上传)/ `raw`(原样递归上传)
- 备份频率最高每天一次,远端目录结构:`<根目录>/<项目id>/<YYYYMMDD>/<任务名>`
- 按任务保留最近 N 份,过期日期目录自动整体删除
- 任务防冲突:项目目录下 `<任务名>.fingerprint` 记录机器识别码(本地 `~/.bpxup/fingerprint`),识别码不一致(其他机器同项目同任务)时拒绝执行
- 幂等:当日已备份则跳过,cron / Task Scheduler / daemon 混跑安全
- 常驻模式(`bpxup daemon`),Windows 上可注册为系统服务(故障自动重启)
- 失败有界重试(默认 3 次),耗尽后任务终止并(可选)企业微信 webhook 告警,等待人工干预
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
retry:
  max_attempts: 3                 # 总尝试次数;interval=30s(run)/10m(daemon)可配
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
# 1. 首次授权(OAuth 设备码,浏览器确认一次,之后自动续期)
./bpxup login --config config.yaml

# 2. 单次执行(适合 cron / Task Scheduler)
./bpxup run --config config.yaml [--project davidoff] [--task realdata]

# 3. 常驻调度
./bpxup daemon --config config.yaml

# 4. 备份完整性巡检(默认校验每个任务最新一份;--fix 重传修复差异)
./bpxup verify --config config.yaml [--project davidoff] [--task realdata] [--date 20261015] [--fix]
```

配置路径优先级:`--config` 参数 > `BPXUP_CONFIG` 环境变量 > `./config.yaml`。
daemon 启动后不感知配置变化,改配置需重启进程。

## 各平台自启动

### cron(macOS / Linux,单次模式)

```cron
0 3 * * * /usr/local/bin/bpxup run --config /etc/bpxup/config.yaml
```

### systemd(Linux,daemon 模式)

```ini
# /etc/systemd/system/bpxup.service
[Unit]
Description=bpxup 百度网盘备份守护进程
After=network-online.target

[Service]
ExecStart=/usr/local/bin/bpxup daemon --config /etc/bpxup/config.yaml
Restart=always
RestartSec=30

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload && systemctl enable --now bpxup
```

### launchd(macOS,daemon 模式)

写一个 plist(`ProgramArguments` 为 `bpxup daemon --config ...`),`launchctl load` 加载。

### Windows(daemon 注册为系统服务)

管理员权限执行:

```bat
bpxup service install --config C:\bpxup\config.yaml
bpxup service start
```

服务名 `bpxup`,开机自启,崩溃自动重启(30s/30s/60s)。也可在 services.msc 中管理。
卸载:`bpxup service stop && bpxup service uninstall`。

**重要**:服务以 LocalSystem 身份运行,与用户账户隔离——
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

一键完成:打 `vX.Y.Z` tag 推送 → 五平台打包 → 创建 GitHub Release。
规范:版本号语义化;产物包名 `bpxup-<os>-<arch>.tar.gz|.zip`,**包内统一为 `bpxup`(.exe),解压即用**;重发同一版本需先删 GitHub Release 与远端 tag。

## 注意事项

- 目录结构:`/<app_name>/<项目id>/<日期>/<任务名>`;`app_name` 留空则用 `/apps`,显式配置 `remote_root` 时优先。
- access_token 约 30 天过期,工具自动用 refresh_token 续期;refresh_token 失效时重新 `bpxup login`。
- 日志为结构化文本,token 与 secret 永不写入日志。

## 许可证

[Apache-2.0](LICENSE):免费开源,可自由使用、修改、分发(含商用),保留版权与许可声明即可。
