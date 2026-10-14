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

release: dist
	cd dist && tar czf $(BINARY)-darwin-arm64.tar.gz $(BINARY)-darwin-arm64 && rm -rf $(BINARY)-darwin-arm64
	cd dist && tar czf $(BINARY)-darwin-amd64.tar.gz $(BINARY)-darwin-amd64 && rm -rf $(BINARY)-darwin-amd64
	cd dist && tar czf $(BINARY)-linux-amd64.tar.gz $(BINARY)-linux-amd64 && rm -rf $(BINARY)-linux-amd64
	cd dist && tar czf $(BINARY)-linux-arm64.tar.gz $(BINARY)-linux-arm64 && rm -rf $(BINARY)-linux-arm64
	cd dist && zip -q $(BINARY)-windows-amd64.zip $(BINARY)-windows-amd64.exe && rm -rf $(BINARY)-windows-amd64.exe

.PHONY: build dist release
