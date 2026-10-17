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

# koc-embedded: the web-console sandbox build (docs/shell.md). The koc_embedded
# tag leaves the Vault, S3 and Kubernetes code out and makes the embedded
# profile permanent. Containers only, so Linux only; kept in sync with the
# koc-embedded build in .goreleaser.yaml and the CI cross-compile matrix.
EMBEDDED_BINARY    := koc-embedded
EMBEDDED_TAGS      := koc_embedded
EMBEDDED_PLATFORMS := linux/amd64 linux/arm64
# The packages the embedded binary is made of and tested through. `./...` also
# holds internal/cli/vault and internal/cli/s3, which the tag only unregisters:
# they are not part of koc-embedded and do not build without the Vault and
# credential-source code it leaves out.
EMBEDDED_VET   := ./cmd/... ./internal/auth/ ./internal/cli/ ./internal/cli/shell/ ./internal/kochome/
EMBEDDED_TESTS := ./internal/cli/ ./internal/cli/shell/ ./internal/kochome/

.PHONY: all build static embedded embedded-check test functional race cover sonar crossbuild vet lint fmt tidy vendor completions size clean

all: build

## build: build a static binary for the host platform
build: static

## static: fully static, stripped, trimmed binary
static:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

## embedded: build the static koc-embedded binary for the host platform
embedded:
	CGO_ENABLED=0 go build -tags $(EMBEDDED_TAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(EMBEDDED_BINARY) $(PKG)

## embedded-check: vet and test the koc_embedded build, and prove its package
## graph holds no Vault, S3 or Kubernetes code. Offline.
embedded-check:
	GOPROXY=off go vet -tags $(EMBEDDED_TAGS) $(EMBEDDED_VET)
	@if GOPROXY=off go list -deps -tags $(EMBEDDED_TAGS) $(PKG) | grep -E '/internal/(kube|vault|s3|cli/s3|cli/vault)$$'; then \
		echo "koc-embedded links code the koc_embedded tag must leave out" >&2; exit 1; fi
	GOPROXY=off go test -tags $(EMBEDDED_TAGS) $(EMBEDDED_TESTS)
	GOPROXY=off go test -tags $(EMBEDDED_TAGS) -run 'KOCHome|ScopeFile|IsSecretName' ./internal/auth/

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

## cover: write coverage.out for SonarQube (see AGENTS.md "SonarQube").
## -coverpkg=./... is load-bearing: without it each test binary only instruments
## its own package, so the whole command tree that internal/cli's root tests
## build is executed but never attributed. It is worth ~11 points.
cover:
	GOPROXY=off go test -coverpkg=./... -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

## sonar: run the SonarQube scan (NETWORK + a server token; deliberately outside
## the offline per-commit gate — see AGENTS.md "SonarQube").
##
## The two prerequisites below are the whole point of this target, because both
## were forgotten by hand and both fail silently:
##   - `cover` first, so the profile matches the tree being scanned. A stale
##     coverage.out does not error; it reports the new code as untested.
##   - -Dsonar.projectVersion, so the new-code period means "since the last
##     release". Omit it and every scan analyses the same version, the baseline
##     freezes at whatever it was, and old findings keep counting as new.
## Host and token come from the environment and must never be committed.
sonar: cover
	@test -n "$(SONAR_HOST_URL)" || { echo "SONAR_HOST_URL is unset" >&2; exit 1; }
	@test -n "$(SONAR_TOKEN)"    || { echo "SONAR_TOKEN is unset" >&2; exit 1; }
	podman run --rm \
		-e SONAR_HOST_URL -e SONAR_TOKEN \
		-v "$(CURDIR):/usr/src" \
		docker.io/sonarsource/sonar-scanner-cli:latest \
		-Dsonar.projectVersion="$$(git describe --tags --abbrev=0)"

## crossbuild: compile every release target (build-only, offline) so a build-tag
## or syscall mistake is caught here instead of at release time.
crossbuild:
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "==> $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 GOPROXY=off go build ./...; \
	done
	@set -e; for p in $(EMBEDDED_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "==> $$os/$$arch ($(EMBEDDED_BINARY))"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 GOPROXY=off go build -tags $(EMBEDDED_TAGS) -o /dev/null $(PKG); \
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

## size: print the built binaries' sizes in bytes (watch for size regressions)
size: static embedded
	@echo "$(BINARY) size: $$(stat -c%s $(BINARY)) bytes"
	@echo "$(EMBEDDED_BINARY) size: $$(stat -c%s $(EMBEDDED_BINARY)) bytes"

## clean: remove build artifacts
clean:
	rm -f $(BINARY) $(EMBEDDED_BINARY)
	rm -rf dist completions
