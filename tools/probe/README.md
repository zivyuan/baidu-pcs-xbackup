# 接口探测工具(仅开发阶段)

用 Playwright 驱动真实浏览器访问网页版网盘,录制官方客户端的 XHR 流量,
用于验证/修正 `internal/baidupan/web.go` 的端点与参数。不参与 Go 构建,
不随二进制分发。

## 安装

```bash
cd tools/probe
pip install -r requirements.txt
playwright install chromium
```

## 使用

```bash
python probe.py login    # 首次:人工扫码登录,保存会话 state.json 与 bduss.json
python probe.py record   # 人工操作网盘(列目录/上传/删除/下载),回车结束,录制落盘
python probe.py auto     # 自动化场景:建目录/上传探针文件/触发下载(选择器失效则用 record)
```

录制产物在 `capture/*.json`(cookie 值已脱敏,只留名字),终端同时打印
请求摘要(method 路径 状态码 errno)。

## 对账方法

拿 capture 里同类操作的请求与 `internal/baidupan/web.go` 逐项对比:

| 操作 | web.go 当前实现 | 官方客户端真实流量看什么 |
|------|----------------|------------------------|
| 列目录 | `pcs.baidu.com/rest/2.0/pcs/file?method=list` | 是否改走 `pan.baidu.com/api/list`,必带参数(web/channel/app_id?) |
| 建目录 | `pcs .../pcs/file?method=mkdir` | 同上,注意 `api/create` 的表单字段 |
| 删除 | `pcs .../pcs/file?method=delete`(multipart param) | 参数结构、是否需要 sign |
| 上传 | `api/precreate` → `pcs superfile2` → `api/create` | 三步的 URL/表单/必填字段、分片大小 |
| 下载 | `pcs .../pcs/file?method=download` 302 | dlink 获取方式、UA/Referer 校验 |

原则:**官方客户端实际发什么,web.go 就跟什么**。BaiduPCS-Go 的组合只是
起点,以真实流量为准。

## 附带产物

- `bduss.json`:login 时生成,格式与 `~/.bpxup/bduss.json` 一致,可直接拷贝
  给 Go 工具做联调(勿提交,已在 .gitignore)。
- 接口失效时的流程:重跑 `record` → 对比 `capture/` 里新旧两份 JSON →
  定位变动端点/参数 → 改 `web.go` → 用 bduss.json 联调验证。
