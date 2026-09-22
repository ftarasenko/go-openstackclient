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
Silicon Go already ad-hoc-signs the binary so it runs. That step picks its own
spelling at load time — Homebrew's declarative `postflight_steps` on 6.0.13 and
newer, the older `postflight` block below that — so the cask installs on any
Homebrew version and prints a deprecation warning on none.

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
koc server create --image ubuntu-cloudimage --flavor 1 --network private \
  --server-group web --hint different_host=<uuid> --hint different_host=<uuid> myvm
koc server add floating ip myvm 192.0.2.5
koc flavor create --ram 512 --disk 1 --vcpus 1 m1.tiny
koc project create demo --domain example
koc volume create --size 1 test-volume
koc volume set stuck-volume --state available --detached
koc volume attachment create test-volume myvm --connect --initiator iqn.2026-08.local:node1
koc network list --long
koc resource provider show <uuid> --allocations -f json
koc hypervisor list --gauge --sort ram --aggregate compute-hp
koc compute host drain cmp-039 --wait --parallel 2
koc compute host drain cmp-039 --cold --dry-run
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

#### Where the password comes from

`--os-password` / `OS_PASSWORD` is the usual answer, but neither is a good place
for a secret: a flag value is visible in `ps` and lands in the shell history, and
an environment variable is inherited by every child process. Two more ways in:

| Source | Use it for |
| --- | --- |
| `--os-password-stdin` | scripts and CI — `koc … --os-password-stdin < secret` |
| the interactive prompt | a shell session, and a `clouds.yaml` entry that deliberately stores no password |

`--os-password-stdin` follows `docker login --password-stdin`: `koc` reads
standard input, strips one trailing line ending, and uses the rest verbatim
(leading and trailing spaces included — only the newline goes). More than one
line is an error, since that is a whole openrc piped in by mistake rather than a
password. It conflicts with an explicitly typed `--os-password` and with
`--creds-from-ns` / `--creds-from-vault`, which bring their own credentials; it
overrides `OS_PASSWORD`, which is background configuration, and it outranks a
named cloud's stored password the same way a typed `--os-password` does.

```sh
koc server list --os-password-stdin < ~/.config/koc/password
pass show keystack/admin | koc server list --os-cloud keystack --os-password-stdin
```

When nothing supplies a password and the run is interactive, `koc` asks for it on
the terminal without echo, the way `python-openstackclient` does — including for
a named cloud whose `clouds.yaml` entry has a `username` but no `password`. A
non-interactive run is never prompted: it fails with `no credentials found`
instead of blocking on a pipe nobody is going to write to. Neither source is
consulted when the request authenticates without a password (application
credentials, a pre-issued token), and `--os-password-stdin` is not combined with
a prompt — stdin has already been spent.

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

`server list`'s default table is upstream's — `ID`, `Name`, `Status`,
`Networks`, `Flavor` — with one deviation: upstream's sixth column is the image
*name*, resolved with a glance lookup `koc` does not make, so rather than
printing a 36-character image UUID in its place `koc` leaves it out and offers
it as the opt-in `Image ID`. Below nova 2.47 the flavor's name comes from a
single flavor listing per invocation, and only when the column is actually
being rendered — so `-c Name -c Status` does not pay for it.

A few columns are **opt-in**: they are in neither the default nor the `--long`
table, and naming one in `-c/--column` (or `--sort-column`) materialises it.
`server list` carries eleven — `Created At`, `Image ID`, `Flavor ID`,
`Availability Zone`, `Host`, `Task State`, `Power State`, `Project ID`,
`User ID`, `Security Groups`, `Properties` — mirroring the extras upstream's
`server list` appends the same way. So a server's age comes from the listing:

```sh
koc server list --all-projects -c Name -c "Created At" --sort-column "Created At"
```

without which reading creation time costs one `server show` per server. Naming
one adds it for that invocation only — the default and `--long` tables are not
otherwise widened, so nothing that reads either positionally is affected.
`server list --help` names the full set.

### Live refresh (`--watch`)

Every read verb — all 221 `list` and `show` leaves — takes `--watch`, which
refreshes it in place instead of exiting:

```sh
koc server list --all --host node-14 -c Name -c Status --watch=1s
koc server list --watch                   # bare: every 2s, as watch(1) defaults
koc baremetal node list --watch=5s --watch-count 12
```

This replaces `watch -n1 koc …`, which re-executes the binary every tick. For
`koc` that is not a redraw but a full cold start, and the cost is almost all
overhead: a TLS handshake to Keystone, a **new Fernet token minted and
persisted**, the catalog parsed again, and every `--project`/`--user` name
resolved again — before the one request you actually wanted. At `-n1` a single
operator terminal is a sustained ~2 req/s write-ish load on the control plane.
`--watch` authenticates once and keeps the connection, so an hour of watching
costs one token instead of 3,600.

It also fixes what `watch(1)` does to the display. Under `watch` the command's
stdout is a pipe, so `koc` renders the table unbounded and `watch` hard-clips
each line at the right edge, silently cutting columns (`--max-width $(tput
cols)` is the usual workaround); colour is off for the same reason, so
`hypervisor list --gauge` loses its thresholds. `--watch` measures the real
terminal and fits the table to it, keeps colour, paints inside the **alternate
screen buffer** so your scrollback survives and quitting leaves the terminal
exactly as it was, and writes each frame in one call so there is no flicker.

A status line reports what is happening:

```
koc server list --all --host node-14 · every 1s · 12:03:45 · 42 rows · 218ms · ok
```

Only the command's own filters appear there — the global flags are where the
credentials live, and a status line is exactly the sort of thing that ends up in
a screenshot.

**Changes are highlighted** (on a terminal, unless `NO_COLOR` is set): a changed
cell in reverse video, a new row in green, a row that has just left the result
held dimmed for one more frame. Rows are matched by identity — the `ID` column,
else `Name`, else position — not by character offset the way `watch -d` does, so
a server being created or deleted does not light up every row below it. Watching
`-c Name -c Status`, you see `BUILD → ACTIVE` land rather than having to spot it.

**A failed refresh does not blank the screen.** The last good frame stays up and
the status line says how stale it is and why (`stale 4s · 2 errors · last: 503
Service Unavailable`), because a fleet-wide listing crossing every cell picks up
a transient 5xx often enough that exiting on one would make the feature useless.
Two failures are still fatal: a rejected credential, since retrying it every
second hammers Keystone and can lock the account out, and a settled error before
any frame has rendered (an unknown column, a bad filter) where there is nothing
to hold and no reason to expect a different answer. If refreshes start taking
more than half the interval, the loop widens it rather than saturating the
control plane, and says so.

On a terminal, single keys drive it: `q` quit, `space`/`r` refresh now, `p`
pause/resume, `+`/`-` lengthen and shorten the interval, `d` toggle
highlighting — and **`?` for the key map**, which is also where `--watch --help`
points, so none of that has to be remembered. (`+` is *slower*: it adds to the
interval.) Whether highlighting is on is reported on the status line as
`diff on`/`diff off`, because a frame with it off looks exactly like a frame in
which nothing changed.

A watch outlives its token. Keystone's default Fernet lifetime is an hour, and
gophercloud re-authenticates on the 401 by itself — so an expiry costs one extra
token, not one per tick. A credential Keystone *refuses* is the opposite: the
loop stops at once rather than retrying a rejected login every second, which is
how an account gets locked out. A Keystone that is merely unreachable is ridden
out like any other transient failure.

`q` and Ctrl-C **cut a refresh short** rather than waiting it out. The loop runs
the refresh on its own goroutine and watches the keyboard meanwhile, so a
fleet-wide query that takes seconds does not make the two keys you press when
the screen looks stuck the slowest ones to answer — measured on a 4s refresh,
that was 3.05s before and is now immediate. Keys that only change what is
painted (`p`, `+`, `-`, `?`) are answered mid-refresh too; `d` and `r` wait for
it, because they touch what the refresh is doing.

The keys need only *stdin* to be a terminal, so `koc … --watch | tee` still
works — it just has no keys, and the status line says so by leaving the hint
off.

**A list taller than the screen scrolls.** A 250-server `server list --all`
measures 495 physical lines at 120 columns, so the frame is a window onto it
rather than the first screenful of it: `↓`/`j` and `↑`/`k` a line, `PgDn`/`Ctrl-F`
and `PgUp`/`Ctrl-B` a page, `End`/`G` and `Home`/`g` either end. The table's
header stays pinned above the window while the body moves, the status line says
where you are (`lines 35–52 of 251`), and the marker at the cut names the keys
instead of only counting what it hid. The place you scrolled to survives each
refresh, and is clamped if the next one returns a shorter list.

`--watch-compact` puts each row on one physical line, cutting an over-long cell
short with an ellipsis instead of wrapping it onto further lines. On that same
fleet it takes 492 rendered lines to 251 — the `Networks` column is what wraps —
and a row of constant height makes the change highlighting far easier to follow.
It is opt-in: without it a watched command renders exactly as an unwatched one
does.

**Piped output stays composable.** With no terminal (or with `--watch-plain`)
`koc` emits no escape sequences at all and appends one whole snapshot per tick,
so `--watch -f json | jq .` is a clean stream and `-f csv` writes its header
once:

```sh
koc server list --watch=5s -f json | jq -c '.[] | select(.Status != "ACTIVE")'
```

| Flag | Default | |
| --- | --- | --- |
| `-w`, `--watch[=DURATION]` | off / `2s` | the interval attaches with `=`; minimum `250ms` |
| `--watch-diff` | on when a terminal | highlight what changed |
| `--watch-count N` | `0` | stop after N refreshes (failures included) |
| `--watch-errors tolerate\|exit` | `tolerate` | `exit` is `watch -e` |
| `--watch-until-change` | off | exit 0 on the first refresh that differs — `watch -g` |
| `--watch-plain` | off | append frames, no escape sequences, even on a terminal |
| `--watch-no-title` | off | suppress the status line — `watch -t` |

`--watch` is registered on the read verbs only, so `koc server delete --watch`
is an unknown flag rather than a loop. Two `show` commands are deliberately
excluded: `console url show` *creates* a console session on every call, and
`server password show` reads a key passphrase from the terminal. `--debug` and
`--timing` are refused alongside a repainting `--watch` (their per-request output
would overwrite the frame) and allowed with `--watch-plain`.

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

### User data (`--user-data`)

`koc server create --user-data <file>` injects a cloud-init payload, and
`koc server rebuild --user-data <file>` / `--no-user-data` replace or clear the
one a server already has (nova microversion 2.57 or later, so every supported
cloud). In all three the **file's bytes are the payload**: koc base64-encodes
them for nova and does not inspect or transform the content, matching
`openstack`. A path is the only accepted form — `-` is a filename, not stdin.
`koc server show <server> --user-data` prints the payload back, decoded.

> **Releases v0.28.0 through v0.32.1 can corrupt the payload.** Those versions
> left the encoding to the SDK, which sent the file unencoded whenever its bytes
> happened to parse as base64 — a file with no `#`, `:`, `-`, `=` or space whose
> length is a multiple of four (`runcmd\nls\n`, `hostname\n`) would take that
> branch. Nova accepts it, so nothing fails: the instance boots ACTIVE having
> run whatever the payload decoded to. On those versions, encode the file
> yourself and pass the encoded text, which the affected code passes through
> unchanged:
>
> ```sh
> base64 -w0 cloud-init.yaml > cloud-init.b64      # a file
> printf '%s' "$USER_DATA" | base64 -w0 > cloud-init.b64   # a shell variable
> koc server create --user-data cloud-init.b64 …   # v0.28.0 - v0.32.1 only
> ```
>
> `base64 -w0` matters: without it GNU coreutils wraps at 76 columns, and while
> both nova and the affected code tolerate the newlines, the unwrapped form is
> what the encoded file is meant to be. On macOS the flag is `-b0`, or pipe
> through `tr -d '\n'`.
>
> **Remove the workaround when you upgrade.** From v0.33.0 the file is encoded
> unconditionally, so a pre-encoded file is encoded a second time and the guest
> receives the base64 text instead of the payload. After upgrading, pass the
> plain file.

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

### Draining a compute host (`koc compute host …`)

Two verbs to empty one compute host of its servers, split on the thing nova
actually enforces — whether the host is still alive:

```sh
koc compute host drain cmp-039 --wait --parallel 2   # host is up, no downtime
koc compute host drain cmp-039 --cold                # host is up, a reboot each
koc compute host evacuate cmp-039 --wait             # host has failed
```

| | Source host | Guest downtime | Server statuses nova accepts |
| --- | --- | --- | --- |
| `drain` | up | none | `ACTIVE`, `PAUSED` |
| `drain --cold` | up | a reboot | `ACTIVE`, `SHUTOFF` |
| `evacuate` | **down** | it already crashed | `ACTIVE`, `SHUTOFF`, `ERROR` |

`drain` moves servers off a healthy host, live or (with `--cold`) by shutting
each one down first; `evacuate` rebuilds the servers of a host that has already
failed, which nova refuses to do while the host is up. Each verb checks the
service state once, up front, and refuses the other's case by name, so picking
the wrong one costs a message rather than one refusal per server. A status a
verb cannot move is reported as `skipped` naming the verb that can take it.

There is no bulk API behind any of this — nova has no host-level endpoint — so
each verb is a loop over the same per-server action the single-server verbs
post, and each reports as it goes: on a terminal, a status line repainted in
place with the elapsed time and the server currently moving; anywhere else, one
line per state change. Progress goes to **stderr** and the result table to
stdout, so `-f json` stays parseable.

Shared flags: `--dry-run` (what would move), `--target-host` (pin the
destination), `--max-servers` (cap how many move), `--parallel` (how many at
once), `--wait` (follow each server off the host rather than returning once nova
accepts the request). **Both exit non-zero if any server fails** — the table's
`Detail` column carries nova's reason for each.

A cold migration does not finish on its own: nova parks each server in
`VERIFY_RESIZE` awaiting a confirm. `--cold` confirms as it goes, so the host is
actually empty when it returns; `--confirm=false` leaves them pending for `koc
server resize --confirm`/`--revert`. Confirming means waiting, so `--cold`
implies `--wait`. On a cloud that sets nova's `resize_confirm_window`, nova may
confirm a server first — that is reported as `auto-confirmed by nova`, not
treated as an error.

These cover `nova host-evacuate-live`, `host-servers-migrate` and
`host-evacuate`; `openstack` has no equivalent for any of them. See
`docs/coverage.md` → "Naming deviations" for why the first two are one verb.

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
| `--src-vault-kv-prefix` | `VAULT_SRC_PREFIX` | the destination's |
| `--src-vault-cacert` | `VAULT_SRC_CACERT` | the destination's |
| `--insecure-src-vault` | `VAULT_SRC_SKIP_VERIFY` | the destination's |

Any explicit source credential replaces the destination's credentials as a group,
so an inherited token can never silently win over a source AppRole. The env names
match the variables the KeyStack e2e pipeline already exports for its
`vault-helper.py`, so a cross-Vault copy needs no flags at all there. The
destination side accepts the symmetric names too: `VAULT_ENGINE` and
`VAULT_PREFIX` are fallback aliases of `VAULT_KV_MOUNT` / `VAULT_KV_PREFIX`
(the `VAULT_KV_*` names win when both are set):

```sh
export VAULT_ADDR=… VAULT_TOKEN=…                  # destination
export VAULT_ENGINE=secret_v2 VAULT_PREFIX=deployments/example
export VAULT_SRC_ADDR=… VAULT_SRC_TOKEN=…          # source
export VAULT_SRC_ENGINE=secret_v2 VAULT_SRC_PREFIX=deployments/old
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

### S3 object storage (`koc s3`)

Another koc-specific group, for the LCM cluster's **Garage** — the S3 store that
holds GitLab's object storage and the MariaDB dumps the `backup-db` scheduled
pipeline uploads. Like `koc vault kv` it never authenticates against Keystone:
S3 credentials alone are used, so it works on a host with no cloud credentials.

```sh
koc s3 bucket list
koc s3 bucket show db-backups                  # exists? reachable? versioned?
koc s3 bucket create scratch
koc s3 bucket delete scratch                   # the bucket must be empty
koc s3 object list db-backups
koc s3 object list db-backups --delimiter /    # one level, like a directory
koc s3 object show db-backups/<key>            # HEAD only, no transfer
koc s3 object delete db-backups/<key>
koc s3 object delete db-backups/e2e- -r        # every key under a prefix
koc s3 du         db-backups --human --group   # size, per storage class
koc s3 download   db-backups/<key> ./dump.mbs.gz.enc
koc s3 download   db-backups/<key>.sha256 -    # "-" streams to stdout, so it pipes
koc s3 download   db-backups/2026/ ./restore -r
koc s3 upload     ./dump.mbs.gz.enc db-backups/
koc s3 upload     ./restore db-backups/2026/ -r
koc s3 copy       db-backups/<key> db-backups/latest.mbs.gz.enc
koc s3 move       db-backups/<key> archive/
koc s3 presign    db-backups/<key> --expire 1h
koc s3 sync       ./restore s3://db-backups/2026/ --delete
```

Every ref also accepts the `s3://<bucket>/<key>` spelling, so a path copied from
`s5cmd` or `aws s3` pastes in unchanged, and a wildcard (`"db-backups/2026/*.gz"`)
selects many where a command takes one.

`bucket list` is scoped to the **access key**, not to the store: Garage answers
with the buckets that key is granted, so a key made for one bucket lists exactly
that one.

**Transfers.** `upload` has no 5 GiB ceiling: anything past `--part-size` (16 MiB
by default) goes out as a multipart upload, `--concurrency` parts at a time, and
a failure aborts it so a half-written object never becomes visible. `-` as the
source reads **standard input**, which is what makes a dump streamable without
staging it on disk:

```sh
mysqldump --all-databases | gzip | koc s3 upload - db-backups/nightly.sql.gz
```

`--recursive` on `download`, `upload`, `copy`, `move` and `object delete` works
on a whole prefix or tree, `--concurrency` objects at a time — which is most of
why a bulk restore finishes, since a thousand small objects are otherwise a
thousand serial round trips. `--include`/`--exclude` take globs (where `*` spans
`/`, as in s5cmd) and `--dry-run` prints exactly what would move.

`copy` and `move` are **server-side**: the source is named in a header, so the
bytes never travel through koc and a 100 GiB object is one small request. A move
is the copy and then the delete, in that order — a failure leaves the source
intact rather than losing the object.

**`sync`** makes a destination match a source, transferring only what differs —
local→S3, S3→local, or S3→S3 (server-side). It is the one command in the group
that *requires* the `s3://` prefix to mark a remote side: it has two sides, and
mistaking which is local is how a `--delete` removes the wrong one. An object
moves when it is absent, when the sizes differ, or when the source is newer;
`--size-only` drops the timestamp test, which is the right rule for content that
never changes in place (a dated backup). The S3-side timestamp is the object's
Last-Modified, because S3 keeps no record of the source file's mtime and a
listing would not return one if it did — correct in the steady state, with one
seam: a tree restored by `download` carries the restore time, so syncing it
*back* re-uploads once and then settles. `--delete` turns the sync into a mirror
and is refused when the source turned out to be empty unless `--force` is also
given, so a mistyped source cannot erase the destination.

`object delete --recursive` is the one destructive shape in the group, so it is
never inferred from a trailing slash — and `--dry-run` prints exactly the keys it
would remove without touching any of them. It is also how a bucket is emptied
before `bucket delete`, which never removes objects implicitly. Keys go out in
batches of up to 1000 per request, so emptying a large bucket costs a thousandth
of the round trips.

`du` is a listing folded into a sum, because S3 has no "how big is this" call:
no object is downloaded, but every key is walked, one request per 1000 of them.
Sizes are exact bytes everywhere unless `--human` is given — a rounded
"14.2 GiB" is not a number a script can add up.

`presign` prints a URL that reads (or, with `--method put`, writes) one object
with no credentials attached to the recipient. It makes **no request** — pure
local signing — so it works offline, and the URL is a bearer credential for that
key until it expires: treat it like a password and keep `--expire` short.

**Versioning** (`bucket show`, `bucket set --versioning`, `--all-versions`,
`--version-id`) is implemented but Garage has none — it reports every bucket as
unversioned — so those are for a koc pointed at AWS, Ceph RGW or MinIO.

A failed request is retried with exponential backoff (`--s3-retries`, 5 by
default): on a cluster network a reset connection mid-transfer should not cost
the whole command. `--s3-anonymous` sends requests unsigned, for a bucket
granted to everyone.

Credentials come from flags, from the environment, or from a Kubernetes Secret:

| flag | env | default |
| --- | --- | --- |
| `--s3-endpoint` | `AWS_ENDPOINT_URL`, `S3_ENDPOINT`, `s3_host` | — |
| `--s3-access-key` | `AWS_ACCESS_KEY_ID`, `S3_ACCESS_KEY`, `s3_access_key` | — |
| `--s3-secret-key` | `AWS_SECRET_ACCESS_KEY`, `S3_SECRET_KEY`, `s3_secret_key` | — |
| `--s3-region` | `AWS_REGION`, `AWS_DEFAULT_REGION`, `S3_REGION`, `s3_region` | `garage` |
| `--s3-cacert` | `AWS_CA_BUNDLE`, `S3_CACERT` | system roots |
| `--s3-creds-from-ns` | `KOC_S3_CREDS_FROM_NS` | — |
| `--s3-anonymous` | `S3_ANONYMOUS` | off (no credentials needed or used) |
| `--s3-retries` | — | `5` |
| `--insecure-s3` | `S3_SKIP_VERIFY` | off (the global `--insecure` also applies) |

The three env families are deliberate: `AWS_*` so an existing `aws`/`boto`
environment works unchanged, `S3_*` as the neutral spelling, and the lower-case
`s3_host` / `s3_access_key` / `s3_secret_key` / `s3_region` that the KeyStack
installer writes as GitLab **group CI/CD variables** — so a pipeline job can call
`koc s3` with no flags at all. Prefer the environment to `--s3-secret-key`: a
flag value is visible in the process list and in shell history.

`--s3-creds-from-ns <namespace>[/<secret>][:<key>]` reads them from a Kubernetes
Secret over the same read-only API `--creds-from-ns` uses (`--kubeconfig` /
`--kube-context` apply). The secret name defaults to `gitlab-object-storage`, so
on an LCM cluster node GitLab's own key needs nothing else:

```sh
koc s3 --s3-creds-from-ns lcm-gitlab bucket list
```

It handles the shapes S3 credentials actually appear in: one key per value
(`access_key`/`secret_key`, `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`, …), and
a Secret holding a whole connection config as a single value — GitLab's `config`
key, an `rclone.conf` or an `.s3cfg` — from which the endpoint, region and
path-style setting are read as well. A Secret carrying only a key pair
supplies only that, and the endpoint still comes from `--s3-endpoint` or the
environment. Note that on a KeyStack LCM cluster **only**
GitLab's key is a Kubernetes Secret; the `db-backup` key is held by Garage itself
and by the masked GitLab group variables, so export it from one of those.

Addressing is **path-style** by default (`<endpoint>/<bucket>/<key>`), matching
the `--host-bucket` setting the backup pipeline gives `s3cmd`; `--no-path-style`
switches to `<bucket>.<endpoint>`, which needs a wildcard DNS record.

Uploads are a single signed `PUT` — there is no multipart support, so the
server's own single-part ceiling (5 GiB on Garage and on AWS) applies. Downloads
never overwrite an existing file without `--force`, and a transfer that fails
partway removes the partial file rather than leaving a truncated dump that looks
complete.

## Layout

```
cmd/koc/main.go            cobra root entrypoint, version
internal/auth/             clouds.Parse + provider + TLS + per-service clients
                           + --creds-from-ns / --creds-from-vault sources
internal/kube/             minimal read-only k8s REST client (no client-go)
internal/vault/            minimal Vault REST client (AppRole/token + KV v2)
internal/s3/               minimal S3 REST client (SigV4, no aws-sdk/minio-go)
internal/cli/vault/        "koc vault kv" list/get/copy/export/decrypt, no Keystone auth
internal/cli/s3/           "koc s3" bucket/object/du/transfer/copy/presign, no Keystone auth
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
- **Cinder's pool capacity can mix raw and replicated figures.** `koc volume
  backend pool list` reports what cinder's scheduler was handed: one
  `total_capacity_gb` and one `free_capacity_gb` per pool. A driver that
  computes the total from the cluster's raw capacity while reporting free space
  after replication yields a pair that cannot both be right, and koc does not
  try to reconcile them — it has no way to know a given driver's replication
  factor. `--long` adds every remaining capability key the driver reported, and
  `koc volume backend capability show <host>` shows the driver's own set, which
  is where a raw figure can be compared against cinder's normalised one. Gate on
  the driver's key when the backend publishes one.
- **`volume list --host` filters client-side.** Cinder's server-side `host`
  filter is admin-only and absent from the default `resource_filters.json`
  allow-list, so a stock deployment ignores it silently — an empty table that
  reads as "no volumes on that host". koc therefore reads the page and matches
  `os-vol-host-attr:host` itself, which also means `--limit` caps what survives
  the filter rather than what cinder returned.
- **`server delete/start/stop --all-projects` is accepted, not required.** koc
  always resolves a server name across projects, so the flag upstream needs for
  that is a no-op here rather than a gate; it exists so an `openstack`
  invocation carrying it does not fail on an unknown flag.
- **`port list --all-projects` is presentational.** Neutron has no cross-project
  switch: it scopes a listing to the caller's project only when the token is not
  an admin one, so an admin already sees every project's ports and there is no
  `all_tenants` parameter to send (a cloud running neutron's filter-validation
  extension would reject one). Upstream OSC therefore gives `port list` no such
  flag. koc accepts it anyway — so an `openstack`-shaped invocation carrying it
  does not fail on an unknown flag — and makes it useful by inserting the
  `Project ID` column, without which the rows of a multi-project result cannot be
  told apart. It also honours `ALL_PROJECTS` and is refused together with
  `--project`, which narrows to one project.
- **`router list` / `security group list --all-projects` is a no-op.** Same
  neutron reasoning as `port list`, but both tables already show the Project
  column, so the flag is accepted only so a script carrying it gets a listing
  rather than a usage error. Use `--project` to narrow to one project.

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
