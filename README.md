# koc — a single-binary OpenStack CLI for KeyStack

`koc` is a statically-linked Go replacement for `python-openstackclient`, built
for the KeyStack cloud. It mirrors the upstream `openstack` client's
noun → verb → flags syntax so operators fluent in OSC need no retraining, but
ships as **one dependency-free binary** suitable for air-gapped / FSTEC-regulated
deployment. No Python at runtime.

> **Status: milestone 1 (scaffold).** This drop contains the cross-cutting
> foundation — auth, TLS, output, microversions — plus one fully wired,
> end-to-end command (`koc baremetal node list`) with tests. The remaining
> service command surfaces (full baremetal, compute, identity, volume, dns,
> image, network) land in subsequent milestones.

## Build

Fully static, stripped binary:

```sh
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git describe --tags --always --dirty)" \
  -o koc ./cmd/koc
```

or just `make build`.

### Air-gapped / offline build

All dependencies are vendored (`vendor/` is committed). The build reproduces
offline with no module proxy:

```sh
GOFLAGS=-mod=vendor GOPROXY=off CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git describe --tags --always --dirty)" \
  -o koc ./cmd/koc
```

## Usage

```sh
koc baremetal node list
koc baremetal node list -f json
koc baremetal node list -c Name -c "Provisioning State" -f value
koc baremetal node list --provision-state active --long
```

### Authentication

`koc` builds one authenticated `ProviderClient` per invocation and reuses it to
derive service clients. Credentials are resolved in this precedence order:

1. `--os-cloud` / `OS_CLOUD` — a named cloud from `clouds.yaml`
2. `OS_*` environment variables
3. Application credentials (`OS_APPLICATION_CREDENTIAL_ID` / `_SECRET`),
   honored through either path above.

Both domain-scoped and project-scoped tokens are supported
(`--os-domain-name` vs. `--os-project-name` + `--os-project-domain-name`).

### TLS / mutual TLS

TLS is wired explicitly into the provider so behavior matches OSC:

| Purpose                 | Flag           | Env / clouds.yaml         |
| ----------------------- | -------------- | ------------------------- |
| Custom CA bundle        | `--os-cacert`  | `OS_CACERT` / `cacert`    |
| Client cert (mTLS)      | `--os-cert`    | `OS_CERT` / `cert`        |
| Client key (mTLS)       | `--os-key`     | `OS_KEY` / `key`          |
| Disable verification    | `--insecure`   | `OS_INSECURE` / `verify`  |

Hostname verification is on by default and the minimum TLS version is 1.2.
`--insecure` logs a warning to stderr. clouds.yaml `verify: false` is honored
unless overridden by an explicit flag/env.

### Output formats

`-f/--format` selects the renderer; `-c/--column` selects columns (repeatable,
case-insensitive, order-preserving):

- `table` (default) — human-readable ASCII table
- `json` — array (list) / object (single resource)
- `yaml`
- `value` — plain, **tab-separated**, no headers, for scripting
- `csv` — RFC 4180 with a header row

### Microversions

Each service client sets its own microversion; defaults negotiate the latest the
endpoint supports. Override per service:

- `--os-baremetal-api-version` / `OS_BAREMETAL_API_VERSION`
- `--os-compute-api-version` / `OS_COMPUTE_API_VERSION`
- `--os-volume-api-version` / `OS_VOLUME_API_VERSION`

Ironic emits `X-OpenStack-Ironic-API-Version`; nova/cinder use the generic
`OpenStack-API-Version` header (gophercloud sets this from `client.Type`).

### Diagnostics

`--debug` logs each HTTP request/response to stderr with auth tokens redacted.

## Layout

```
cmd/koc/main.go            cobra root entrypoint, version
internal/auth/             clouds.Parse + provider + TLS + per-service clients
internal/output/           -f/-c formatter (table/json/yaml/value/csv)
internal/cli/              root command wiring
internal/cli/baremetal/    baremetal (ironic) command group
```

## Development

```sh
make test     # go test ./...
make vet      # go vet ./...
make lint     # golangci-lint run ./...
make tidy     # go mod tidy && go mod vendor
```

## KeyStack documentation caveat

Command and flag names should be verified against the KeyStack command
reference at <https://docs.keystack.ru/>. At implementation time that site
returned HTTP 403 and was not reachable from the build environment, so the
`baremetal node list` surface here follows **upstream OSC semantics** and is
**unverified against KeyStack docs**. Where KeyStack later proves to differ,
KeyStack wins and the divergence will be captured in a code comment citing the
doc URL.

## Tech

- Go (see `go.mod`), gophercloud **v2** (`github.com/gophercloud/gophercloud/v2`)
- cobra + pflag
- Vendored dependencies for offline/air-gapped builds
