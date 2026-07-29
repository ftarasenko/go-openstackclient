# koc — a single-binary OpenStack CLI for KeyStack

`koc` is a statically-linked Go replacement for `python-openstackclient`, built
for the KeyStack cloud. It mirrors the upstream `openstack` client's
noun → verb → flags syntax so operators fluent in OSC need no retraining, but
ships as **one dependency-free binary** suitable for air-gapped / FSTEC-regulated
deployment. No Python at runtime.

> **Status: broad v1 surface.** The cross-cutting foundation (auth, TLS,
> output, microversions) is in place, and the following service command trees
> are implemented, each with httptest-based unit tests:
>
> - **baremetal** (ironic) — node lifecycle (create/delete/show/set/unset),
>   provision transitions (manage/provide/deploy/undeploy/rebuild/inspect, with
>   `--wait`), maintenance, power, boot device, ports, drivers, conductors
> - **server** (nova) — full lifecycle, add/remove volume·floating-ip·security-group,
>   console log/url, plus `compute service`, `hypervisor list` (with color
>   allocation **gauges**), `quota show`
> - **compute** — flavor, keypair
> - **identity** (keystone) — endpoint, domain, project, user, role
>   (+assignments), service, region, catalog, application credential, token,
>   group
> - **volume** (cinder) — volume, snapshot, backup, type, service
> - **dns** (designate) — zone, recordset
> - **image** (glance) — image CRUD, `save`, project sharing
> - **network** (neutron) — network, subnet, router, port, floating ip,
>   security group (+rule), agent
> - **placement** — resource provider (list/show/delete/trait), allocation, trait
> - **keyvrm** (Keystack Virtual Resource Manager — in-house) — app-config,
>   host-aggregate-config, availability-zone, event, recommendation
>
> In addition to the standard Keystone flow, credentials can be sourced from a
> standalone Ironic in a Kubernetes namespace (`--creds-from-ns`) or an
> openrc-style secret in Vault (`--creds-from-vault`).
>
> A few operations use raw `ServiceClient` requests where gophercloud v2 lacks a
> typed verb (server floating-IP actions, quota defaults, image
> activate/deactivate) — isolated behind small helpers and flagged in code.
> KeyVRM has no gophercloud package at all and uses the raw request layer end to
> end.

## Install

### Homebrew (macOS / Linux)

```sh
brew install ftarasenko/tap/koc
```

No `--cask` flag is needed — nothing else in the tap shares the name. The binary
is unsigned, so the cask strips the macOS quarantine flag on install; on Apple
Silicon Go already ad-hoc-signs the binary so it runs.

### Shell completion

`koc` ships cobra's completion generator for bash, zsh, fish and powershell:

```sh
koc completion zsh > "${fpath[1]}/_koc"   # zsh: then restart the shell
source <(koc completion bash)             # bash: current shell only
```

Release archives also bundle `completions/koc.{bash,zsh,fish}`. (The Homebrew
cask installs only the binary — casks have no completion stanza — so `brew`
users wire it up with the command above.)

### Prebuilt binaries

Each release publishes static binaries for **linux/amd64, linux/arm64,
darwin/amd64, darwin/arm64, windows/amd64, windows/arm64** with a
`checksums.txt`, attached to the [GitHub release](https://github.com/ftarasenko/go-openstackclient/releases).

## Build

Fully static, stripped binary:

```sh
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git describe --tags --always --dirty)" \
  -o koc ./cmd/koc
```

or just `make build`.

Releases are cut by GoReleaser (`.goreleaser.yaml`): the `release` workflow builds
the six static binaries, publishes the GitHub release, and pushes the Homebrew
cask to `ftarasenko/homebrew-tap`. Trigger it via `workflow_dispatch` with the
`tag` input (this environment blocks pushing tag refs from a workstation; the
workflow creates the tag server-side) — see AGENTS.md "Cutting a release".

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
koc baremetal node list -f json
koc baremetal node inspect cmp-039 --wait
koc server list --all-projects --long
koc server create --image ubuntu-cloudimage --flavor 1 --network private myvm
koc server create --image ubuntu-cloudimage --flavor 1 --nic net-id=<uuid> \
  --boot-from-volume 20 --boot-volume-type ssd --config-drive myvm
koc server add floating ip myvm 10.0.0.5
koc flavor create --ram 512 --disk 1 --vcpus 1 m1.tiny
koc project create demo --domain itkey
koc volume create --size 1 test-volume
koc network list --long
koc resource provider show <uuid> --allocations -f json
koc hypervisor list --gauge --sort ram --aggregate compute-hp
koc keyvrm recommendation list
koc vault kv copy -r deployments/itkey/dev deployments/itkey/e2e
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

#### Alternative credential sources

Two koc-specific, mutually exclusive flags source credentials outside the normal
`OS_*` / `clouds.yaml` flow (both use minimal in-repo REST clients — no client-go
or Vault SDK — to preserve the air-gap invariant):

- `--creds-from-ns <namespace>` reads a metal3 ironic-standalone-operator
  instance's basic-auth secret from a Kubernetes namespace and builds a
  **standalone Ironic** client (baremetal only, no Keystone).
  `--kubeconfig` / `--kube-context` select the cluster.
- `--creds-from-vault <path>` reads an openrc-style KV v2 secret from Vault and
  folds its `OS_*` into the normal Keystone flow (all services). The path may
  start with the KV mount (`secret_v2/…`), a leading `/` (absolute), or be
  relative to `--vault-kv-prefix`. Vault is reached via `--vault-*` flags /
  `VAULT_*` env; when those are absent on a cluster node, the address, namespace,
  role_id, KV mount/prefix and AppRole secret-id are auto-discovered from the LCM
  `k0s-system/lcm-config` ConfigMap and the `cert-manager/vault-approle` secret.
  Vault TLS uses the system roots (or `--vault-cacert`); pass `--insecure-vault`
  (env `VAULT_SKIP_VERIFY`) to skip Vault TLS verification. The global
  `--insecure` governs only the OpenStack/Keystone TLS, not Vault.

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

Table output fits the terminal width: over-long cells wrap across lines when
stdout is a TTY (piped output stays unbounded, matching `openstack`). `--max-width
<n>` caps the width explicitly and `--fit-width` forces fitting even when piped.
A very large opaque cell (base64 `user_data`, cert bundles) is elided in the
table to a `<N bytes; …>` placeholder; the full value is always available via
`-f json/yaml`, `-f value`, or by naming it with `-c <column>`. `server show
--user-data` prints just the base64-decoded `user_data`.

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

### Hypervisor allocation gauges

`koc hypervisor list --gauge` renders vCPU/RAM/Disk allocation as color bars with
warning/critical thresholds (`--warn-pct`/`--crit-pct`), overcommit ratios, an
`--aggregate` filter and `--sort`/`--reverse`. Column profiles auto-fit the
terminal width (detected via `golang.org/x/term`; override with `--width`),
`--ascii` falls back to plain bars, and `--color` forces `auto`/`always`/`never`.
Allocation figures come from placement (nova dropped these fields at microversion
2.88); nova supplies VMs/type/state/cpu_model/host_ip. `--check-actual` compares
real CPU/RAM usage scraped from each host's node_exporter (`--ne-*` flags tune the
scheme/port/suffix/concurrency/timeout). `-f json`/`csv` emit the raw numbers via
the output layer.

### Vault KV (`koc vault kv`)

A koc-specific command group — Vault is not an OpenStack service, and there is no
`python-openstackclient` equivalent. It authenticates with Vault credentials only
(never Keystone), so it works on a host that has no cloud credentials at all:

```sh
koc vault kv list  deployments/itkey/dev
koc vault kv get   deployments/itkey/dev/openrc     # prints values in cleartext
koc vault kv copy -r deployments/itkey/dev deployments/itkey/e2e
koc vault kv export deployments/itkey/dev --recipient koc-export.pub -o .junit/vault.xml
koc vault kv decrypt .junit/vault.xml -i koc-export.key
```

`copy` fills a gap in the Vault CLI itself, which has no `kv copy` — the
alternative is piping `vault kv get -format=json` into `vault kv put` per secret.
Without `-r` the source must be a single secret; with `-r` the whole subtree is
mirrored under the destination (nested folders included). `--dry-run` reports the
exact set of writes without performing any, `--skip-existing` leaves destination
secrets that already exist untouched, and `--src-version` pins a single copy to a
specific source version. Secret values are never printed by `copy` (or by
`--debug`, which logs only method/path/status) — `koc vault kv get` is the
explicit way to see them.

The Vault to talk to, and the **destination** of a copy, is the one described by
the global `--vault-*` flags (see [Authentication](#authentication)), including
LCM cluster auto-discovery. The **source** is addressed by `--src-vault-*`
overrides; each one left unset is inherited from the destination, so copying
between two paths of a single Vault needs no extra flags:

| flag | env | default |
| --- | --- | --- |
| `--src-vault-addr` | `VAULT_SRC_ADDR` | the destination's |
| `--src-vault-namespace` | `VAULT_SRC_NAMESPACE` | the destination's |
| `--src-vault-token` | `VAULT_SRC_TOKEN` | the destination's credentials |
| `--src-vault-role-id` / `--src-vault-secret-id` | `VAULT_SRC_ROLE_ID` / `VAULT_SRC_SECRET_ID` | the destination's credentials |
| `--src-vault-kv-mount` | `VAULT_SRC_ENGINE` | the destination's |
| `--src-vault-kv-prefix` | `VAULT_SRC_PREFIX` | the destination's |
| `--src-vault-cacert` | `VAULT_SRC_CACERT` | the destination's |
| `--insecure-src-vault` | `VAULT_SRC_SKIP_VERIFY` | the destination's |

Any explicit source credential replaces the destination's credentials as a group,
so an inherited token can never silently win over a source AppRole. The env names
match the variables the KeyStack e2e pipeline already exports for its
`vault-helper.py`, so a cross-Vault copy needs no flags at all there:

```sh
export VAULT_ADDR=… VAULT_TOKEN=…                  # destination
export VAULT_SRC_ADDR=… VAULT_SRC_TOKEN=…          # source
export VAULT_SRC_ENGINE=secret_v2 VAULT_SRC_PREFIX=deployments/itkey
koc vault kv copy -r dev dev
```

Only secret **data** is copied. KV v2 `custom_metadata`, version history and
`delete_version_after` are not, so the result is a copy of the current values,
not a replica.

#### Encrypted export (`kv export` / `kv decrypt`)

`koc vault kv export <path> --recipient <pub.pem>` writes the subtree as a JUnit
XML report for `artifacts:reports:junit` — one test case per secret, so a CI run
still shows which paths exist, which are empty (skipped) and which could not be
read (failure) — with every payload **encrypted**. There is no plaintext mode:
`--recipient` is required.

Each secret is sealed with a fresh AES-256-GCM key, itself wrapped to the
recipient's RSA public key with OAEP-SHA256 (standard library only, so `vendor/`
is unaffected). CI therefore holds only the public key: a leaked runner, artifact
or Pages copy yields nothing readable, and only the private-key holder can
recover the values.

```sh
# once, on the operator's machine
openssl genrsa -out koc-export.key 4096
openssl rsa -in koc-export.key -pubout -out koc-export.pub

# in CI, with the public key only (env KOC_EXPORT_RECIPIENT also works)
koc vault kv export deployments/itkey/dev --recipient koc-export.pub -o .junit/vault.xml

# later, by the key holder — prints Path/Key/Value rows, honours -f/-c
koc vault kv decrypt .junit/vault.xml -i koc-export.key
koc vault kv decrypt .junit/vault.xml -i koc-export.key -f json
```

An existing PKI-issued **certificate** is accepted as the recipient, so a
deployment's own cert can be used without extracting the key. `-o -` (or no `-o`)
writes to stdout, and `decrypt -` reads from stdin; `decrypt` needs no Vault
access at all. Payloads look like:

```
koc-enc:v1:rsa-oaep-sha256:aes-256-gcm
AgCt+YNs5aaiMNcYrgAFe7c0kI+DeSGtIWdjJmQOBxYYNvhf…
```

Secret **paths stay readable** — they are what makes the report useful — but each
path is the payload's GCM additional authenticated data, so moving a payload to
another test case makes decryption fail rather than silently succeed under the
wrong name. A secret named `ssl_certificates` is expanded one test case per key,
so each certificate is separately visible and separately encrypted. `decrypt`
never writes to a Vault: recovering a secret and re-injecting it stay separate
acts.

## Layout

```
cmd/koc/main.go            cobra root entrypoint, version
internal/auth/             clouds.Parse + provider + TLS + per-service clients
                           + --creds-from-ns / --creds-from-vault sources
internal/kube/             minimal read-only k8s REST client (no client-go)
internal/vault/            minimal Vault REST client (AppRole/token + KV v2)
internal/cli/vault/        "koc vault kv" list/get/copy/export/decrypt, no Keystone auth
internal/output/           -f/-c formatter (table/json/yaml/value/csv)
internal/cli/              root command wiring
internal/cli/resolve/      cross-service name→ID resolution
internal/cli/baremetal/    baremetal (ironic) command group
internal/cli/keyvrm/       KeyVRM in-house catalog service (raw request layer)
```

## Development

```sh
make test     # go test ./...
make vet      # go vet ./...
make lint     # golangci-lint run ./...
make tidy     # go mod tidy && go mod vendor
```

## Known limitations

A multi-perspective correctness review hardened the initial surface (auth domain
scoping, nova floating-IP microversion, `--wait` semantics, `--limit`, metadata
unset, cross-service resolution, debug redaction, and more). A few lower-risk
items are deferred and worth noting:

- **Name-not-found on list filters is silent.** A name→ID resolver that finds no
  match passes the reference through as a literal ID, so a mistyped `--domain`/
  `--project` filter yields an empty result rather than an error (write paths
  still 404 loudly). UUIDs always short-circuit resolution.
- **`baremetal node set` uses JSON-patch `replace`** for scalar attributes; on
  some ironic builds `add` is needed for a previously-absent attribute.
- **`role assignment list` with both `--project` and `--domain`** sends both
  scope qualifiers; keystone treats them as mutually exclusive.
- **`--debug` elides large/binary bodies** (image up/downloads) and redacts
  tokens and credential fields; it does not pretty-print JSON.

## KeyStack documentation caveat

Command and flag names should be verified against the KeyStack command
reference at <https://docs.keystack.ru/>. That site returns HTTP 403; the
documentation source is mirrored locally (Sphinx/reST) and is the working
reference. The KeyStack-specific extensions (`compute service set
--admin-state`, `server` dynamic server groups / `--availability-zone` /
`server list` created/deleted filters, `server evacuate --preserve-ephemeral`)
are verified against it. Other surfaces (e.g. `baremetal node list`) still
follow **upstream OSC semantics** and remain unverified; where KeyStack later
proves to differ, KeyStack wins.

## Tech

- Go (see `go.mod`), gophercloud **v2** (`github.com/gophercloud/gophercloud/v2`)
- cobra + pflag
- Vendored dependencies for offline/air-gapped builds
