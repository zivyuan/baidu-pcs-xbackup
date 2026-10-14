BINARY := bpxup
VERSION ?= dev
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

.PHONY: build dist release
