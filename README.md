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
> - **volume** (cinder) — volume (incl. `set --state/--attached/--detached`),
>   attachment (reserve → connect → complete), snapshot, backup, type, service
> - **dns** (designate) — zone, recordset
> - **image** (glance) — image CRUD, `save`, project sharing
> - **network** (neutron) — network, subnet, router, port, floating ip,
>   security group (+rule), agent
> - **loadbalancer** (octavia) — load balancer, listener, pool, member, health
>   monitor, l7policy, l7rule, quota, amphora, provider, flavor and flavorprofile
>   (62 of python-octaviaclient's 82 commands), plus failover, stats and a
>   flattened status tree
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
Alongside them: **`.rpm` and `.deb` packages** for the two linux architectures
(for a local yum/apt mirror on an air-gapped node — these do install the shell
completions), an **SPDX SBOM bundle** (`koc_<ver>_sboms.tar.gz`, one
`*.spdx.json` per artifact inside), a keyless **cosign signature** over
`checksums.txt` (`checksums.txt.bundle`), and a GitHub
**build-provenance attestation**. Builds are byte-reproducible, so the checksums
can be re-derived independently from the tag.

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.bundle \
  --certificate-identity-regexp 'https://github.com/ftarasenko/go-openstackclient/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum -c checksums.txt

# What built it: repo, workflow, commit, runner.
gh attestation verify koc_<version>_linux_amd64.tar.gz \
  --repo ftarasenko/go-openstackclient
```

The two identity flags are the part that matters — without them you have checked
only that *somebody* signed the file. Verify the signature first, then let
`sha256sum -c` carry that trust to each artifact.

`checksums.txt.bundle` is a [Sigstore bundle][sigstore-bundle]: it carries the
signature, the signing certificate and the transparency-log inclusion proof in
one file, so nothing else has to be downloaded alongside it. Reading it needs
**cosign v3 or newer** (where the format is the default), or cosign v2.6+ with an
explicit `--new-bundle-format`. Releases up to and including **v0.22.0** instead
carry a detached `checksums.txt.sig` + `.pem` pair; a v3 client still verifies
those — pass `--certificate`/`--signature` in place of `--bundle` and it falls
back to the legacy path automatically.

Both commands reach the network by default (`cosign` consults the public Rekor
log, `gh` calls the GitHub API). On an isolated network, verify the bundle
against a `--trusted-root` file captured on a connected host — no Rekor call, no
`--offline` flag. `sha256sum -c checksums.txt` is fully offline and is what you
gate the install on. See [SECURITY.md](SECURITY.md) for that procedure, the
supported-version policy, and how to report a vulnerability.

[sigstore-bundle]: https://blog.sigstore.dev/cosign-3-0-available/

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
cask to `ftarasenko/homebrew-tap`. It is triggered by pushing a `v*` tag
(`git tag -a vX.Y.Z -m vX.Y.Z && git push origin vX.Y.Z`) — see AGENTS.md
"Cutting a release".

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
koc server add floating ip myvm 192.0.2.5
koc flavor create --ram 512 --disk 1 --vcpus 1 m1.tiny
koc project create demo --domain example
koc volume create --size 1 test-volume
koc volume set stuck-volume --state available --detached
koc volume attachment create test-volume myvm --connect --initiator iqn.2026-08.local:node1
koc network list --long
koc resource provider show <uuid> --allocations -f json
koc hypervisor list --gauge --sort ram --aggregate compute-hp
koc keyvrm recommendation list
koc vault kv copy -r deployments/example/dev deployments/example/staging
```

### Authentication

`koc` builds one authenticated `ProviderClient` per invocation and reuses it to
derive service clients. Credentials are resolved in this precedence order:

1. `--os-cloud` / `OS_CLOUD` — a named cloud from `clouds.yaml`
2. `OS_*` environment variables
3. Application credentials (`OS_APPLICATION_CREDENTIAL_ID` / `_SECRET`),
   honored through either path above.

Project-, domain- and system-scoped tokens are all supported:

| Scope | Flags |
| --- | --- |
| project | `--os-project-name` + `--os-project-domain-name` (or `--os-project-id`) |
| domain | `--os-domain-name` |
| system | `--os-system-scope all` (env `OS_SYSTEM_SCOPE`) |

A token has exactly one scope, so `--os-system-scope` conflicts with an
explicit `--os-project-name` / `--os-project-id` / `--os-domain-name` and is
rejected up front. A project inherited from `clouds.yaml`, the environment or a
Vault openrc is treated as background configuration instead and is overridden,
so `koc baremetal driver show ipmi --os-system-scope all` works from a shell
that already has a project-scoped openrc sourced. `all` is the only value
Keystone defines.

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

### Timeouts

| Purpose                        | Flag        | Env / default              |
| ------------------------------ | ----------- | -------------------------- |
| Whole-exchange HTTP cap        | `--timeout` | `OS_TIMEOUT` / `0` (unbounded) |

`--timeout <duration>` (e.g. `--timeout 90s`) caps a single HTTP request/response
exchange on **every** client `koc` builds — OpenStack, standalone Ironic, Vault
and Kubernetes. It is per request, not per command, so the `--wait` polling loops
are unaffected.

It **defaults to 0, meaning unbounded**, on purpose. A whole-exchange cap counts
the body transfer, so any default large enough for `koc image save` of a
multi-gigabyte image over a slow link is too large to be a useful guard, and any
default small enough to be useful would break those transfers. The failure a
default would be reaching for — an endpoint that accepts the connection and then
never answers — is bounded regardless by a fixed **60s response-header timeout**
that always applies and cannot be disabled: it fires on silence without ever
capping a transfer that is making progress. Set `--timeout` when you want a hard
ceiling on a specific invocation (a CI step, a health probe).

There is no upstream equivalent: keystoneauth has a session `timeout`, but
`python-openstackclient` registers no global flag for it.

### Output formats

`-f/--format` selects the renderer; `-c/--column` selects columns (repeatable,
case-insensitive, order-preserving); `--sort-column` sorts list output
(repeatable, for tie-breaks):

- `table` (default) — human-readable ASCII table
- `json` — array (list) / object (single resource)
- `yaml`
- `value` — plain, **tab-separated**, no headers, for scripting
- `csv` — RFC 4180 with a header row

**`-f value` is tab-separated, where `openstack` uses a single space.** Most
values that appear in it contain spaces (status strings, flavor names, fixed-IP
lists), so a space-joined row cannot be split back into its cells; a tab can.
The consequence for scripts: `openstack … -f value | cut -d' ' -f2` picks the
wrong field under `koc`. Use `cut -f2` (tab is `cut`'s default), `awk '{print
$2}'`, or `-c <column>` to select one column outright. Cells are otherwise
unquoted in both clients, so a value that itself contains a tab or a newline
breaks the one-cell-per-tab, one-row-per-line contract — prefer `-f csv` or `-f
json` for fields that may hold arbitrary text (image descriptions, `properties`,
server metadata). Newlines are passed through on purpose so that `koc zone export
showfile <id> -f value > zone.txt` yields a zonefile `zone import create` can read
back; control characters and ANSI escapes are stripped regardless.

Table output fits the terminal width: over-long cells wrap across lines when
stdout is a TTY (piped output stays unbounded, matching `openstack`). `--max-width
<n>` caps the width explicitly and `--fit-width` forces fitting even when piped.
A very large opaque cell (base64 `user_data`, cert bundles) is elided in the
table to a `<N bytes; …>` placeholder; the full value is always available via
`-f json/yaml`, `-f value`, or by naming it with `-c <column>`. `server show
--user-data` prints just the base64-decoded `user_data`.

**`-f json` renders the same view the table does, not the raw API object.** Keys
are the column titles a list command displays (`"Project ID"`, `"Service Name"`),
and a composite cell arrives pre-formatted — a port's `fixed_ips` is the string
`ip_address='192.0.2.5', subnet_id='…'` where `openstack -f json` gives an array
of objects. It is the right shape for `-c`-narrowed output and for reading, but
it is **not** a drop-in for upstream's JSON in a script that indexes into nested
fields. Timestamps are normalised: RFC 3339 when set, `null` when absent (never
Go's zero date).

`--sort-column <col>` sorts any list command's rows, in every format. It is a
client-side sort applied before `-c` narrows the columns, so the sort key does
not have to be one of the displayed columns, and it needs no support from the
API. Numeric columns compare numerically (`--sort-column Size` puts 9 before
10, not before 100), the sort is stable so repeated `--sort-column` flags break
ties, and column names are matched case-insensitively.

### Flag abbreviation

`openstack` is built on argparse, which accepts any **unambiguous prefix** of a
long option — so `--all` works for `--all-projects` and `--fit` for
`--fit-width`. `pflag` does not abbreviate, so `koc` normalises the command line
before parsing it and accepts the same abbreviations:

```sh
koc volume list --all      # → --all-projects
koc server list --fit      # → --fit-width
koc image list --form json # → --format json
```

The rules are deliberately conservative, so an abbreviation can never change the
meaning of a command line: only `--long` forms are considered (short flags and
clusters are untouched), a token that is already a real flag name is never
rewritten, and a prefix matching zero or **more than one** flag is left alone so
`koc` reports the usual "unknown flag" rather than guessing. Everything after a
bare `--` is positional and is not examined.

`--all` is therefore not one flag. Where upstream defines it, it is a real flag
with its own meaning — `image list --all` lists every *visibility*, `flavor list
--all` every flavor, `quota show --all` every service — and expansion does not
apply. Where the command has exactly one `--all…` flag it expands to that one
(`volume list --all` → `--all-projects`). Where it has several, as `server unset`
does with `--all-properties` and `--all-tags`, a bare `--all` is ambiguous and
rejected, which is what `openstack` does too.

### `--name` is an exact match

Most OpenStack APIs filter on `name` by **equality**, so a partial name matches
nothing and you get an empty table rather than an error. Glance is the strictest:
its query builder accepts only `in:` and `eq:` on `name` and rejects every other
operator, so there is no wildcard to reach for.

```sh
koc image list --name distro-9.7-x86_64             # exact — the only thing glance filters on
koc image list --name in:distro-9.7,distro-9.6      # several exact names (glance's in: operator)
koc image list --name-contains distro               # substring, case-insensitive (koc-native)
koc image list -f value -c ID -c Name | grep distro # same idea, when you want a real regex
```

`--name-contains` is filtered client-side, because glance cannot do it; `--name`
deliberately keeps upstream's exact semantics so `koc` and `openstack` return the
same rows for the same command. The two are mutually exclusive.

Nova is the exception worth knowing: `server list --name` is a server-side
**regular expression**, so `koc server list --name '^web-'` works as written.
`volume list`, `network list`, `port list` and `subnet list` are exact-match with
no `--name-contains` yet — pipe through `grep` there.

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

`--timing` prints one line per API call to stderr with its method, URL, status
and wall-clock duration — the signal for "why is this slow" without the body
dumps `--debug` produces. The two combine; passwords in a URL are redacted.

```
timing: GET    https://nova.example/v2.1/servers/detail 200 in 412ms
```

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
koc vault kv list  deployments/example/dev
koc vault kv get   deployments/example/dev/openrc     # prints values in cleartext
koc vault kv copy -r deployments/example/dev deployments/example/staging
koc vault kv export deployments/example/dev --recipient koc-export.pub -o .junit/vault.xml
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
| `--src-vault-kv-prefix` | `VAULT_SRC_PREFIX` | `--vault-kv-prefix` |
| `--src-vault-cacert` | `VAULT_SRC_CACERT` | the destination's |
| `--insecure-src-vault` | `VAULT_SRC_SKIP_VERIFY` | the destination's |

A **relative** path on either side is resolved against the global
`--vault-kv-prefix`, and each side can be moved off it on its own — the source
with `--src-vault-kv-prefix`, the destination with `--dst-vault-kv-prefix`
(env `VAULT_DST_PREFIX`). Neither override disturbs the other side, so a copy
between two prefixes of one Vault names only the prefix that moves, which matters
when the global one is auto-discovered from the cluster rather than typed:

```sh
# regions/a/dev → regions/b/dev, with regions/a discovered from the node
koc vault kv copy -r --dst-vault-kv-prefix regions/b dev dev
```

Any explicit source credential replaces the destination's credentials as a group,
so an inherited token can never silently win over a source AppRole. The env names
match the variables the KeyStack e2e pipeline already exports for its
`vault-helper.py`, so a cross-Vault copy needs no flags at all there:

```sh
export VAULT_ADDR=… VAULT_TOKEN=… VAULT_KV_PREFIX=deployments/example-e2e  # destination
export VAULT_SRC_ADDR=… VAULT_SRC_TOKEN=…                                  # source
export VAULT_SRC_ENGINE=secret_v2 VAULT_SRC_PREFIX=deployments/example
koc vault kv copy -r dev dev
```

Both prefixes are worth exporting: an unset `VAULT_KV_PREFIX` leaves a relative
destination path at the mount root, which for a `copy` is a silent write to the
wrong place rather than a read that visibly 404s.

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
koc vault kv export deployments/example/dev --recipient koc-export.pub -o .junit/vault.xml

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
make test        # go test ./...
make race        # go test -race ./...  (needs cgo; shipped binaries stay CGO_ENABLED=0)
make vet         # go vet ./...
make lint        # golangci-lint run ./...
make crossbuild  # compile all six release targets (build-only, offline)
make completions # generate completions/koc.{bash,zsh,fish}
make size        # print the built binary size
make tidy        # go mod tidy && go mod vendor
```

## Known limitations

A multi-perspective correctness review hardened the initial surface (auth domain
scoping, nova floating-IP microversion, `--wait` semantics, `--limit`, metadata
unset, cross-service resolution, debug redaction, and more). A few lower-risk
items are deferred and worth noting:

- **Name-not-found resolution is silent.** A name→ID resolver that finds no match
  passes the reference through as a literal ID, so a mistyped `--domain`/
  `--project` filter yields an empty result rather than an error, and
  `koc network delete typo-name` reports neutron's error for a malformed UUID
  rather than koc's "no such network". The `server` package is the exception and
  does error properly (`no server found with name "…"`); the other resolvers
  should be brought in line with it. UUIDs always short-circuit resolution.
- **`baremetal node set` uses JSON-patch `replace`** for scalar attributes; on
  some ironic builds `add` is needed for a previously-absent attribute.
- **`role assignment list` with both `--project` and `--domain`** sends both
  scope qualifiers; keystone treats them as mutually exclusive.
- **`--debug` elides large/binary bodies** (image up/downloads) and redacts
  tokens and credential fields; it does not pretty-print JSON.
- **`-f json` mirrors the rendered table**, not the API object — see "Output
  formats" above before consuming it from a script.
- **`baremetal port --name` can be set but not shown.** `port create`/`port set`
  send ironic's `name` attribute (API 1.88, OpenStack 2024.1), but gophercloud's
  port result type has no field for it and no catch-all, so `port show`/`port
  list` cannot render it back. Reading it needs a koc-owned DTO for the port
  reads.
- **`server delete/start/stop --all-projects` is accepted, not required.** koc
  always resolves a server name across projects, so the flag upstream needs for
  that is a no-op here rather than a gate; it exists so an `openstack`
  invocation carrying it does not fail on an unknown flag.

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
