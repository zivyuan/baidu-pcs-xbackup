BINARY := bpxup
# 开发构建默认取 git 真实位置(上个 tag+领先提交数+sha+是否脏),仅无 git 信息时兜底 dev
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/bpxup

dist:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-arm64 ./cmd/bpxup
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-amd64 ./cmd/bpxup
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-amd64 ./cmd/bpxup
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-arm64 ./cmd/bpxup
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-windows-amd64.exe ./cmd/bpxup

# 压缩包内统一重命名为 bpxup(.exe),解压即用;平台信息只体现在包名
release: dist
	cd dist && cp $(BINARY)-darwin-arm64 $(BINARY) && tar czf $(BINARY)-darwin-arm64.tar.gz $(BINARY) && rm $(BINARY) $(BINARY)-darwin-arm64
	cd dist && cp $(BINARY)-darwin-amd64 $(BINARY) && tar czf $(BINARY)-darwin-amd64.tar.gz $(BINARY) && rm $(BINARY) $(BINARY)-darwin-amd64
	cd dist && cp $(BINARY)-linux-amd64 $(BINARY) && tar czf $(BINARY)-linux-amd64.tar.gz $(BINARY) && rm $(BINARY) $(BINARY)-linux-amd64
	cd dist && cp $(BINARY)-linux-arm64 $(BINARY) && tar czf $(BINARY)-linux-arm64.tar.gz $(BINARY) && rm $(BINARY) $(BINARY)-linux-arm64
	cd dist && cp $(BINARY)-windows-amd64.exe $(BINARY).exe && zip -q $(BINARY)-windows-amd64.zip $(BINARY).exe && rm $(BINARY).exe $(BINARY)-windows-amd64.exe

# 一键发布:校验 → 打 tag 推送 → 五平台打包 → 创建 GitHub Release。
# 发布规范(固化):
#   - 版本号语义化 x.y.z,tag 格式 vX.Y.Z,与二进制内嵌 version 一致
#   - 产物包名 bpxup-<os>-<arch>.tar.gz|.zip,包内统一 bpxup(.exe)
#   - 仅允许在干净工作区发布;重发同一版本需先删 GitHub Release 与远端 tag
publish:
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "必须显式传入语义化版本,用法: make publish VERSION=x.y.z(不接受 git describe 的 v 前缀或开发态后缀)"; exit 1; }
	@git diff --quiet && git diff --cached --quiet || { echo "工作区有未提交改动,先提交"; exit 1; }
	git tag v$(VERSION)
	git push origin v$(VERSION)
	$(MAKE) release VERSION=$(VERSION)
	gh release create v$(VERSION) dist/* --title "v$(VERSION)" --generate-notes

.PHONY: build dist release publish
