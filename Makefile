.PHONY: build agent test fmt lint lint-version-check verify check ci clean release-check bump site site-serve

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# golangci-lint's release, read from mise.toml, the single source of tool pins
# (frostyard/core ADR-0043): `mise install` provisions it, and CI installs the
# same mise.lock-verified release. It must be built with a Go no older than
# go.mod's, or its gofmt and type checker disagree with the toolchain.
GOLANGCI_LINT_VERSION := $(strip $(shell sed -n 's/^golangci-lint = "\(.*\)"/\1/p' mise.toml))
GO_VERSION := $(strip $(shell sed -n 's/^go \([0-9.]*\)$$/\1/p' go.mod))

# The repository's Go files, tracked or new, so gofmt skips ignored local
# scratch such as build/.
GO_FILES = $(shell git ls-files --cached --others --exclude-standard '*.go')

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o build/nsl .

# The VM image embeds the agent; it runs on x86-64 VMs whatever the host.
agent:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o build/nsl-agent ./cmd/nsl-agent

test:
	go test ./...

fmt:
	gofmt -w $(GO_FILES)

lint: lint-version-check
	golangci-lint run

lint-version-check:
	@test -n "$(GOLANGCI_LINT_VERSION)" || { echo "mise.toml pins no golangci-lint"; exit 1; }
	@installed="$$(golangci-lint version --short 2>/dev/null)" || { \
		echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required (install with: mise install)"; exit 1; }; \
	if [ "$$installed" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "expected golangci-lint $(GOLANGCI_LINT_VERSION), found $$installed (install with: mise install)"; exit 1; \
	fi; \
	built="$$(golangci-lint version 2>/dev/null | sed -n 's/.*built with go\([0-9.]*\).*/\1/p')"; \
	if [ -n "$$built" ] && [ "$$(printf '%s\n%s\n' "$(GO_VERSION)" "$$built" | sort -V | head -1)" != "$(GO_VERSION)" ]; then \
		echo "golangci-lint $(GOLANGCI_LINT_VERSION) was built with go$$built, older than go.mod's go$(GO_VERSION)"; exit 1; \
	fi

# The gate triad (frostyard/core ADR-0043, ADR-0044). verify is credential-free
# and leaves a clean checkout clean: what a read-only reviewer runs. check is the
# developer gate and may format. ci is verify plus what CI alone adds.
verify:
	go mod tidy -diff
	python3 scripts/license-notices.py --check
	go vet ./...
	test -z "$$(gofmt -l $(GO_FILES))"
	$(MAKE) --no-print-directory lint
	python3 -m unittest discover -s scripts -p 'test_*.py'
	go test ./...

check: fmt verify

ci: verify
	mkdir -p build
	go test -race -coverprofile=build/coverage.out ./...
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o build/nsl-linux-amd64 .
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o build/nsl-linux-arm64 .
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o build/nsl-agent ./cmd/nsl-agent

release-check:
	goreleaser check

# Tag the next release and push it; release.yml builds it. svu derives the
# version from conventional commits (.svu.yml) and is pinned in mise.toml.
# Only a clean main that matches origin/main, after make ci, is tagged.
bump:
	@command -v svu >/dev/null 2>&1 || { echo "svu is required for make bump (install with: mise install)"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "The working tree is not clean; commit or stash before bumping."; exit 1; }
	@test "$$(git branch --show-current)" = main || { echo "Bump from main."; exit 1; }
	git fetch --quiet --tags origin main
	@test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" || { echo "HEAD is not origin/main; pull or push first."; exit 1; }
	$(MAKE) ci
	@test -z "$$(git status --porcelain)" || { echo "make ci changed the working tree; not tagging."; exit 1; }
	@version=$$(svu next); \
		git tag -a "$$version" -m "Version $$version"; \
		echo "Tagged $$version; pushing it to origin"; \
		git push origin "$$version"

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
