BINARY   := koc
PKG      := ./cmd/koc
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

# Hard air-gap requirement: builds must reproduce offline from vendor/.
export CGO_ENABLED := 0
export GOFLAGS     := -mod=vendor

# The six targets .goreleaser.yaml ships. Kept here so `make crossbuild` and the
# CI cross-compile matrix stay in sync with the release matrix.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build static test functional race cover crossbuild vet lint fmt tidy vendor completions size clean

all: build

## build: build a static binary for the host platform
build: static

## static: fully static, stripped, trimmed binary
static:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

## test: run unit tests
test:
	go test ./...

## functional: the koc functional tests in test/functional (build tag
## "functional"). Against a devstack from scripts/devstack/up.sh, pass
## KOC_FT_ENV=<log-dir>/functional.env, optionally KOC_BIN=<koc> and
## KOC_FT_AUTH=clouds|env. Without KOC_FT_ENV only the no-cloud tests run (the
## harness's own, and the Vault CLI against the fake server). Still offline.
FUNCTIONAL_TIMEOUT ?= 20m
functional:
	GOPROXY=off go test -tags functional -count=1 -timeout $(FUNCTIONAL_TIMEOUT) -v ./test/functional/...

## race: run unit tests under the race detector (needs cgo; shipped binaries
## stay CGO_ENABLED=0). Still offline — vendor/ only, no module proxy.
race:
	CGO_ENABLED=1 GOPROXY=off go test -race ./...

## cover: write coverage.out for SonarQube Cloud (see AGENTS.md "SonarQube Cloud";
## .github/workflows/sonar.yml runs this, so there is no local scan to remember).
## -coverpkg=./... is load-bearing: without it each test binary only instruments
## its own package, so the whole command tree that internal/cli's root tests
## build is executed but never attributed. It is worth ~11 points.
##
## -coverpkg also makes EVERY test binary report EVERY block, so the raw profile
## repeats each block once per package (~36x, ~800k lines). The awk pass merges
## the repeats, keeping the highest count; `go tool cover` merges the same way,
## so the total is unchanged and the upload is the size of the tree.
cover:
	GOPROXY=off go test -coverpkg=./... -coverprofile=coverage.raw.out ./...
	@awk 'NR == 1 { print; next } \
		{ k = $$1 " " $$2; if (!(k in c)) { o[++n] = k; c[k] = $$3 } else if ($$3 + 0 > c[k] + 0) c[k] = $$3 } \
		END { for (i = 1; i <= n; i++) print o[i], c[o[i]] }' coverage.raw.out > coverage.out
	@rm -f coverage.raw.out
	@go tool cover -func=coverage.out | tail -1

## crossbuild: compile every release target (build-only, offline) so a build-tag
## or syscall mistake is caught here instead of at release time.
crossbuild:
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "==> $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 GOPROXY=off go build ./...; \
	done

## vet: run go vet
vet:
	go vet ./...

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## fmt: format sources
# Needs gofmt from Go 1.27+. Go 1.27 changed the indentation of a multi-value
# return whose operands are composite literals, and the pinned golangci-lint
# bundles that gofmt, so an older one silently reverts the tree and the lint job
# then fails on files `make fmt` just "fixed".
fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

## tidy: tidy and re-vendor modules
tidy:
	GOFLAGS= go mod tidy
	GOFLAGS= go mod vendor

## vendor: re-vendor modules
vendor:
	GOFLAGS= go mod vendor

## completions: generate the shell completions bundled into release archives and
## the .rpm/.deb. Deliberately NOT a goreleaser `before` hook: that step also
## holds the cross-repo Homebrew PAT, and repository code (including vendor/)
## must not execute with that token in scope. Run this before goreleaser.
completions:
	mkdir -p completions
	go run $(PKG) completion bash > completions/koc.bash
	go run $(PKG) completion zsh  > completions/koc.zsh
	go run $(PKG) completion fish > completions/koc.fish

## size: print the built binary size in bytes (watch for size regressions)
size: static
	@echo "$(BINARY) size: $$(stat -c%s $(BINARY)) bytes"

## clean: remove build artifacts
clean:
	rm -f $(BINARY)
	rm -rf dist completions
