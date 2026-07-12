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
- **CLI**: cobra + pflag
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
  options.go               global flags (auth/TLS/microversion/output/debug), env-defaulted
  provider.go              Authenticate(): clouds.yaml OR OS_*; domain/scope resolution
  tls.go                   explicit *tls.Config (CA bundle, mTLS, --insecure, TLS 1.2 min)
  services.go              auth.Client factory methods: Compute()/Identity()/Volume()/...
  debug.go                 --debug transport: redacts tokens+secrets, elides large bodies
internal/output/           -f/--format {table,json,yaml,value,csv} and -c/--column layer
internal/cli/              root.go wires every service's command group onto the root
internal/cli/resolve/      cross-service name→ID (image→glance, network→neutron, project→keystone)
internal/cli/<service>/    one package per service; one file per noun; a client.go helper
```

Services: `baremetal` (ironic), `server`+`compute` (nova), `identity` (keystone),
`volume` (cinder), `dns` (designate), `image` (glance), `network` (neutron),
`placement`.

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

## Releases & CI

- `.github/workflows/ci.yml` — offline vet + static build + `go test` + pinned
  golangci-lint, on push to `main`/`claude/**` and PRs.
- `.github/workflows/release.yml` — builds static binaries for **linux, darwin,
  windows × amd64/arm64** (+ `.sha256`) on a `v*` tag or `workflow_dispatch`
  (with a `tag` input; the workflow creates the tag + Release server-side via
  `GITHUB_TOKEN`, since the environment blocks pushing tag refs).
- `.github/workflows/delete-release.yml` — dispatch to delete a release + tag.

## Do / don't

- **Do** keep new work reproducible offline, tested via the `runXxx` seam, and
  routed through the output layer.
- **Don't** hand-edit `vendor/`, import gophercloud v1, format structured output
  inline, or push to a branch other than the designated feature branch.
