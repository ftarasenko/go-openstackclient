# AGENTS.md

Guidance for AI coding agents (and humans) working in this repository. Keep it
current when the structure or conventions change.

## What this is

`koc` — a single, statically-linked Go binary that replaces
`python-openstackclient` for the KeyStack cloud. No Python at runtime. It mirrors
the upstream `openstack` client's `noun → verb → flags` syntax and ships as one
dependency-free binary for air-gapped / FSTEC-regulated deployment.

- **Module**: `github.com/ftarasenko/go-openstackclient` (binary name: `koc`)
- **Go**: `go.mod` floors the build at `go 1.26.0` (what `golang.org/x/crypto`
  forces); **develop on 1.27+** — CI builds and formats there, and gofmt differs
  (see "Build / test / lint")
- **SDK**: gophercloud **v2** (`github.com/gophercloud/gophercloud/v2`) — never v1 or the dead rackspace fork
- **CLI**: cobra + pflag; `golang.org/x/term` for terminal-width detection (rich gauges)
- **Deps are vendored** (`vendor/` is committed) — builds must reproduce offline

## Build / test / lint (run before every commit)

Everything runs offline from `vendor/`. Prefer the Makefile:

```sh
make build       # CGO_ENABLED=0 static, -trimpath, -ldflags "-s -w -X main.version=..."
make test        # go test ./...
make race        # CGO_ENABLED=1 go test -race ./...   (still offline; see below)
make crossbuild  # build all six release targets (GOOS/GOARCH matrix, build-only)
make vet         # go vet ./...
make lint        # golangci-lint run ./...   (golangci-lint v2; config in .golangci.yml)
make fmt         # gofmt -w
make completions # generate completions/koc.{bash,zsh,fish} for the release archives
make size        # build, then print the binary's size in bytes
make tidy        # GOFLAGS= go mod tidy && go mod vendor   (only when deps change)
```

`make race` is the one target that sets `CGO_ENABLED=1` — the race detector
requires cgo. It is deliberately a *separate* target (and a separate CI job) so
cgo never leaks into the static binaries the release ships; those stay
`CGO_ENABLED=0`. It is still offline (`vendor/` only, `GOPROXY=off`).

`make crossbuild` and `make size` exist because the product is one binary shipped
to six platforms: a build-tag or syscall mistake in a `darwin`/`windows` path used
to surface first at release time, and a size regression used to surface never.
`PLATFORMS` in the Makefile must stay in sync with `.goreleaser.yaml`'s
`goos`/`goarch` and the CI cross-compile matrix.

**The embedded profile** (`KOC_PROFILE=embedded`, docs/shell.md). Its refusals live
in one table, `internal/cli/embedded.go`, enforced from the root's
`PersistentPreRunE` — never inside a service package. A new flag or argument
that reads or writes a local file must be added there (or to the test's
`notFileFlags` with the reason it is not one): `TestEmbeddedFileFlags` walks the
tree and fails otherwise.

The air-gap invariant — every build/test must pass with **no module proxy**:

```sh
GOFLAGS=-mod=vendor GOPROXY=off CGO_ENABLED=0 go build ./...
GOFLAGS=-mod=vendor GOPROXY=off go test ./...
```

If you add an import from a new gophercloud subpackage, run `make tidy` (needs
network once) so `vendor/` and `vendor/modules.txt` stay complete; otherwise the
offline build breaks. Do not hand-edit `vendor/`.

`.golangci.yml` enables a substantially larger linter set than golangci-lint's
`standard` default — notably `bodyclose`, `contextcheck`, `errorlint`,
`forcetypeassert`, `gocognit`, `gosec`, `nilerr`, `noctx`, `nolintlint`, `revive`
(with `exported`, `package-comments`, `unused-parameter`, `deep-exit` named
explicitly) and `usestdlibvars` — plus `max-issues-per-linter: 0` /
`max-same-issues: 0` so a run reports every occurrence rather than the first
three. New code is expected to pass all of it; a `//nolint` must name the linter
and carry a rationale (`nolintlint` enforces both). The narrow per-file
exclusions in that file are scoped on purpose — do not widen one to a linter or
a package.

`gocognit` runs at `min-complexity: 25`, above golangci-lint's own default of
30 in strictness but well above SonarSource's 15. A command's `RunE`/opts
builder is a chain of `if flags.Changed("x") { opts.X = &x }` — one branch per
CLI flag, linear and one level deep — so the metric there tracks how many flags
a verb has, not how hard it is to read; the tallest such function is 22. Above
25 the functions were genuinely doing several jobs at once and have been split.
The threshold lives here rather than only in SonarQube because the offline gate
is the one every commit passes through (see "Releases & CI").

`nilnil` is deliberately **not** enabled. All of its hits in this tree are the
idiomatic "optional value is absent" pattern rather than ambiguous not-found
signalling: the `parseKeyValMap`/`parseProperties`-style helpers return a nil map
for empty input, and helpers like `buildGatewayInfo`/`imageVisibility` return a
nil pointer meaning "leave this field unset". A sentinel error would make every
call site worse. The reasoning is recorded in `.golangci.yml` next to the disabled
entry; re-check it there before enabling.

Gate before a change is done (which parts an agent runs itself and which it
leaves to GitHub is in "Where checks run (agent sessions)"): `gofmt` clean,
`go vet` clean, `golangci-lint` **0 issues**, `go test ./...` green, the offline static build succeeds, and — if the
commit changes the command surface — `docs/coverage.md` is updated (see "Coverage
tracking") and every new leaf has a functional test (`go test -tags functional
./test/functional/` runs `TestEveryLeafIsCovered`, offline; see "Testing").

**`gofmt` here means Go 1.27's.** 1.27 changed how a multi-value `return` whose
operands are composite literals is indented, and the two versions disagree in
both directions — 1.26's gofmt reformats what 1.27's wrote and vice versa. The
pinned golangci-lint bundles 1.27's (v2.13 and v2.14 are built with
go1.27.0) and enforces it as the `gofmt` formatter, so running `make fmt` under
an older toolchain silently reverts the tree and the lint job then rejects the
files it just touched. `go.mod`'s floor stays at 1.26.0 because nothing in the
code needs 1.27 — building and testing on 1.26 is fine, formatting is not. If
the golangci-lint pin ever moves to a build made with a newer Go, re-run
`make fmt` with that Go in the same commit. The lint job's `check-latest` Go
can also outrun the pin: v2.13.2 could not read go1.27.2's export data
("export data version 5"), and v2.14.0 can — move the pin when that happens.

## Where checks run (agent sessions)

An agent session — a cloud container or a laptop — runs the **cheap, fast**
checks for the code it touched; GitHub runs the **whole matrix** on every push
to `claude/**`. Measured in a 4-core cloud container on 2026-10-21:

| Check | Where | Cost |
| --- | --- | --- |
| `go vet ./...` | session | ~5 s |
| `go test ./<pkg>/...` for the packages you changed | session | ~1 s each |
| `go test ./...` (full unit suite) | session, before pushing a multi-package change | ~45 s, ~40 lines of output |
| `go test -tags functional ./test/functional/` (no cloud; includes `TestEveryLeafIsCovered`) | session, when the command surface changed | seconds |
| `gofmt` (Go **1.27**, see below) | session, on the files you changed | seconds |
| `make lint` (golangci-lint **v2.14.0**) | GitHub `ci.yml` → `lint` | — |
| `make race`, `make crossbuild` | GitHub `ci.yml` | — |
| `make cover` + SonarQube Cloud | GitHub `sonar.yml` | ~1–5 min; never run it by hand |
| vendor integrity, govulncheck, importer, api-compat | GitHub `supply-chain.yml` | — |
| devstack functional run | GitHub `functional.yml` (nightly) | 75 min |

Rules that keep this cheap and correct:

- **Filter test output.** `go test ./... 2>&1 | grep -v '^ok'` prints only
  failures; a verbose `-v` run of the whole tree is thousands of lines that
  nobody reads. Use `-run` to rerun one failing test.
- **Format with Go 1.27's gofmt even when the session has an older Go.** The
  cloud image ships an older toolchain, and its gofmt reverts 1.27's formatting
  (see "Build / test / lint"). The go command fetches the right one through the
  module proxy and caches it:
  `"$(GOTOOLCHAIN=go1.27.2 go env GOROOT)/bin/gofmt" -l <files>` (`-w` to fix).
  Never run `make fmt` on an older toolchain.
- **Do not trust a local golangci-lint that is not v2.14.0.** The cloud image
  carries an older one, which enables different checks; a clean run there proves
  nothing. Push and read the `lint` job instead.
- **After pushing, read the result.** Check the runs for the pushed commit on
  GitHub (`ci`, `supply-chain`, `sonar`) and fix a red job before moving on —
  the push is not done until they are green.

## SonarQube Cloud (out-of-band static analysis)

SonarQube Cloud (sonarcloud.io) analyses the project from
**`.github/workflows/sonar.yml`**, on every push to `master` and `claude/**`, on
every pull request from this repository, and on `workflow_dispatch`. Nobody runs a scan by
hand any more. It is **not** part of the per-commit offline gate: it needs
network and a token, so it lives in its own network-allowed workflow, outside
the air-gap invariant on purpose (see "Releases & CI" for why that boundary is
load-bearing). The tests that produce its coverage profile still run offline.
The code is public, so analysing it on an external service exposes nothing new;
the token is the only secret.

One-time setup, by a maintainer:

1. Import the repository on sonarcloud.io. If it assigns a key or organization
   other than `ftarasenko_go-openstackclient` / `ftarasenko`, change
   `sonar-project.properties` to match.
2. Turn **Automatic Analysis off** for the project (Administration → Analysis
   Method). It conflicts with a CI-based scan, and it cannot import coverage.
3. Set the **new-code definition to "Previous version"**.
4. Store the project's analysis token as the repository secret **`SONAR_TOKEN`**.

Until the secret exists, and on pull requests from forks or Dependabot (GitHub
does not give those runs repository secrets), the `scan` job is skipped with a
notice rather than failed.

The workflow exists because the manual scan failed silently, twice over, on every
scan between 2026-09-01 and 2026-09-22. It now does both steps every time:

- It **generates `coverage.out` on the checkout it scans** (`make cover`). A
  stale profile does not error. The scanner uploads it, and every line written
  since the profile was generated reports as uncovered. The 2026-09-22 scan read
  25% new-code coverage against a profile from 2026-08-31; the tree's actual
  figure for the same period was 83%.
- It **passes `sonar.projectVersion`** from `git describe --tags --abbrev=0
  --match 'v*'`, and fails the job if no tag is reachable. A missing version
  makes every scan analyse the same version, so `PREVIOUS_VERSION` never
  advances. The baseline froze at v0.26.0 across seven releases, "new code" grew
  to 24.5k lines, and long-standing findings kept counting as new. The checkout
  is full-depth (`fetch-depth: 0`) for the same reason. A shallow clone has no
  tags and no blame, and the scan degrades without saying so.

**Coverage is generated by `make cover`, not a bare `go test -coverprofile`.**
That target passes `-coverpkg=./...`, and the flag is load-bearing here: by default a test binary
instruments only its own package, but `internal/cli`'s root tests build the
*entire* command tree via `NewRootCommand`, so every service's flag registration
already executes and would otherwise not be attributed to it. Measuring without
`-coverpkg` under-reported the project by roughly 11 points (54% vs 65% when
measured). The figure with it was 76.3% on 2026-10-21. The tests are the same
either way. Two consequences of the flag that the target handles:

- Every test binary reports every block, so the raw profile repeats each block
  once per package (about 36× and 800k lines). The target merges the repeats,
  keeping the highest count, as `go tool cover` does. The total is unchanged, and
  the upload is about 23k lines.
- A package with no test file is reported through `go tool covdata`. Toolchains
  fetched by `GOTOOLCHAIN` do not ship that tool, so a single test-less package
  failed the whole run with `no such tool "covdata"`. `cmd/koc` has a test for
  that reason. Keep every package that holds Go code tested.

`/coverage.*` is gitignored (root-anchored, so `docs/coverage.md` survives), and
`coverage.*` is in `sonar.exclusions` so the profile is not analysed as source.

**Why `sonar.projectVersion` matters.** The project's new-code period is
`PREVIOUS_VERSION`, so the version string is what makes "new code" mean *since
the last release* instead of *since the previous scan*. Otherwise a mechanical
refactor re-attributes long-standing untested lines to new code, and the gate
fails on work nobody just wrote. The same note sits next to the setting in
`sonar-project.properties`.

Two known, accepted divergences — neither is a regression to chase:

- **Cognitive complexity.** Sonar's `go:S3776` fires at 15; this repo's
  `gocognit` gate is 25 for the reasons in "Build / test / lint" above. The two
  cannot be reconciled server-side from here — the threshold is a rule
  *parameter*, it lives in the Quality Profile, and the Go profile in use is the
  built-in "Sonar way", which is read-only — so the rule is **suppressed
  project-wide** by the `e2` entry in `sonar-project.properties`, which carries
  the full reasoning. `golangci-lint` is the gate that every commit passes
  through, so it wins; do not split a function only to satisfy S3776, and do not
  re-enable the rule without raising its threshold to 25 on a custom profile
  first.
- **First scan of a fresh project.** With no prior analysis there is no previous
  version to diff against, so the whole tree lands in "new code" and the gate can
  fail on pre-existing findings. Re-check against the second scan before treating
  anything as new.

Coverage numbers will not match `go tool cover`: Sonar reports *line* coverage
and honours `sonar.coverage.exclusions` (which drops `cmd/koc/main.go`), while
`go tool cover -func` reports *statement* coverage over everything.

## Layout

```
cmd/koc/main.go            cobra root entrypoint; version var; signal-cancelled context
internal/kochome/          KOC_HOME: the one resolver for config/state paths; KOC_PROFILE
internal/auth/             one authenticated ProviderClient per invocation + per-service clients
  options.go               global flags (auth/TLS/microversion/output/debug/timing/timeout + creds-from-*), env-defaulted
  transport.go             http.Client construction: --timeout whole-exchange cap (0 = off) + fixed 60s response-header timeout
  provider.go              Authenticate(): clouds.yaml OR OS_* OR --creds-from-*; domain/scope resolution
  tls.go                   explicit *tls.Config (CA bundle, mTLS, --insecure, TLS 1.2 min)
  services.go              auth.Client factory methods: Compute()/Identity()/Volume()/...
  credsfrom.go             --creds-from-ns/-vault: standalone basic-auth ironic + Vault openrc → OS_*
  scopefile.go             KOC_SCOPE_FILE: a host-written token + scope, mapped onto the OS_* options
  debug.go                 --debug transport: redacts tokens+secrets, elides large bodies
internal/kube/             minimal read-only k8s REST client (kubeconfig + secret/Ironic reads); no client-go
internal/vault/            minimal Vault REST client (AppRole login / token + KV v2 read + X-Vault-Namespace)
  vaulttest/               fake Vault server for tests, pinned to a real server's answers (TestContract)
internal/s3/               minimal S3 REST client, no aws-sdk-go-v2 / minio-go: hand-rolled SigV4
                           (header + presigned query), list/head/get/put, multipart upload,
                           server-side copy, batch delete, versioning, retry with backoff
internal/output/           -f/--format {table,json,yaml,value,csv} and -c/--column layer
internal/watch/            --watch: the in-process refresh loop (ticker, alternate-screen
                           painter, status line, identity-based diff, raw-mode keys)
internal/cli/keyvrm/       KeyVRM (in-house catalog service): cobra verbs over the typed layer in pkg/keyvrm
internal/cli/vault/        "koc vault kv" list/get/copy/export/decrypt (package vaultcli); Vault creds only
internal/cli/s3/           "koc s3" bucket/object lifecycle, du, download/upload (multipart, stdin,
                           recursive), server-side copy/move, presign, sync (package s3cli); S3 creds only
internal/cli/quota/        "koc quota show|set" — the one cross-service noun (nova+cinder+neutron)
internal/cli/              root.go wires every service's command group onto the root;
                           watch.go gives every read verb --watch in the same final pass;
                           run.go is one invocation on a fresh tree (main and every shell line);
                           embedded.go is the embedded profile's one refusal table
internal/cli/shell/        "koc shell": x/term REPL, cobra completion, scope-file reload, audit events
internal/cli/resolve/      the CLI's name→ID policy (zero-match fallback, --watch memo) over pkg/openstack/resolve
internal/cli/<service>/    one package per service; one file per noun; a client.go helper
pkg/                       the PUBLIC Go API (see "Public Go API"); the CLI is its first consumer
  openstack/               Session: New/FromProvider/ForRegion + per-service client constructors
  openstack/paging/        Collect[T]: page until a result cap is met
  openstack/resolve/       strict name→ID resolvers, Resolver + endpoint-keyed Cache
  openstack/<service>/     one package per service: the API half of its runXxx seams (image so far)
  keyvrm/                  KeyVRM typed requests + DTOs
scripts/devstack/          up.sh (single-node devstack per series, local VM or --gha runner),
                           smoke.sh (per-feature proof + koc read verbs), collect-logs.sh
test/functional/           Go functional tests (build tag "functional"): the koc binary against
                           a devstack from up.sh, plus the Vault CLI against vaulttest
test/importer/             nested module (own go.mod): pkg/ used from outside, run in supply-chain.yml
```

Services: `baremetal` (ironic), `server`+`compute` (nova), `identity` (keystone),
`volume` (cinder), `dns` (designate), `image` (glance), `network` (neutron),
`placement`, `keyvrm` (Keystack Virtual Resource Manager — in-house).

`keyvrm` is the first **non-standard catalog service**: its endpoint is resolved
by catalog type `keyvrm` (there is no gophercloud package), it authenticates with
the plain Keystone token, and requests use the raw `ServiceClient.Get/Put/Post`
pattern decoding into koc-owned DTO structs.

> **Feature parity:** `koc keyvrm …` mirrors the Python `kvrm` CLI
> (`~/code/project_k/keyvrm-cli`) over the `keyvrm-sdk` API
> (`~/code/project_k/keyvrm/package/keyvrm_sdk`). When either changes (new
> endpoints, DTO fields, or verbs), re-check and update `pkg/keyvrm/`
> (`requests.go`/`types.go`, the public API) and `internal/cli/keyvrm/` (the verb
> files, the CLI) to keep them in sync. See `docs/proposals/keyvrm.md` for the
> command mapping.

## Command pattern (follow it exactly for new commands)

Every command file mirrors `internal/cli/baremetal/node.go`:

1. `newXxxCommand(a *auth.Options, o *output.Options) *cobra.Command` builds the
   cobra command and registers flags. Two-word OSC nouns (`floating ip`,
   `security group rule`, `application credential`) are modeled as nested parent
   commands so cobra resolves them unambiguously.
2. `RunE` starts with `o.Validate()`, builds the service client via the
   package's `client.go` helper, then delegates to a **`runXxx` seam**:
   ```go
   func runXxx(ctx context.Context, client *gophercloud.ServiceClient,
       o *output.Options, /* args */, w io.Writer) error
   ```
   The seam takes an already-built `*gophercloud.ServiceClient` and an
   `io.Writer` so tests drive it directly against a mock endpoint — no auth.
   In a service that has moved behind `pkg/` (image, keyvrm so far), the seam's
   API half — build opts, call, page, extract, wrap the error — is a public
   function there, and the seam is that call plus the render step. Add the
   public function first, then the seam over it (see "Public Go API").
3. Results route through the output layer: `o.WriteList(w, output.Table{...})`
   for lists, `o.WriteSingle(w, fields, values)` for a single resource. Never
   `fmt.Println` structured output.
4. `context` comes from `cmd.Context()`. Pagination is `List(...).AllPages(ctx)`
   then `Extract*`. `--limit` is a hard result cap where the API treats it only
   as a page size (truncate after Extract).
5. **Update `docs/coverage.md` in the same commit** — see "Coverage tracking".
6. **Add a functional test that runs the new leaf** against the devstack
   (`test/functional`, see "Testing"). Every leaf command has one;
   `TestEveryLeafIsCovered` fails the offline CI run for a leaf that does not.

A new `list` or `show` leaf gets `--watch` for free: `internal/cli/watch.go`
wraps every read verb in the final tree pass, so the command is re-run into a
frame buffer on a ticker with nothing in its own file to say so. Two things
follow. A read verb has to be genuinely read-only and non-interactive — a `show`
that POSTs (nova's remote-consoles API) or that prompts belongs in that file's
`watchDenied`, with the reason. And it has to stay safe to call repeatedly:
`RunE` may not consume stdin, write a file, or depend on state a previous call
left behind.

Client helpers (`client.go`) authenticate once via `a.Authenticate(ctx)` then
call the right `auth.Client` factory. When a command needs a **second** service
(cross-service name resolution), return the `*auth.Client` too and derive the
secondary client lazily (see `server/client.go` `newComputeSession`).

## Public Go API (`pkg/`)

`pkg/` is a public, versioned Go API: the OpenStack service layer the CLI is
built on, for downstream importers (multi-region resource views, services that
need koc's resolve-by-name and paging). The CLI is its first consumer, so the
same requests leave either way. The plan, the per-service checklist for the
services not yet moved, and the owner's decisions on its scope are in
`docs/proposals/public-api.md`.

Rules, enforced where a tool can:

- **No CLI in `pkg/`.** depguard (`.golangci.yml`, rule `public-api`) denies
  `pkg/**` cobra, pflag, `golang.org/x/term` and every `internal/...` import —
  a helper both need moves into `pkg/`. forbidigo, scoped to `pkg/`, denies
  `os.Getenv`/`LookupEnv`/`Environ`, the standard streams, `fmt.Print*`, `log`
  and `os.Exit`. Paths, prompts, environment variables and file locations are
  the caller's: pass them in (a later embedded/`koc shell` mode depends on it).
- **No mutable package state.** `pkg/openstack`'s `TestNoPackageState` walks
  every `pkg/` source and allows a package-level `var` only for an
  `errors.New` sentinel, a `regexp.MustCompile` or a blank interface check. A
  cache, clock or hook lives on a value the caller owns — `resolve.Cache` is
  the model; the CLI's process-wide memo is one such value held in
  `internal/cli/resolve`.
- **Plain functions on a `*gophercloud.ServiceClient`**, gophercloud-style,
  returning gophercloud v2 types where they exist; koc's own extension structs
  (raw-request fallbacks, request-body splices like `image.ImportOpts`) are
  exported with doc comments. Wait helpers take a `context.Context` (the
  deadline) and an explicit interval — `image.WaitForStatus` is the model.
  Cross-service calls take a `*openstack.Session` or the extra clients
  explicitly.
- **Errors.** A public function wraps with the text the CLI printed, naming
  the ID it was given (`"getting image %s: %w"`), and never prints. Where the
  CLI has always named the operator's *reference* instead, the CLI re-labels
  the same inner error (`internal/cli/image` `relabel`), so its output stays
  byte-identical; a CLI-flag-specific message (`--import: …`) is a sentinel in
  `pkg/` (`image.ErrGlanceDirectUnavailable`) that the CLI prefixes. Errors
  importers branch on are exported types or sentinels: `openstack.ServiceError`,
  `openstack.ErrNotFound` (matched by `resolve.NotFoundError`).
- **Strict resolution.** `pkg/openstack/resolve` errors on zero matches; the
  literal-ref fallback the older CLI packages still use is CLI policy, applied
  in `internal/cli/resolve`.
- **Tests.** Every public package has black-box tests (`package xxx_test`,
  exported identifiers only; httptest + `th`/`fakeclient`, asserting method,
  URL, microversion header and body) and runnable `Example…` functions with
  `// Output:` for the common path. Moving a service never edits its seam
  tests beyond an import path; tests of code that moved move with it.
- **Same bytes.** A `refactor(<service>)` move changes no command, flag,
  request or message: diff `koc <every leaf> --help` before and after, and run
  the affected verbs against a fake endpoint with the old and new binary.

**What counts as the API, and how it versions.** Exported identifiers under
`pkg/...` and the gophercloud v2 types they accept and return. One module, one
tag stream: while `0.y.z`, a breaking `pkg/` change bumps MINOR **and** follows
the deprecation policy; from `v1.0.0`, a breaking change is a major version and
a `/vN` path. A gophercloud major bump is therefore a koc major bump. The CLI's
flags and output are a separate contract and not covered by it.

**Deprecation policy.** An exported identifier is removed only after it has
carried `// Deprecated: use X instead.` for at least **two MINOR releases and
90 days**, whichever is longer. The commit that deprecates it carries a
`Deprecated: <identifier>` footer, which `scripts/release-notes.sh` lists under
`### Deprecations`. The same text is in `pkg/openstack/doc.go`, which godoc
shows.

**Checks.** `supply-chain.yml`'s `importer` job builds `test/importer` (a
nested module using `pkg/` only by its public paths) and fails if its graph
picks up a CLI dependency; `api-compat` (`scripts/api-compat.sh`, also runnable
locally with apidiff on `PATH`) fails an incompatible `pkg/` change the commit
range does not mark breaking. Both are network-allowed; the offline gate
covers the rest. Do not add a second `go.mod` for `pkg/`: one module keeps
`vendor/`, the offline gate and GoReleaser as they are.

## Minimum supported cloud: Zed (2022.2)

`koc` runs against a fleet spanning **OpenStack Zed (2022.2) to current**. A
command that only works on the newest release is not done. Each service's Zed
release caps its microversion at:

| Service | Zed version | Max microversion |
| --- | --- | --- |
| ironic | 21.1 | **1.80** |
| nova | 26.x | **2.93** |
| cinder | 21.3.2 | **3.70** |
| placement | 9.0.0 | **1.39** |
| keystone, glance, neutron, designate, octavia | — | no microversions; capability is per-extension |

The `--os-*-api-version` defaults are `latest`, which negotiates downward, so
the common path already works. The hazard is a **version-gated endpoint**: below
its microversion it does not exist, and ironic answers 404 — indistinguishable
from "no such node" unless you say otherwise.

So when adding a command: **look up the microversion the API gated the feature
behind** (`ironic/api/controllers/v1/versions.py` in the sdist is the canonical
list; PyPI is reachable where opendev.org is not). If it is above the cap in the
table, route the error through `internal/cli/baremetal.explainMicroversion` (or
the equivalent for that service) and state the requirement in the command's
`Short`. Known post-Zed ironic features: `node inventory` (1.81), shards
(1.82), `node children list` (1.83), `node unhold` (1.85), `node firmware list`
(1.86), `node service` (1.87), runbooks (1.92), inspection rules (1.96).

The same rule keeps `koc baremetal introspection …` alive: ironic dropped the
`inspector` inspect interface in 33.0.0 (2026.1) and moved inspection rules into
its own API at 1.96, but on Zed the inspector is the only in-band inspection
there is. Both dialects ship.

## Conventions

- **Errors**: wrap with `fmt.Errorf("...: %w", err)`; non-zero exit on failure
  (handled in `main`); human-readable messages to stderr.
- **Flags**: names mirror upstream OSC. KeyStack docs (docs.keystack.ru) were
  unreachable (HTTP 403) at implementation time, so flag surfaces are marked
  **UNVERIFIED against KeyStack** in a doc comment near the flag definitions,
  with upstream-OSC fallback. Keep that note when adding flags; if KeyStack later
  proves to differ, KeyStack wins and cite the doc URL in a comment.
- **name→ID**: resolvers pass UUIDs through untouched, list-by-name for exactly
  one match, and error on multiple. A zero-match **errors** (`no <kind> found
  for "<ref>"`, upstream's behaviour) in `server`, `volume` and `network`; the
  remaining packages still fall back to the literal ref (README "Known
  limitations") and should move to the strict form when touched — new resolvers
  are always strict. In network tests, `echoLookup` answers a name lookup with
  one match whose ID is the ref.
- **Output** is the single source of truth for formatting — extend
  `internal/output`, don't format inline.
- **Credential sources** (koc-specific, no OSC equivalent; mutually exclusive):
  `--creds-from-ns <ns>` reads a metal3 ironic-standalone-operator instance's
  basic-auth secret from a k8s namespace and builds a standalone ironic client
  (baremetal only, no Keystone); `--creds-from-vault <path>` reads an openrc-style
  KV v2 secret from Vault (AppRole or token) and folds its `OS_*` into the normal
  Keystone flow (all services). The Vault path is absolute (leading `/`) or
  relative to `--vault-kv-prefix`. Both use `internal/kube` / `internal/vault`
  (minimal REST, no client-go / Vault SDK — preserves the air-gap invariant).
  When `--vault-*` connection flags are absent, the Vault address/namespace/
  role_id/KV mount/prefix are auto-discovered from the LCM `k0s-system/lcm-config`
  ConfigMap and the AppRole secret-id from `cert-manager/vault-approle`, so on a
  cluster node `--creds-from-vault <path>` needs no Vault flags (explicit flags /
  `VAULT_*` env always win; Vault TLS uses system roots).

## Testing

Use `net/http/httptest` + gophercloud's fixtures
(`github.com/gophercloud/gophercloud/v2/testhelper` as `th`, and
`.../testhelper/client` as `fakeclient`). A test builds a fake
`*gophercloud.ServiceClient` (set `sc.Type` and `sc.Microversion` to match the
real factory), points it at the mock, and calls the `runXxx` seam directly.
Assert **request method, URL, microversion header(s), request body, and rendered
output**. Cover at least the primary list plus one write verb per noun.

For Vault, use `internal/vault/vaulttest` rather than a hand-rolled handler: it
answers the way a real server does (a LIST of an empty path is a 404, a policy
denial has its own 403 body), and its `TestContract` re-runs against a real dev
server with `VAULTTEST_ADDR` set. Extend that table, from a fresh probe, before
koc relies on another part of the Vault API
(`docs/verification/2026-09-27-vault-api-probe.md`).

**Functional tests** (`test/functional`, build tag `functional`, so `go test
./...` never builds them) run the real `koc` binary as an operator does: a
separate process, credentials from `clouds.yaml` or an openrc, output parsed
from `-f json`. `make functional KOC_FT_ENV=<log-dir>/functional.env` runs them
against a devstack from `scripts/devstack/up.sh`; without `KOC_FT_ENV` the cloud
tests skip and the rest (the harness's own tests, the Vault CLI against the
fake) still run, which is what offline CI does.

**Every leaf command is covered by a functional test.** Unit tests through the
`runXxx` seam prove koc sends what it means to send; only a run against a real
cloud proves the cloud accepts it — the bare `OS_VOLUME_API_VERSION=3`, ID-only
domains and `--os-project-name` over a named cloud were all bugs every unit
test passed. `TestEveryLeafIsCovered` enforces the rule offline: it reads this
package's sources, resolves every koc invocation (a runner's
`run`/`ok`/`json`/`fails` with literal command words) against the real command
tree, and fails for a leaf that no test invokes. A test gated to one cell still
covers its leaf, since the check reads the source rather than a run.
`test/functional/uncovered.txt` lists the leaves that predate the rule; it only
shrinks — delete a line when its test lands (the check fails until you do), and
never add one: a new command lands with its functional test. A test covers a
leaf well when it drives the verb through its effect (create → show → set →
show → delete), not merely when the verb exits 0.

Rules for adding one:

- Run koc through a `runner` (`defaultRunner`, `cloudsRunner`, `openrcRunner`):
  it starts from a scrubbed environment, so a test never inherits a credential.
  `KOC_FT_AUTH` (`clouds` or `env`) picks the suites' default path; both are
  default operator paths, and the nightly alternates them across cells.
- Gate on capability, not release: `requireExtension`/`requireFeature` first,
  `requireSeries` only for behaviour nothing on the cloud advertises,
  `requireBackend` only for data-plane assertions. A new capability also goes
  into `TestCapabilityMatrix`, so its disappearance fails the run instead of
  turning its tests into skips.
- tap mirrors are API-only everywhere (under OVN the API loads but nothing is
  mirrored) and absent before 2025.1.

## Coverage tracking

`docs/coverage.md` records how much of the upstream `openstack` surface `koc`
implements, measured against the OSC / ironic / designate / octavia /
osc-placement / ironic-inspector entry points and the gophercloud v2 package
graph. It is the project's progress dashboard, so it is only useful while it is
accurate.

**Every commit that changes the command surface updates it in the same commit.**
That means any commit which adds, renames, or removes a `koc` command, or moves a
gap from one tier to another. Concretely:

- Bump the affected per-service row (raw **and** core counts) and the headline
  total, plus the snapshot line at the top (date + `koc` commit + leaf-command
  count).
- Delete the command from the prioritised-gap tier it was listed under. If it was
  Tier 1/2 and you vendored a new gophercloud package for it, also update the
  "vs gophercloud v2" packages-used row.
- Add a row to "Naming deviations" if the new command intentionally differs from
  the upstream name, or to "koc-native commands" if it has no upstream
  equivalent (KeyStack extensions, KeyVRM, Vault).
- Re-derive rather than guess when a batch of commands lands or a baseline
  version moves — the "Updating this document" section carries the exact
  commands. Counts are cheap to recompute and expensive to be wrong about.
- Then **check the three arithmetic identities** the "Updating this document"
  section states (rows sum to the headline; leaves = upstream-equivalent +
  koc-native; denominators sum to the in-scope total). A row edited by hand
  instead of re-derived is exactly what breaks them.

A docs-only refresh of this file is a `docs:` commit; when it rides along with
the feature it belongs to, it is part of that `feat:` commit and needs no
separate entry.

## gophercloud v2 gotchas (real bug sources)

- **Constructors set `client.Type`**; the microversion header is emitted from
  it — ironic → `X-OpenStack-Ironic-API-Version`; nova/cinder/placement → the
  generic `OpenStack-API-Version`. Set `sc.Microversion` for compute/volume/
  baremetal/placement; leave it empty for identity/network/dns/image.
- **`setMicroversionHeader` overwrites `RequestOpts.MoreHeaders`** on every
  request. To pin a single raw call to a specific microversion, shallow-copy the
  service client and set `.Microversion` on the copy (see
  `server/actions.go serverActionRaw`, pinned to nova 2.43 for floating-IP
  actions removed at 2.44).
- **Env auth is built in-repo, not via `openstack.AuthOptionsFromEnv()`** —
  that helper only reads `OS_DOMAIN_NAME` and rejects the standard split
  `OS_USER_DOMAIN_NAME` / `OS_PROJECT_DOMAIN_NAME` openrc. `auth/provider.go`
  builds `AuthOptions` from `OS_*`/flags and sets `ao.Scope` explicitly so a user
  in one domain can scope to a project in another.
- **Some subresources lack typed packages** (cinder service enable/disable, nova
  floating-IP actions, glance activate/deactivate). Fall back to raw
  `ServiceClient.Get/Post/Put/Delete` with the correct microversion, **isolated
  behind a small helper**, and note it in a comment so it's easy to replace.
- **`servers.CreateOpts.UserData` guesses at the encoding.** It base64-encodes
  the bytes only when they do not already decode as base64, and Go's decoder
  ignores newlines — so an ordinary file whose remaining bytes are all in the
  base64 alphabet with a length divisible by four (`runcmd\nls\n`,
  `hostname\n`) is sent verbatim, nova's lenient `format: base64` check accepts
  it, and the guest is served the decoded garbage. **Encode before handing the
  value over** (`readUserData` in `server/server.go`), which pins the
  pass-through branch and matches upstream OSC. `RebuildOpts` has no `UserData`
  field at all — it still models the personality files nova removed at 2.57 — so
  rebuild splices the field in via `serverRebuildOptsExt`.
- **Provision-state / async transitions** (ironic): after deploy/manage/inspect,
  `--wait` polls `provision_state` keyed off `target_provision_state` clearing —
  see `baremetal/node_provision.go`.

## Commit messages (Conventional Commits 1.0.0)

Every commit follows [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

```
<type>[optional (scope)]: <description>

[optional body]

[optional footer(s)]
```

- **Types** used in this repo: `feat` (new capability), `fix` (bug fix), `docs`,
  `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`. Anything that
  ships user-visible behavior is `feat`/`fix`, not `chore`.
- **Scope** is optional and, when present, names the service/package — e.g.
  `feat(keyvrm):`, `fix(auth):`, `ci(release):`.
- **Description** is imperative, lower-case, no trailing period.
- **Breaking changes**: append `!` after the type/scope (`feat(auth)!: …`) **and**
  add a `BREAKING CHANGE: <what/why>` footer.
- The subject line drives the version bump (see below), so classify honestly.

## Releases & CI

The checks are split along the network boundary, and the split is
load-bearing: everything in `ci.yml` is **offline** (`-mod=vendor`,
`GOPROXY=off`), everything that needs network lives in its own workflow
(`supply-chain.yml`, `functional.yml`, `sonar.yml`), and **no network-allowed job is ever a
dependency of an offline job**. That is what keeps the air-gap invariant from
being quietly weakened by a check that happens to need a proxy.

- `.github/workflows/ci.yml` — **offline only**, on push to `master`/`claude/**`
  and PRs. Four jobs: `build-test` (vet + static build + `go test`, and it records
  the built binary's size to the log and step summary, since one binary is the
  whole product and a size jump is a signal); `crossbuild`, a matrix over the six
  release targets (build-only — cross-built test binaries cannot run — so a
  build-tag or syscall mistake in a `darwin`/`windows` path is caught here rather
  than at release time); `test-race`, `go test -race` on the primary target, a
  separate job because `-race` needs `CGO_ENABLED=1` and that must never leak into
  the shipped static binaries; and `lint`, with golangci-lint pinned and its
  download checksum-verified against the release's published `checksums.txt`
  (it lints the `functional` build tag too). `build-test` also runs
  `test/functional` with no cloud: its devstack tests skip, and the harness's
  own tests plus the Vault CLI against the fake server run offline.
  Go is resolved as `1.27.x` + `check-latest` rather than `go-version-file:
  go.mod`, so the binaries get the newest 1.27 patch stdlib instead of the exact
  version go.mod pins — see the comment in the workflow before changing it. The
  pin sits a minor above go.mod's floor on purpose: it has to match the gofmt
  the pinned golangci-lint bundles (above). Two rules follow. When a dependency
  raises the floor in go.mod, these pins move in the same commit if the floor
  would overtake them — every offline job runs `GOTOOLCHAIN=local` and fails
  outright otherwise. And when the golangci-lint pin moves to a build made with
  a newer Go, these pins and `make fmt` move with it.
- `.github/workflows/supply-chain.yml` — the **network-allowed** checks, on the
  same triggers plus a weekly cron and `workflow_dispatch`. Five jobs:
  `vendor-integrity` re-derives the vendor tree (`go mod download && go mod
  verify`, then `go mod vendor`) and diffs it against the committed one — this
  closes a real gap, because `-mod=vendor` does not consult `go.sum`, so a
  hand-edited vendored source file used to build, vet and `go mod verify` green;
  `govulncheck` (pinned, installed from the module proxy) covers the dependency
  graph *and* the toolchain's stdlib; `dependency-review` runs on PRs only and
  fails on high-severity advisories or copyleft/unknown licences entering the
  graph. If `vendor-integrity` fails, either a dependency change skipped `make
  tidy` or `vendor/` was hand-edited — which AGENTS.md forbids and this job now
  actually catches. The other two guard the public Go API (see "Public Go API"):
  `importer` builds and tests `test/importer`, a nested module that uses `pkg/`
  only through its public import paths (and fails if its dependency graph
  picks up cobra, pflag, x/term or `internal/`), and `api-compat` runs
  `scripts/api-compat.sh` — apidiff, pinned, between the latest `v*` tag and
  HEAD — which fails on an incompatible `pkg/` change the range does not mark
  breaking. Both need the network (the nested module resolves its own graph;
  apidiff installs from the proxy), which is why they live here.
- `.github/workflows/functional.yml` — the **nightly devstack run**, also
  network-allowed: `schedule` (02:23 UTC), `workflow_dispatch`, and a push to
  `master`/`claude/**` that touches the workflow, `scripts/devstack/**`,
  `test/functional/**` or `internal/vault/vaulttest/**` (so an edit to the
  bring-up or the tests is proved on a runner by the commit that makes it; that
  push is also what gives a branch its first run, since `workflow_dispatch`
  reaches a branch only after one). One job per supported cell — zed and
  caracal on `ubuntu-22.04` (ML2/OVS), epoxy and latest on `ubuntu-24.04`
  (ML2/OVN) — with `fail-fast: false`, a 75-minute timeout, and a single
  `concurrency` group so two runs never stack at once. It is a **thin
  wrapper**: enable `/dev/kvm`, build koc offline, `scripts/devstack/up.sh
  --series <cell> --gha`, `scripts/devstack/smoke.sh --koc <that binary>`, `make
  functional` with the same binary (the Go functional tests, under the cell's
  `KOC_FT_AUTH`: env openrc on zed and caracal, `clouds.yaml` on epoxy and
  latest), and on failure `collect-logs.sh` plus a 14-day artifact. Every devstack decision
  lives in `up.sh`, which was proved on runner-sized VMs first
  (`docs/verification/2026-09-27-devstack-bringup.md`, "Results"); a fix for a
  runner-only failure goes into `up.sh` under `--gha`, never into a workflow
  step, and the results doc changes with it. The artifact is public (so is the
  repository), so it carries devstack's own output only — the whole-system
  journal, `functional.env` and `openrc.env` are excluded at upload. A red cell with a failed
  "Stack devstack" step is devstack or the runner image; a red "Smoke" step
  with a `koc …` FAIL line, or a red "Functional tests" step, is koc.
- `.github/workflows/sonar.yml` — the **SonarQube Cloud scan**, network-allowed:
  push to `master`/`claude/**`, pull requests, `workflow_dispatch`. A `token` job turns
  "is `SONAR_TOKEN` available?" into an output (a job-level `if` cannot read
  secrets), so fork/Dependabot PRs and an unconfigured repository skip the
  `scan` job instead of failing it. `scan` checks out full depth, runs `make
  cover` offline, and passes `sonar.projectVersion` from the latest `v*` tag. It
  refuses to scan without one. See "SonarQube Cloud".
- `.github/dependabot.yml` — weekly `gomod` and `github-actions` updates, each
  grouped into a single PR. Commit-message prefixes are set to `build` (gomod) and
  `ci` (actions) so Dependabot speaks Conventional Commits and
  `scripts/release-notes.sh` keeps categorising its commits correctly. Dependabot
  re-runs `go mod vendor` in its gomod PRs; `vendor-integrity` is the check that a
  PR which forgot to did not land.
- `.github/workflows/release.yml` — drives **GoReleaser** (`.goreleaser.yaml`),
  triggered **only** by pushing a `v*` tag. There is deliberately no
  `workflow_dispatch`: a dispatch run's ref is the branch it started from, never
  the tag it would create, so it cannot satisfy the `release` environment's
  `v*`-tag deployment rule and is rejected before any step runs. Creating the tag
  in an earlier job does not help either — GitHub does not trigger workflows from
  a ref pushed with `GITHUB_TOKEN`. Tag-driven instead means the environment rule
  is meaningful, immutability needs no scripted pre-check, and an agent cannot cut
  a release unilaterally. GoReleaser builds the six static binaries (linux/darwin/windows ×
  amd64/arm64), four **`.rpm`/`.deb` packages** (nfpms — the target is an
  air-gapped RHEL-derivative node where Homebrew is useless; these install the
  shell completions to the system directories, which the cask cannot), a
  `checksums.txt`, a **bundle of per-artifact SPDX SBOMs** (syft, tarred by
  `scripts/sbom-bundle.sh` into one `koc_<ver>_sboms.tar.gz` so ten loose
  documents stop burying the binaries on the release page), a **keyless cosign
  signature** over `checksums.txt`, and the GitHub Release, then publishes a
  **Homebrew cask** (`Casks/koc.rb`) to `ftarasenko/homebrew-tap` — so
  `brew install ftarasenko/tap/koc` works. The workflow additionally records a
  **build-provenance attestation** for the artifacts. Pushing to the tap needs a
  cross-repo fine-grained PAT stored as `HOMEBREW_TAP_TOKEN` in the **`release`
  environment** (the built-in `GITHUB_TOKEN` cannot push to a second repo). That
  environment has no required reviewers — it is a scoping boundary, not a gate —
  but its deployment rule admits only `v*` **tags**, so no run on an arbitrary
  branch can reach the PAT. cosign's keyless signing needs `id-token: write`. The `go build` stays offline via `-mod=vendor`; the
  release body is **not** GoReleaser's changelog (disabled) — after publish the
  workflow sets it with `gh release edit --notes-file` from
  `scripts/release-notes.sh` (GoReleaser v2 ignores `--release-notes`; see below).
- `.github/workflows/delete-release.yml` — dispatch to delete a release, then its
  tag, in two separate steps. It runs in the `release-admin` environment, which
  **does** require a reviewer: it is the only workflow that destroys published
  artifacts. The steps are split because a ruleset protecting `refs/tags/v*`
  denies `GITHUB_TOKEN` the tag deletion, so a combined `--cleanup-tag` would fail
  the run after the release was already gone; separated, the run summary says
  exactly which half succeeded and what an admin must still do.
- `.github/workflows/prune-release-assets.yml` — dispatch (`dry_run` defaults to
  true) to reclaim storage by deleting `.tar.gz`/`.zip`/`.rpm`/`.deb` from
  releases outside a keep window, while **retaining** `checksums.txt`, its
  cosign bundle (plus the older detached signature/certificate) and the SBOM
  bundle — note the SBOM bundle ends in `.tar.gz`, so its always-keep guard, not
  the suffix filter, is what protects it. Because builds are byte-reproducible from
  the tag, a pruned release stays verifiable and re-derivable; deleting the
  checksums would end that, which is why the retain list is redundant with the
  delete list on purpose. Pruned versions stop installing via the Homebrew cask.
  Its keep-list is keyed on asset **suffix**, so changing what `signs:` writes
  means editing that jq filter in the same commit.

`checksums.txt` on its own is not an integrity story: whoever can swap an artifact
in a release can swap the checksums with it. The cosign signature is what roots
the trust, and the checksums then chain to every artifact. The verification
command (no key distribution needed) is in `.goreleaser.yaml` next to the `signs:`
block and in README "Prebuilt binaries".

**The signature is a Sigstore bundle (`checksums.txt.bundle`), not a detached
`.sig`/`.pem` pair.** cosign v3 made the standardized bundle the default, and the
migration was not optional: under that default the `--output-signature` /
`--output-certificate` flags the old config passed are *silently ignored* rather
than rejected, so the release would have published no signature at all — and
since `release.yml` only runs on a `v*` tag, no PR check could have caught it.
The bundle also carries the transparency-log inclusion proof, which is what makes
the air-gapped `--trusted-root` verification in SECURITY.md possible; cosign v3
deprecated `--offline` in favour of exactly that. Three consequences to keep in
mind when touching this: verifying needs cosign v3+ (or v2.6+ with an explicit
`--new-bundle-format`); releases up to v0.22.0 still carry the old pair and a v3
client falls back to it automatically; and `.goreleaser.yaml` names
`--new-bundle-format` explicitly so a future default flip cannot repeat the
failure silently.

**GoReleaser has no `before:` hooks, on purpose.** Hooks run inside the same
workflow step as GoReleaser, and that step carries `HOMEBREW_TAP_TOKEN` — a PAT
with write access to a second repository. A `go run ./cmd/koc completion …` hook
would therefore execute repository code, and the whole `vendor/` tree, with that
token in scope on every release. Completion generation moved out to **`make
completions`**, run in its own secret-free step before GoReleaser. Locally, run
`make completions` before `goreleaser release`/`build` or the archives will be
missing `completions/`.

**The cask's quarantine step is a `custom_block`, not a GoReleaser hook.** The
binary is unsigned, so the cask has to strip `com.apple.quarantine` from the
staged `koc` or macOS refuses to launch it. GoReleaser emits
`homebrew_casks.hooks.post.install` as Homebrew's `postflight do … end`, which
Homebrew 6.0.16 deprecated in favour of the declarative `postflight_steps` —
every `brew` command touching the cask printed a deprecation warning, and
Homebrew deprecations become errors a few releases later. GoReleaser cannot emit
the `*_steps` stanzas yet (goreleaser/goreleaser#6870, PR #6873, milestone
v2.19.0) and has no way to emit *both* spellings, so `.goreleaser.yaml` writes
the stanza itself through `custom_block`: the cask probes at load time and picks
`postflight_steps` on Homebrew >= 6.0.13, `postflight` below that, so it installs
on every Homebrew and warns on none.

The three Homebrew versions that matter, because they are three releases apart
and the obvious probe gets it wrong: the `postflight_steps` **stanza** landed in
6.0.0, the `run`/`on_macos` **steps** the block actually calls landed in 6.0.13,
and `postflight` was **deprecated** in 6.0.16. So the probe asks
`Homebrew::InstallSteps::DSL.method_defined?(:run)`, not
`respond_to?(:postflight_steps)` — the latter is true on 6.0.0–6.0.12, where the
block body would then die. Two more things, both spelled out next to the block:
the Homebrew path token has to be escaped as `{{ "{{staged_path}}" }}` because
GoReleaser runs the whole generated cask through its own template engine as a
final pass (the legacy branch spells the same path `#{staged_path}`, since the
steps DSL has no Ruby interpolation); and `custom_block` lands right after
`cask "koc" do`, which is fine because Homebrew orders artifacts by class rather
than by file position. `hooks.post.install_steps`, when GoReleaser ships it,
still cannot express the fallback branch, so this block outlives it.

**Builds are byte-reproducible, and it takes more than one setting.** `builds:
mod_timestamp: {{ .CommitTimestamp }}` fixes the binary, but a tar/zip records
every *member's* mtime, so `LICENSE`, `README.md` and the generated completions
would still carry the runner's checkout/generation time and two builds of the same
tag would hash differently. Hence `archives: builds_info.mtime` plus a per-file
`info.mtime`, and `nfpms: mtime`, all pinned to `{{ .CommitDate }}`. Note the two
formats: the build's `mod_timestamp` wants unix seconds (`.CommitTimestamp`),
the archive/package mtime fields want RFC 3339 (`.CommitDate`). Do not
"simplify" any of these away — reproducibility is what lets an air-gapped
consumer re-derive the checksums instead of trusting them.

Third-party actions are pinned to **full commit SHAs** with the human-readable
version in a trailing comment (`uses: owner/action@<40-hex> # vX.Y.Z`) — supply-
chain hygiene for the air-gapped/FSTEC target, and the exact form Dependabot's
`github-actions` ecosystem understands and bumps. A version tag is not an
acceptable pin; if `api.github.com` is unreachable when adding an action, resolve
the SHA before merging rather than landing a tag with a TODO.

### Release notes are generated from the commit log

`release.yml` builds the GitHub Release body by running `scripts/release-notes.sh
<tag>`, which walks the Conventional Commits between the previous tag and the tag
being cut and groups them by type. GitHub's own `generate_release_notes` is
**not** used: it categorizes merged PRs by label, and this repo commits straight
to `master`, so it would yield only a compare link. Commit-type → heading:

| Commit type            | Release-notes heading      |
| ---------------------- | -------------------------- |
| `!` / `BREAKING CHANGE`| `### ⚠️ Breaking changes`  |
| `feat`                 | `### Features`             |
| `fix`                  | `### Bug fixes`            |
| `perf`                 | `### Performance`          |
| `refactor`             | `### Refactoring`          |
| `docs`                 | `### Documentation`        |
| `test`                 | `### Tests`                |
| `build`/`ci`/`chore`   | `### Build & tooling`      |
| (unrecognized)         | `### Other`                |
| `Deprecated:` footer   | `### Deprecations` (too)   |

**The quality of the notes is the quality of the subject lines** — each commit's
`<description>` becomes one bullet verbatim (its scope is bolded). So keep
commits focused and their subjects self-describing; a single mega-commit yields a
single vague bullet.

A `Deprecated: <identifier> …` footer — what the public Go API's deprecation
policy asks of the commit that deprecates an exported identifier (see "Public
Go API") — adds a bullet under `### Deprecations`, right after the breaking
changes; the commit itself is still listed under its type. Use one footer per
identifier.

A breaking change is listed **once**, under `### ⚠️ Breaking changes`, and not
again under its own type — the heading is the one line a consumer must not miss,
and a duplicated bullet buries it. Breaking is detected from either signal the
spec allows: `!` after the type/scope, or a `BREAKING CHANGE:` footer in the
body. Use both, as the "Breaking changes" rule above says, but the notes are
correct if only one is present.

### Cutting a release (do this every time)

1. **Find the range.** Last tag: `git describe --tags --abbrev=0`. New commits:
   `git log --no-merges <lastTag>..HEAD`.
2. **Pick the version** from the highest-impact commit (semver): `BREAKING
   CHANGE`/`!` → MAJOR, `feat` → MINOR, `fix`/`perf` → PATCH. While the project is
   `0.y.z` there is no stable API, so a breaking change bumps MINOR and any `feat`
   bumps MINOR; a docs/ci/chore-only range is a PATCH.
3. **Preview the notes** locally: `scripts/release-notes.sh vX.Y.Z`. If a bullet
   reads badly, fix it by rewording the offending commit (e.g. `git commit
   --amend` before it is tagged), not by hand-editing the release afterwards.
4. **Push the tag** from a maintainer's workstation — that push *is* the trigger:
   ```sh
   git tag -a vX.Y.Z -m vX.Y.Z && git push origin vX.Y.Z
   ```
   The workflow then builds the six binaries, signs and attests them, generates
   the notes and publishes. Note that an agent sandbox cannot do this (pushing a
   tag ref returns 403), which is intentional — a release is a human act.
5. **Versions are immutable.** A published tag is never moved or re-released —
   pushing a tag that already exists fails at the git level, and there is no
   `mode: replace`. Once `vX.Y.Z` is out, the next change ships as a new version;
   re-cutting the same number would swap the bytes under a name consumers (e.g.
   Homebrew, which keys on the version string) have already cached and would not
   re-download. If a release **fails before it fully publishes** (e.g. a transient
   `uploads.github.com` flake → GoReleaser aborts before pushing the cask), it
   never reached consumers, so free the version for a clean re-cut by deleting it
   first: dispatch `delete-release.yml` with the tag (approve the `release-admin`
   gate, and delete the tag yourself if a ruleset protects it), then push the tag
   again. Do not paper over a partial release by re-using the same tag.

## Private data never leaves the org

`github.com/ftarasenko/go-openstackclient` is a **public** repository. Nothing
committed to it — code, comments, docs, tests, fixtures, commit messages — may
carry private data:

- cloud and host names (`*.itkey.com`, node names), internal IPs and URLs,
  cluster/region names;
- tokens, passwords, `X-Auth-Token` values, AppRole IDs, certificates, kubeconfigs;
- customer or project identifiers, real tenant/user UUIDs, real server names;
- raw request/response captures from a real cloud (`--debug` output), which
  contain all of the above.

Verification against a real cloud is still expected — report the *findings*
(ratios, timings, "identical output", upstream `file:line` citations) and keep
the evidence out of the repository. Fixtures and examples use
`example.com`/RFC 5737 addresses and synthetic UUIDs. When in doubt, leave it
out and mention it in the conversation instead.

The rule was applied retroactively: on 2026-08-08 the history was rewritten with
`git filter-repo --sensitive-data-removal` to purge two documents that named a
real cloud and its hosts, so every tag and branch changed SHA. Do not make that
necessary again — anything once pushed survives in forks, clones and pull-request
refs no matter what the history says afterwards.

## Do / don't

- **Do** keep new work reproducible offline, tested via the `runXxx` seam, and
  routed through the output layer.
- **Do** scrub private data before every commit — see "Private data never leaves
  the org".
- **Do** update `docs/coverage.md` in the same commit as any command-surface
  change.
- **Do** give every leaf command a functional test — and a new one before it
  lands (`TestEveryLeafIsCovered`).
- **Do** keep `pkg/` free of the CLI, the environment and package state, and
  follow the deprecation policy before removing or changing an exported
  identifier there (see "Public Go API").
- **Don't** hand-edit `vendor/`, import gophercloud v1, format structured output
  inline, land a new command without touching `docs/coverage.md` or without a
  functional test, add a line to `test/functional/uncovered.txt`, push private
  data to GitHub, or push to a branch other than the designated feature branch.
