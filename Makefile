.PHONY: build agent test fmt verify ci clean release-check site site-serve

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o build/nsl .

# The VM image embeds the agent; it runs on x86-64 VMs whatever the host.
agent:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o build/nsl-agent ./cmd/nsl-agent

test:
	go test ./...

fmt:
	gofmt -w .

verify:
	python3 -m unittest discover -s scripts -p 'test_*.py'
	go mod tidy -diff
	python3 scripts/license-notices.py --check
	go vet ./...
	test -z "$$(gofmt -l .)"
	go test ./...

ci: verify
	go test -race ./...
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o build/nsl-linux-amd64 .
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o build/nsl-linux-arm64 .
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o build/nsl-agent ./cmd/nsl-agent

release-check:
	goreleaser check

# The documentation site (site/), built with pinned ProperDocs and MaterialX.
SITE_VENV := build/site-venv

$(SITE_VENV)/bin/properdocs: site/requirements.txt
	python3 -m venv $(SITE_VENV)
	$(SITE_VENV)/bin/pip install -q --require-hashes -r site/requirements.txt
	touch $@

site: $(SITE_VENV)/bin/properdocs
	$(SITE_VENV)/bin/properdocs build --strict -f site/properdocs.yml

site-serve: $(SITE_VENV)/bin/properdocs
	$(SITE_VENV)/bin/properdocs serve -f site/properdocs.yml

clean:
	go clean
	rm -f build/nsl build/nsl-agent build/nsl-linux-amd64 build/nsl-linux-arm64
