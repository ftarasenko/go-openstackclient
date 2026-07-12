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
make build   # CGO_ENABLED=0 static, -trimpath, -ldflags "-s -w -X main.version=..."
make test    # go test ./...
make vet     # go vet ./...
make lint    # golangci-lint run ./...   (golangci-lint v2; config in .golangci.yml)
make fmt     # gofmt -w
make tidy    # GOFLAGS= go mod tidy && go mod vendor   (only when deps change)
```

The air-gap invariant — every build/test must pass with **no module proxy**:

```sh
GOFLAGS=-mod=vendor GOPROXY=off CGO_ENABLED=0 go build ./...
GOFLAGS=-mod=vendor GOPROXY=off go test ./...
```

If you add an import from a new gophercloud subpackage, run `make tidy` (needs
network once) so `vendor/` and `vendor/modules.txt` stay complete; otherwise the
offline build breaks. Do not hand-edit `vendor/`.

Gate before committing: `gofmt` clean, `go vet` clean, `golangci-lint` **0
issues**, `go test ./...` green, and the offline static build succeeds.

## Layout

```
cmd/koc/main.go            cobra root entrypoint; version var; signal-cancelled context
internal/auth/             one authenticated ProviderClient per invocation + per-service clients
  options.go               global flags (auth/TLS/microversion/output/debug + creds-from-*), env-defaulted
  provider.go              Authenticate(): clouds.yaml OR OS_* OR --creds-from-*; domain/scope resolution
  tls.go                   explicit *tls.Config (CA bundle, mTLS, --insecure, TLS 1.2 min)
  services.go              auth.Client factory methods: Compute()/Identity()/Volume()/...
  credsfrom.go             --creds-from-ns/-vault: standalone basic-auth ironic + Vault openrc → OS_*
  debug.go                 --debug transport: redacts tokens+secrets, elides large bodies
internal/kube/             minimal read-only k8s REST client (kubeconfig + secret/Ironic reads); no client-go
internal/vault/            minimal Vault REST client (AppRole login / token + KV v2 read + X-Vault-Namespace)
internal/output/           -f/--format {table,json,yaml,value,csv} and -c/--column layer
internal/cli/keyvrm/       KeyVRM (in-house catalog service); typed request layer (types.go/requests.go) + cobra verbs
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

Client helpers (`client.go`) authenticate once via `a.Authenticate(ctx)` then
call the right `auth.Client` factory. When a command needs a **second** service
(cross-service name resolution), return the `*auth.Client` too and derive the
secondary client lazily (see `server/client.go` `newComputeSession`).

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

- `.github/workflows/ci.yml` — offline vet + static build + `go test` + pinned
  golangci-lint, on push to `main`/`claude/**` and PRs.
- `.github/workflows/release.yml` — drives **GoReleaser** (`.goreleaser.yaml`) on
  a `v*` tag or `workflow_dispatch` (with a `tag` input; the workflow creates the
  tag server-side via `GITHUB_TOKEN`, since the environment blocks pushing tag
  refs). GoReleaser builds the six static binaries (linux/darwin/windows ×
  amd64/arm64), a `checksums.txt`, and the GitHub Release, then publishes a
  **Homebrew cask** (`Casks/koc.rb`) to `ftarasenko/homebrew-tap` — so
  `brew install ftarasenko/tap/koc` works. Pushing to the tap needs a cross-repo
  fine-grained PAT stored as the `HOMEBREW_TAP_TOKEN` repo secret (the built-in
  `GITHUB_TOKEN` cannot push to a second repo). The `go build` stays offline via
  `-mod=vendor`; the release body is **not** GoReleaser's changelog (disabled) —
  it is supplied via `--release-notes` from `scripts/release-notes.sh` (below).
- `.github/workflows/delete-release.yml` — dispatch to delete a release + tag.

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
| `build`/`ci`/`chore`   | `### Build & tooling`      |
| (unrecognized)         | `### Other`                |

**The quality of the notes is the quality of the subject lines** — each commit's
`<description>` becomes one bullet verbatim (its scope is bolded). So keep
commits focused and their subjects self-describing; a single mega-commit yields a
single vague bullet.

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
4. **Trigger the build** via `workflow_dispatch` on `release.yml` with the `tag`
   input — the environment blocks pushing tag refs, so the workflow (running with
   its own `GITHUB_TOKEN`) creates the tag server-side, builds the six binaries,
   generates the notes, and publishes.
5. **Versions are immutable.** A published tag is never moved or re-released —
   `release.yml` refuses to run if the tag already exists, and there is no
   `mode: replace`. Once `vX.Y.Z` is out, the next change ships as a new version;
   re-cutting the same number would swap the bytes under a name consumers (e.g.
   Homebrew, which keys on the version string) have already cached and would not
   re-download. If a release **fails before it fully publishes** (e.g. a transient
   `uploads.github.com` flake → GoReleaser aborts before pushing the cask), it
   never reached consumers, so free the version for a clean re-cut by deleting it
   first: dispatch `delete-release.yml` with the tag, then re-dispatch
   `release.yml`. Do not paper over a partial release by re-running the same tag.

## Do / don't

- **Do** keep new work reproducible offline, tested via the `runXxx` seam, and
  routed through the output layer.
- **Don't** hand-edit `vendor/`, import gophercloud v1, format structured output
  inline, or push to a branch other than the designated feature branch.
