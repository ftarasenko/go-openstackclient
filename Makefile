BINARY   := koc
PKG      := ./cmd/koc
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

# Hard air-gap requirement: builds must reproduce offline from vendor/.
export CGO_ENABLED := 0
export GOFLAGS     := -mod=vendor

.PHONY: all build static test vet lint fmt tidy vendor clean

all: build

## build: build a static binary for the host platform
build: static

## static: fully static, stripped, trimmed binary
static:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

## test: run unit tests
test:
	go test ./...

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

## clean: remove build artifacts
clean:
	rm -f $(BINARY)
	rm -rf dist
