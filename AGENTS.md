# AGENTS.md

Guidance for AI coding agents (and humans) working in this repository. Keep it
current when the structure or conventions change.

## What this is

`koc` — a single, statically-linked Go binary that replaces
`python-openstackclient` for the KeyStack cloud. No Python at runtime. It mirrors
the upstream `openstack` client's `noun → verb → flags` syntax and ships as one
dependency-free binary for air-gapped / FSTEC-regulated deployment.

- **Module**: `github.com/ftarasenko/go-openstackclient` (binary name: `koc`)
- **Go**: see `go.mod` (currently `go 1.25`); target ≥ 1.22
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
`forcetypeassert`, `gosec`, `nilerr`, `noctx`, `nolintlint`, `revive` (with
`exported`, `package-comments`, `unused-parameter`, `deep-exit` named explicitly)
and `usestdlibvars` — plus `max-issues-per-linter: 0` / `max-same-issues: 0` so a
run reports every occurrence rather than the first three. New code is expected to
pass all of it; a `//nolint` must name the linter and carry a rationale
(`nolintlint` enforces both). The narrow per-file exclusions in that file are
scoped on purpose — do not widen one to a linter or a package.

`nilnil` is deliberately **not** enabled. All of its hits in this tree are the
idiomatic "optional value is absent" pattern rather than ambiguous not-found
signalling: the `parseKeyValMap`/`parseProperties`-style helpers return a nil map
for empty input, and helpers like `buildGatewayInfo`/`imageVisibility` return a
nil pointer meaning "leave this field unset". A sentinel error would make every
call site worse. The reasoning is recorded in `.golangci.yml` next to the disabled
entry; re-check it there before enabling.

Gate before committing: `gofmt` clean, `go vet` clean, `golangci-lint` **0
issues**, `go test ./...` green, the offline static build succeeds, and — if the
commit changes the command surface — `docs/coverage.md` is updated (see "Coverage
tracking").

## Layout

```
cmd/koc/main.go            cobra root entrypoint; version var; signal-cancelled context
internal/auth/             one authenticated ProviderClient per invocation + per-service clients
  options.go               global flags (auth/TLS/microversion/output/debug/timing/timeout + creds-from-*), env-defaulted
  transport.go             http.Client construction: --timeout whole-exchange cap (0 = off) + fixed 60s response-header timeout
  provider.go              Authenticate(): clouds.yaml OR OS_* OR --creds-from-*; domain/scope resolution
  tls.go                   explicit *tls.Config (CA bundle, mTLS, --insecure, TLS 1.2 min)
  services.go              auth.Client factory methods: Compute()/Identity()/Volume()/...
  credsfrom.go             --creds-from-ns/-vault: standalone basic-auth ironic + Vault openrc → OS_*
  debug.go                 --debug transport: redacts tokens+secrets, elides large bodies
internal/kube/             minimal read-only k8s REST client (kubeconfig + secret/Ironic reads); no client-go
internal/vault/            minimal Vault REST client (AppRole login / token + KV v2 read + X-Vault-Namespace)
internal/output/           -f/--format {table,json,yaml,value,csv} and -c/--column layer
internal/cli/keyvrm/       KeyVRM (in-house catalog service); typed request layer (types.go/requests.go) + cobra verbs
internal/cli/vault/        "koc vault kv" list/get/copy/export/decrypt (package vaultcli); Vault creds only
internal/cli/quota/        "koc quota show|set" — the one cross-service noun (nova+cinder+neutron)
internal/cli/              root.go wires every service's command group onto the root
internal/cli/resolve/      cross-service name→ID (image→glance, network→neutron, project→keystone)
internal/cli/<service>/    one package per service; one file per noun; a client.go helper
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
> endpoints, DTO fields, or verbs), re-check and update `internal/cli/keyvrm/`
> (`requests.go`/`types.go` for the API, the verb files for the CLI) to keep them
> in sync. See `docs/proposals/keyvrm.md` for the command mapping.

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
3. Results route through the output layer: `o.WriteList(w, output.Table{...})`
   for lists, `o.WriteSingle(w, fields, values)` for a single resource. Never
   `fmt.Println` structured output.
4. `context` comes from `cmd.Context()`. Pagination is `List(...).AllPages(ctx)`
   then `Extract*`. `--limit` is a hard result cap where the API treats it only
   as a page size (truncate after Extract).
5. **Update `docs/coverage.md` in the same commit** — see "Coverage tracking".

Client helpers (`client.go`) authenticate once via `a.Authenticate(ctx)` then
call the right `auth.Client` factory. When a command needs a **second** service
(cross-service name resolution), return the `*auth.Client` too and derive the
secondary client lazily (see `server/client.go` `newComputeSession`).

## Minimum supported cloud: Zed (2022.2)

`koc` runs against a fleet spanning **OpenStack Zed (2022.2) to current**. A
command that only works on the newest release is not done. Each service's Zed
release caps its microversion at:

| Service | Zed version | Max microversion |
| --- | --- | --- |
| ironic | 21.4.0 | **1.82** |
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
`Short`. Known post-Zed ironic features: `node children list` (1.83), `node
unhold` (1.85), `node firmware list` (1.86), `node service` (1.87), runbooks
(1.92), inspection rules (1.96).

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
  one match, and error on multiple; a zero-match currently falls back to the
  literal ref (documented trade-off in README "Known limitations").
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

The per-commit checks are split across two workflows along the network boundary,
and the split is load-bearing: everything in `ci.yml` is **offline**
(`-mod=vendor`, `GOPROXY=off`), everything that needs network lives in
`supply-chain.yml`, and **no job in `supply-chain.yml` is ever a dependency of an
offline job**. That is what keeps the air-gap invariant from being quietly
weakened by a check that happens to need a proxy.

- `.github/workflows/ci.yml` — **offline only**, on push to `master`/`claude/**`
  and PRs. Four jobs: `build-test` (vet + static build + `go test`, and it records
  the built binary's size to the log and step summary, since one binary is the
  whole product and a size jump is a signal); `crossbuild`, a matrix over the six
  release targets (build-only — cross-built test binaries cannot run — so a
  build-tag or syscall mistake in a `darwin`/`windows` path is caught here rather
  than at release time); `test-race`, `go test -race` on the primary target, a
  separate job because `-race` needs `CGO_ENABLED=1` and that must never leak into
  the shipped static binaries; and `lint`, with golangci-lint pinned and its
  download checksum-verified against the release's published `checksums.txt`.
  Go is resolved as `1.25.x` + `check-latest` rather than `go-version-file:
  go.mod`, so the binaries get the newest 1.25 patch stdlib instead of the exact
  version go.mod pins — see the comment in the workflow before changing it.
- `.github/workflows/supply-chain.yml` — the **network-allowed** checks, on the
  same triggers plus a weekly cron and `workflow_dispatch`. Three jobs:
  `vendor-integrity` re-derives the vendor tree (`go mod download && go mod
  verify`, then `go mod vendor`) and diffs it against the committed one — this
  closes a real gap, because `-mod=vendor` does not consult `go.sum`, so a
  hand-edited vendored source file used to build, vet and `go mod verify` green;
  `govulncheck` (pinned, installed from the module proxy) covers the dependency
  graph *and* the toolchain's stdlib; `dependency-review` runs on PRs only and
  fails on high-severity advisories or copyleft/unknown licences entering the
  graph. If `vendor-integrity` fails, either a dependency change skipped `make
  tidy` or `vendor/` was hand-edited — which AGENTS.md forbids and this job now
  actually catches.
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
  `checksums.txt`, an **SPDX SBOM per artifact** (syft), a **keyless cosign
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
  signature/certificate and the SBOMs. Because builds are byte-reproducible from
  the tag, a pruned release stays verifiable and re-derivable; deleting the
  checksums would end that, which is why the retain list is redundant with the
  delete list on purpose. Pruned versions stop installing via the Homebrew cask.

`checksums.txt` on its own is not an integrity story: whoever can swap an artifact
in a release can swap the checksums with it. The cosign signature is what roots
the trust, and the checksums then chain to every artifact. The verification
command (no key distribution needed) is in `.goreleaser.yaml` next to the `signs:`
block and in README "Prebuilt binaries".

**GoReleaser has no `before:` hooks, on purpose.** Hooks run inside the same
workflow step as GoReleaser, and that step carries `HOMEBREW_TAP_TOKEN` — a PAT
with write access to a second repository. A `go run ./cmd/koc completion …` hook
would therefore execute repository code, and the whole `vendor/` tree, with that
token in scope on every release. Completion generation moved out to **`make
completions`**, run in its own secret-free step before GoReleaser. Locally, run
`make completions` before `goreleaser release`/`build` or the archives will be
missing `completions/`.

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

**The quality of the notes is the quality of the subject lines** — each commit's
`<description>` becomes one bullet verbatim (its scope is bolded). So keep
commits focused and their subjects self-describing; a single mega-commit yields a
single vague bullet.

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
- **Don't** hand-edit `vendor/`, import gophercloud v1, format structured output
  inline, land a new command without touching `docs/coverage.md`, push private
  data to GitHub, or push to a branch other than the designated feature branch.
