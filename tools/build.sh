#!/bin/sh
# bpxup 一键构建:测试 → 五平台产物+打包 → SHA256 校验和
# 用法:
#   tools/build.sh            开发版(版本自动取 git describe)
#   tools/build.sh 0.2.1      指定版本号(仅影响产物内嵌版本,不打 tag)
# 发布正式版仍用:make publish VERSION=x.y.z
set -e
cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
echo "== 测试 =="
go test ./... -count=1

echo "== 构建 $VERSION =="
rm -rf dist
make release VERSION="$VERSION"

echo "== 校验和 =="
cd dist
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum bpxup-* > SHA256SUMS
else
	shasum -a 256 bpxup-* > SHA256SUMS
fi
cd ..

echo "== 完成 =="
ls -lh dist/
if [ "$(uname -s)/$(uname -m)" = "Darwin/arm64" ]; then
	cd dist && tar -xzf bpxup-darwin-arm64.tar.gz && ./bpxup version && rm bpxup && cd ..
fi
echo "服务器一般用 dist/bpxup-linux-amd64.tar.gz(解压后即 bpxup)"
