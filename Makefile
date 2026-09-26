.PHONY: build test fmt verify ci clean release-check

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o build/nsl .

test:
	go test ./...

fmt:
	gofmt -w main.go main_test.go

verify:
	go mod tidy -diff
	go vet ./...
	test -z "$$(gofmt -l main.go main_test.go)"
	go test ./...

ci: verify
	go test -race ./...
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o build/nsl-linux-amd64 .
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o build/nsl-linux-arm64 .

release-check:
	goreleaser check

clean:
	go clean
	rm -f build/nsl build/nsl-linux-amd64 build/nsl-linux-arm64
