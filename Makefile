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

.PHONY: all build static test race crossbuild vet lint fmt tidy vendor completions size clean

all: build

## build: build a static binary for the host platform
build: static

## static: fully static, stripped, trimmed binary
static:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

## test: run unit tests
test:
	go test ./...

## race: run unit tests under the race detector (needs cgo; shipped binaries
## stay CGO_ENABLED=0). Still offline — vendor/ only, no module proxy.
race:
	CGO_ENABLED=1 GOPROXY=off go test -race ./...

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
