#!/usr/bin/env python3
"""bpxup 接口探测工具(仅开发阶段使用,不参与 Go 构建)。

用途:驱动真实浏览器访问网页版网盘,录制 pan.baidu.com / pcs.baidu.com /
d.pcs.baidu.com 的全部 XHR 流量,用于:
  1. 验证 internal/baidupan/web.go 的端点/参数推断是否与官方客户端一致;
  2. 接口失效时,重跑场景对比新旧流量,快速定位变动的端点或参数。

用法:
  python probe.py login    # 首次:打开浏览器,人工扫码登录,保存会话与 BDUSS
  python probe.py record   # 打开网盘页,人工随意操作,回车结束并落盘录制
  python probe.py auto     # 自动化场景:上传探针文件/建目录/删目录(尽力而为)

产物:
  state.json    浏览器会话(含 cookie,勿提交)
  bduss.json    与 bpxup ~/.bpxup/bduss.json 同格式,可直接拷给 Go 工具联调
  capture/*.json  每次录制的流量清单(已脱敏:cookie 只留名字)
"""

import json
import os
import re
import sys
import time

from playwright.sync_api import sync_playwright

HERE = os.path.dirname(os.path.abspath(__file__))
STATE = os.path.join(HERE, "state.json")
BDUSS_OUT = os.path.join(HERE, "bduss.json")
CAPTURE_DIR = os.path.join(HERE, "capture")

WATCH_HOSTS = ("pan.baidu.com", "pcs.baidu.com", "d.pcs.baidu.com")
# 响应体里可能出现的大文件排除用:只截取 JSON 小响应
MAX_BODY = 8192


def now():
    return time.strftime("%Y%m%d-%H%M%S")


def redact_headers(headers):
    """保留可复现请求所需的头;cookie 只留名字,值一律脱敏。"""
    keep = {}
    for k, v in headers.items():
        lk = k.lower()
        if lk == "cookie":
            keep[k] = "; ".join(
                p.split("=", 1)[0] + "=***" for p in v.split(";") if "=" in p
            )
        elif lk in ("user-agent", "content-type", "referer", "accept", "origin"):
            keep[k] = v
    return keep


def attach_recorder(page, records, label):
    def on_request(req):
        try:
            if not any(h in req.url for h in WATCH_HOSTS):
                return
            if req.resource_type not in ("xhr", "fetch"):
                return
            post = req.post_data or ""
            # 表单里的长 block_list 截断,保持录制可读
            if len(post) > MAX_BODY:
                post = post[:MAX_BODY] + "...(截断)"
            records.append(
                {
                    "ts": time.time(),
                    "op": label,
                    "method": req.method,
                    "url": req.url,
                    "post_data": post,
                    "headers": redact_headers(req.headers),
                }
            )
        except Exception:
            pass

    def on_response(resp):
        # 关联响应摘要:状态码 + errno(若有)
        try:
            req = resp.request
            for r in records:
                if r["url"] == req.url and "status" not in r:
                    r["status"] = resp.status
                    ct = resp.headers.get("content-type", "")
                    if "json" in ct:
                        body = resp.text()[:2048]
                        m = re.search(r'"errno"\s*:\s*(-?\d+)', body)
                        if m:
                            r["resp_errno"] = int(m.group(1))
                    break
        except Exception:
            pass

    page.on("request", on_request)
    page.on("response", on_response)


def save_records(records, label):
    os.makedirs(CAPTURE_DIR, exist_ok=True)
    path = os.path.join(CAPTURE_DIR, f"{now()}-{label}.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(records, f, ensure_ascii=False, indent=1)
    print(f"\n已录制 {len(records)} 条请求 → {path}")
    for r in records:
        u = r["url"].split("?")[0]
        err = r.get("resp_errno", "")
        print(f"  [{r['status'] if 'status' in r else '?'}] {r['method']:<5} {u}  errno={err}")
    return path


def launch(pw, headless=False):
    browser = pw.chromium.launch(headless=headless)
    ctx = browser.new_context(
        storage_state=STATE if os.path.exists(STATE) else None,
        # 网盘对 UA 敏感,用桌面 Chrome UA
        user_agent=(
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
            "(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
        ),
    )
    return browser, ctx


def get_bduss(ctx):
    for c in ctx.cookies("https://pan.baidu.com"):
        if c["name"] == "BDUSS":
            return c["value"]
    return None


def cmd_login(pw):
    browser, ctx = launch(pw)
    page = ctx.new_page()
    page.goto("https://pan.baidu.com")
    print("请在浏览器完成登录(扫码/账号密码),检测到 BDUSS 后自动继续...")
    bduss = None
    for _ in range(300):  # 最长等 5 分钟
        bduss = get_bduss(ctx)
        if bduss:
            break
        page.wait_for_timeout(1000)
    if not bduss:
        print("超时未检测到 BDUSS")
        sys.exit(1)
    ctx.storage_state(path=STATE)
    with open(BDUSS_OUT, "w", encoding="utf-8") as f:
        json.dump({"bduss": bduss}, f, ensure_ascii=False, indent=2)
    print(f"登录成功,会话 → {STATE}")
    print(f"BDUSS(bpxup 格式,可拷作 ~/.bpxup/bduss.json)→ {BDUSS_OUT}")
    browser.close()


def cmd_record(pw):
    browser, ctx = launch(pw)
    page = ctx.new_page()
    records = []
    attach_recorder(page, records, "manual")
    page.goto("https://pan.baidu.com/disk/main")
    print("请在浏览器里手工操作(列目录/上传/删除/下载...),完成后回到终端按回车结束。")
    input()
    ctx.storage_state(path=STATE)  # 顺手续期会话
    save_records(records, "manual")
    browser.close()


def cmd_auto(pw):
    """自动化场景,选择器尽量宽松;失败不影响录制产物。"""
    browser, ctx = launch(pw)
    page = ctx.new_page()
    records = []
    attach_recorder(page, records, "auto")
    page.goto("https://pan.baidu.com/disk/main")
    page.wait_for_load_state("networkidle")

    probe_dir = "bpxup-probe"
    stamp = time.strftime("%H%M%S")

    def step(name, fn):
        try:
            fn()
            print(f"  ✓ {name}")
        except Exception as e:
            print(f"  ✗ {name}: {e}(选择器可能变了,改用 record 模式手工操作)")

    # 1. 建目录
    def mkdir():
        page.get_by_title("新建文件夹").click(timeout=5000)
        page.locator(".dbl-click-input, input[class*=input]").fill(probe_dir + "-" + stamp)
        page.keyboard.press("Enter")
        page.wait_for_timeout(2000)
    step("新建目录", mkdir)

    # 2. 上传探针文件
    def upload():
        probe_file = os.path.join(HERE, f"probe-{stamp}.txt")
        with open(probe_file, "w") as f:
            f.write(f"bpxup probe {stamp}\n" + "x" * 1024)
        page.get_by_title("上传文件").click(timeout=5000)
        page.wait_for_selector("input[type=file]", timeout=5000)
        page.locator("input[type=file]").set_input_files(probe_file)
        page.wait_for_timeout(5000)  # 等上传链路(precreate/superfile2/create)完成
        os.remove(probe_file)
    step("上传探针文件", upload)

    # 3. 下载(点第一个文件)
    def download():
        rows = page.locator(r"[class*=table] [class*=fileName], dd[class*=file]")
        rows.first.hover(timeout=5000)
        # 下载按钮通常悬浮出现;失败就算了,record 模式可补
        page.get_by_title("下载").first.click(timeout=5000)
        page.wait_for_timeout(3000)
    step("触发下载", download)

    save_records(records, "auto")
    ctx.storage_state(path=STATE)
    browser.close()


def main():
    if len(sys.argv) < 2 or sys.argv[1] not in ("login", "record", "auto"):
        print(__doc__)
        sys.exit(2)
    with sync_playwright() as pw:
        {"login": cmd_login, "record": cmd_record, "auto": cmd_auto}[sys.argv[1]](pw)


if __name__ == "__main__":
    main()
