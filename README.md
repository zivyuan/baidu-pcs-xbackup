# baidu-pcs-xbackup(bpxup)- 百度网盘自动备份工具

定时备份工具,支持两种认证模式:
- **oauth**(默认):百度网盘开放平台 API,需要企业 AppKey(开放平台现已只对企业开放);
- **web**:网页版网盘接口 + BDUSS Cookie(思路同 BaiduPCS-Go),个人账号可用。

Go 静态编译,单文件部署,支持 macOS、Linux(Ubuntu/CentOS/Mint 等)、Windows(含 Cygwin、Windows Server)。

## 功能

- 多项目 / 多任务,每个任务可指定本地文件或目录
- 两种备份形态:`archive`(打包 tar.gz 上传)/ `raw`(原样递归上传)
- 备份频率最高每天一次,远端目录结构:`<根目录>/<项目id>/<YYYYMMDD>/<任务名>`
- 按任务保留最近 N 份,过期日期目录自动整体删除
- 任务防冲突:项目目录下 `<任务名>.fingerprint` 记录机器识别码(本地 `~/.bpxup/fingerprint`),识别码不一致(其他机器同项目同任务)时拒绝执行
- 幂等:当日已备份则跳过,cron / Task Scheduler / daemon 混跑安全
- 常驻模式(`bpxup daemon`),Windows 上可注册为系统服务(故障自动重启)

## 配置

复制 `config.example.yaml` 为 `config.yaml`(**此文件含密钥,已在 .gitignore 中,勿入库**):

```yaml
auth: oauth                       # oauth | web
app_key: "你的AppKey"             # auth: oauth 时必填
secret_key: "你的SecretKey"
app_name: "项目数据备份"           # 备份根目录名(建在网盘根下);留空 = /apps
access_token_file: "~/.bpxup/token.json"   # oauth 模式凭证
bduss_file: "~/.bpxup/bduss.json"          # web 模式凭证
log_file: ""                      # daemon 模式默认 ./bpxup.log
projects:
  - id: davidoff
    tasks:
      - name: realdata
        source: /data/realdata    # 文件或目录
        mode: archive             # archive | raw
        retention: 7              # 保留份数
        daily_at: "03:00"         # 每日执行时刻(daemon 用)
```

个人账号(无企业 AppKey)把 `auth` 改为 `web` 即可,`app_key`/`secret_key` 不再需要。

## 使用

```bash
# 1. 首次登录
#    oauth 模式:设备码授权,浏览器确认一次,之后自动续期
#    web 模式:粘贴网页版网盘 Cookie 中的 BDUSS
./bpxup login --config config.yaml

# 2. 单次执行(适合 cron / Task Scheduler)
./bpxup run --config config.yaml [--project davidoff] [--task realdata]

# 3. 常驻调度
./bpxup daemon --config config.yaml
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

## web 模式须知(auth: web)

走的是网页版非公开接口(参考 BaiduPCS-Go 的可用端点组合),使用前必须了解:

- **非公开接口,随时可能失效**。无人值守场景下接口变动意味着备份悄悄断掉,请自行配置对 `run` 失败的告警(cron 邮件、监控面板等)。
- **账号风险**:使用非官方接口原则上违反百度用户协议,存在限流/风控可能,建议用小号专门跑备份。
- **BDUSS 生命周期**:长期有效但不可自动续期,改密码等操作会使其失效;失效后重新执行 `bpxup login` 粘贴新 BDUSS 即可。
- BDUSS 即登录凭证,`bduss_file`(默认 `~/.bpxup/bduss.json`,0600)等同密码,勿泄露勿入库。

## 许可证

[Apache-2.0](LICENSE):免费开源,可自由使用、修改、分发(含商用),保留版权与许可声明即可。
