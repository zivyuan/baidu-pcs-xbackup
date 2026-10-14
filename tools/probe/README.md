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

拿 capture 里同类操作的请求与 `internal/baidupan/web.go` 逐项对比。
原则:**官方客户端实际发什么,web.go 就跟什么**。BaiduPCS-Go 的组合只是
起点,以真实流量为准。

## 已验证端点(2026-10 实测,Go + 纯 BDUSS + netdisk UA)

| 操作 | 端点 | 要点 |
|------|------|------|
| 列目录 | `GET pan.baidu.com/rest/2.0/xpan/file?method=list&dir=&num=` | 偶发 errno=-6 风控抖动,退避重试即过 |
| 建目录 | `POST pan.baidu.com/api/create?a=mkdir` | 表单 path/isdir=1;-8 已存在 |
| 删除 | `POST pan.baidu.com/api/filemanager?opera=delete` | **必须带 bdstoken**;表单 filelist=json 数组;8001=不存在 |
| bdstoken | `GET pan.baidu.com/api/gettemplatevariable?fields=["bdstoken"]` | 写操作前取一次缓存 |
| 上传 | `api/precreate` → `POST d.pcs.baidu.com/rest/2.0/pcs/superfile2`(multipart,**必带 app_id=250528**)→ `api/create` | 分片 4MB |
| 下载 | 列父目录取 fs_id → `xpan/file?method=filemetas&dlink=1`(响应键 **info** 非 list)→ GET dlink | UA 必须 netdisk |
| 配额 | `GET pan.baidu.com/api/quota?checkfree=1&checkexpire=1&web=1` | login 校验用 |

踩坑记录(失效时优先复查):

- `pcs.baidu.com/rest/2.0/pcs/file`(*method=list/mkdir/delete/download/meta*)
  已对纯 BDUSS 恒返回 **31030 pcs token not exist**,整族废弃。
- `api/create?a=mkdir` 对已存在路径**不报错而是自动改名新建**(`<名>_时间戳`),
  必须先列父目录确认存在再创建;`xpan/file?method=create` 则不认 BDUSS(9019)。
- `pan.baidu.com/api/list`(web=1)需要页面 JS 种的反爬 Cookie(ndut_fmt 等),
  Go 无法直接复用,不要走这条路;`xpan/file?method=meta` 同样不认 BDUSS。
- Python urllib 即使带全 Cookie 也持续 -6(TLS 指纹风控),探测必须用 Go
  或浏览器内 fetch,结论才对产品有效。

## 附带产物

- `bduss.json`:login 时生成,格式与 `~/.bpxup/bduss.json` 一致,可直接拷贝
  给 Go 工具做联调(勿提交,已在 .gitignore)。
- 接口失效时的流程:重跑 `record` → 对比 `capture/` 里新旧两份 JSON →
  定位变动端点/参数 → 改 `web.go` → 用 bduss.json 联调验证。
