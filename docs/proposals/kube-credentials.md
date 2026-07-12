# Proposal: koc credential sources — `--creds-from` (ironic k8s) and `--creds-from-vault` (openrc KVP)

Status: `--creds-from` design agreed, impl pending; `--creds-from-vault` design added, impl if small
Context: metal3 `ironic-standalone-operator` + external HashiCorp Vault on k0s (project_k / lcm-k0s)

Two independent, koc-specific credential sources that skip the usual `OS_*` /
clouds.yaml setup. They are mutually exclusive (error if both set):

| Flag | Source | Auth result | Scope |
|---|---|---|---|
| `--creds-from <ns>` | k8s Ironic secret in a namespace | standalone HTTP-basic-auth ironic client (no Keystone) | `baremetal` only |
| `--creds-from-vault <kv-path>` | openrc-style KV v2 secret in Vault | populates `OS_*` → normal Keystone auth | all services |

Both reuse the same **minimal-REST, zero-new-dependency** muscle (stdlib
`net/http`+`crypto/tls` and vendored `yaml.v3`), mandated by the AGENTS.md
air-gap invariant.

---

# Part 1 — `--creds-from <namespace>` (ironic, k8s)

## Goal (agreed UX)

Load the standalone-Ironic credentials straight out of the Kubernetes namespace
and use them to run normal baremetal commands — no `OS_*` / clouds.yaml, no
Keystone:

```
koc --creds-from lcm-ironic baremetal node list
```

`--creds-from <ns>` is a **koc-specific global flag** (no python-openstackclient
equivalent). When set, koc reads the Ironic instance and its API secret from that
namespace over the Kubernetes API and builds a standalone HTTP-basic-auth Ironic
service client pointed at the advertised endpoint.

## Why this deployment needs it

Ironic here is metal3 `ironic-standalone-operator`, **not** Keystone-fronted. The
API uses HTTP basic-auth over TLS; there is no token/catalog. koc's normal
gophercloud+Keystone flow cannot talk to it, so `--creds-from` bypasses Keystone
and constructs a basic-auth `baremetal` client directly.

## Cluster findings (verified on kolla@10.224.142.244, cluster `llm-sl972-k0s-rc10`)

Namespaces: `ironic-operator` (operator), `lcm-ironic` (the Ironic instance).

Everything needed is derivable from the Kubernetes API + secret — no hardcoding:

| Fact | Source |
|---|---|
| API secret name | `Ironic.spec.apiCredentialsName` → `ironic-service-csmcj` |
| username / password | that secret's `username` / `password` keys (basic-auth) |
| endpoint host:port | `Ironic.spec.networking.ipAddress` (`10.224.142.182`, keepalived VIP) `: apiPort` (`6385`) |
| TLS CA + server name | `Ironic.spec.tls.certificateName` (`ironic-tls`) → `ca.crt`; server name from `tls.crt` SAN (`ironic.<cluster>.vm.lab.itkey.com`) |

### The two-secret trap

`lcm-ironic` holds **two** `ironic-service-<rand>` basic-auth secrets, both
username `ironic`, both owned by the `Ironic` CR — only the one referenced by
`spec.apiCredentialsName` is live. The loader MUST resolve it from the CR, never
"list and pick the first."

### End-to-end validation

```
curl --cacert ca.crt -u ironic:<pw> \
  https://ironic.<cluster>.vm.lab.itkey.com:6385/v1/nodes
  → {"nodes": []}        # cert-verified basic-auth works
```

The `tls.crt` SAN is a DNS name only, so connecting by IP needs either the FQDN
(DNS resolves it to the VIP) or `tls.Config.ServerName` set to that SAN while
dialing the IP. koc uses the latter (dial `ipAddress`, verify against the cert's
SAN) so it works without depending on DNS.

## Retrieval recipe

1. Read kubeconfig (`--kubeconfig` → `$KUBECONFIG` → `~/.kube/config`; on a k0s
   controller `/var/lib/k0s/pki/admin.conf`), honoring `--kube-context`.
2. GET the `Ironic` in `<ns>` (`apis/ironic.metal3.io/v1alpha1/.../ironics`);
   require exactly one, read `spec.apiCredentialsName`, `spec.networking.{ipAddress,apiPort}`,
   `spec.tls.certificateName`.
3. GET the api secret → decode `username` / `password`.
4. GET the TLS secret → `ca.crt` (trust root) and `tls.crt` (→ SAN for ServerName).
5. Build endpoint `https://<ipAddress>:<apiPort>/` and a basic-auth `baremetal`
   client.

## Implementation approach: minimal REST (no new deps)

Chosen over `client-go` **because of the AGENTS.md air-gap invariant** — the
build must reproduce offline from committed `vendor/`. `client-go` would add tens
of MB and dozens of transitive modules; minimal REST adds **zero** dependencies:

- kubeconfig parsed with the already-vendored `gopkg.in/yaml.v3`
- HTTPS to the kube-apiserver via stdlib `net/http` + `crypto/tls`/`x509`
  (client-cert auth), mirroring the patterns in `internal/auth/tls.go`
- Ironic client built from gophercloud **core** types already vendored — no new
  gophercloud subpackage, so **no `make tidy` / network step** is required.

### Files

```
internal/kube/
  kubeconfig.go   # parse kubeconfig (yaml.v3): server URL, CA, client cert/key or token; context select
  client.go       # minimal REST client: GET {path} over TLS → JSON; GetSecret(ns,name); GetIronic(ns)
internal/auth/
  options.go      # + CredsFrom, Kubeconfig, KubeContext fields and --creds-from / --kubeconfig / --kube-context flags
  provider.go     # Authenticate(): if CredsFrom != "" short-circuit Keystone, load ironic creds, return Client{ironic: ...}
  credsfrom.go    # loadIronicCreds() via internal/kube; ironicCreds; basicAuthTransport; standalone baremetal client builder
  services.go     # Baremetal(): if c.ironic != nil build the standalone basic-auth client; other services error clearly
```

### Standalone client (gophercloud gotcha honored)

Per AGENTS.md, the ironic microversion header is emitted from `client.Type`, so
the hand-built client sets `Type: "baremetal"` and `Microversion`:

```go
pc := &gophercloud.ProviderClient{}                      // no TokenID → no X-Auth-Token
pc.HTTPClient = http.Client{Transport: rt}               // rt = debug( basicAuth( TLS transport ) )
sc := &gophercloud.ServiceClient{
    ProviderClient: pc,
    Endpoint:       endpoint,          // "https://<ip>:<port>/"  (trailing /)
    ResourceBase:   endpoint + "v1/",  // ironic v1
    Type:           "baremetal",       // → X-OpenStack-Ironic-API-Version
    Microversion:   o.BaremetalAPIVersion,
}
```

Basic auth is injected by a `RoundTripper` (`req.SetBasicAuth`); the `--debug`
transport wraps it on the **outside** so the `Authorization` header is never
logged. `--insecure` is honored for the ironic endpoint.

Only `baremetal` is supported under `--creds-from` (this namespace exposes only
Ironic); other service factories return a clear "only baremetal is supported with
--creds-from" error.

---

# Part 2 — `--creds-from-vault <kv-path>` (openrc from Vault)

## Goal (UX)

Read an openrc-style KV v2 secret from Vault and use it to authenticate the
normal Keystone flow — works for **all** services, not just baremetal:

```
koc --creds-from-vault deployments/itkey/e2e-lcm/llm-sl972-k0s-rc10/llm-sl972-k0s-rc10-cp/openrc server list
```

`--creds-from-vault <path>` is koc-specific. The KV path is passed explicitly on
the CLI (it is deployment/cluster-specific); the Vault connection parameters come
from standard `VAULT_*` env / flags (the deployment defaults, below).

## Deployment facts (verified on cluster `llm-sl972-k0s-rc10`)

External HashiCorp Vault, AppRole auth (`install_vault: false`). Source of truth
is the `k0s-system/lcm-config` ConfigMap (key `lcm-config.yaml`) plus two secrets:

| Item | Value / location |
|---|---|
| Address | `https://vault.itkey.com` (`vault_addr`) |
| Namespace | `""` (root) — but Vault **Enterprise**-capable → `X-Vault-Namespace` must be supported |
| Auth method | AppRole → `POST /v1/auth/approle/login` (validated end-to-end ✓) |
| role_id | `vault_app_role_id` (ConfigMap) — `2fe16fd7-…` |
| secret_id | k8s secret `cert-manager/vault-approle`, key `secret-id` |
| CA bundle | k8s secret `cert-manager/vault-ca`, key `ca.pem` (or file `vault_ca_bundle_file`) |
| KV v2 mount | `secret_v2` (`vault_kv_region_engine`) |
| Region prefix | `deployments/itkey/e2e-lcm/llm-sl972-k0s-rc10` (`vault_kv_region_prefix`) |
| openrc path | `<region-prefix>/<cluster>-cp/openrc` (CLI-supplied) → read at `/v1/secret_v2/data/<path>` |

**One default approle covers the KVP — no separate approle needed.** Verified
end-to-end (from a pod with Vault egress): login with the *default* LCM approle
(role_id `vault_app_role_id` + secret_id `cert-manager/vault-approle:secret-id`)
returns policies `['cert-manager-pki', 'default', 'e2e-lcm']`, and the **`e2e-lcm`**
policy grants read on the whole `deployments/itkey/e2e-lcm/…` region prefix,
including `…/openrc`. (The `keystack-ai` backend uses the same approle — role name
`keystack`, `VAULT_USERNAME`=role_id, `VAULT_PASSWORD`=secret_id — but that
component is **not part of the default deployment**; the canonical creds are the
two LCM defaults above.)

Reachability caveat: from the node **host**, direct egress to `vault.itkey.com`
is Istio-restricted/flaky (there is a `gitlab-egress-vault-istio` gateway); the
read succeeds from inside a workload with egress. koc is run against a reachable
Vault, so this is an operator-network concern, not a design one.

### openrc secret schema (verified)

The KV v2 secret at the openrc path is **not** a flat map of `OS_*` keys — it has
three fields:

| Field | Content |
|---|---|
| `value` | plaintext openrc shell (`export OS_AUTH_URL=…`, `OS_USERNAME`, `OS_PASSWORD`, `OS_PROJECT_NAME`, `OS_PROJECT_DOMAIN_NAME`, `OS_USER_DOMAIN_NAME`, `OS_REGION_NAME`, `OS_INTERFACE`, `OS_IDENTITY_API_VERSION`, …) |
| `openrc` | base64 of `value` |
| `clouds` | a `clouds.yaml` document (cloud name `kolla-admin`) |

koc maps by reading **`value`** (parse `export OS_*=…` → `auth.Options`) — simple,
no temp files. **`clouds`** is a structured alternative usable directly by
gophercloud's `clouds.Parse` (already a dependency) when a `--os-cloud` selector
is wanted. Recommendation: parse `value` for v1; support `clouds` via `--os-cloud`
if a secret carries multiple clouds.

## Vault connection flags (standard `VAULT_*` names)

| Flag | Env | Default |
|---|---|---|
| `--vault-addr` | `VAULT_ADDR` | — (deployment: `https://vault.itkey.com`) |
| `--vault-namespace` | `VAULT_NAMESPACE` | `""` (root); sent as `X-Vault-Namespace` when set |
| `--vault-token` | `VAULT_TOKEN` | — (if set, skip AppRole login) |
| `--vault-role-id` | `VAULT_ROLE_ID` | — |
| `--vault-secret-id` | `VAULT_SECRET_ID` | — |
| `--vault-approle-path` | `VAULT_APPROLE_PATH` | `approle` (login at `auth/<path>/login`) |
| `--vault-kv-mount` | `VAULT_KV_MOUNT` | `secret_v2` |
| `--vault-cacert` | `VAULT_CACERT` | system roots |

## Auth flow (minimal REST, no deps)

1. **Token** (Vault-CLI precedence): `--vault-token`/`VAULT_TOKEN`, else the cached
   `~/.vault-token` from `vault login` (`VAULT_TOKEN_FILE` overrides the path),
   unless an explicit AppRole was given. With no token, `POST
   {addr}/v1/auth/{approle-path}/login` with `{"role_id","secret_id"}`
   (+ `X-Vault-Namespace`) → `.auth.client_token`.
2. **Read**: `GET {addr}/v1/{mount}/data/{kv-path}` (+ `X-Vault-Token`,
   `X-Vault-Namespace`) → KV v2 returns `.data.data`, whose `value` field is the
   plaintext openrc (see schema above).
3. **Map** into `auth.Options`: parse the `value` field's `export OS_*=…` lines
   and apply each to the matching field (`OS_AUTH_URL`→AuthURL, `OS_USERNAME`,
   `OS_PASSWORD`, `OS_PROJECT_NAME`, `OS_USER_DOMAIN_NAME`,
   `OS_PROJECT_DOMAIN_NAME`, `OS_REGION_NAME`, `OS_INTERFACE`, …). Then the
   **existing** `Authenticate()` Keystone path runs unchanged — no service code
   is touched.

Parsing the openrc generically (any `OS_*` var) keeps it robust to the exact key
set; the `clouds` field is the structured fallback for multi-cloud secrets.

### Files (adds to Part 1's layout)

```
internal/vault/
  client.go     # minimal REST: approle login (or token) + KV v2 read; X-Vault-Namespace; TLS via CA
internal/auth/
  options.go    # + CredsFromVault + Vault* connection fields/flags/env
  provider.go   # Authenticate(): if CredsFromVault != "", read openrc from Vault, apply OS_* to o, continue Keystone
  credsfrom.go  # loadOpenrcFromVault() → map into Options
```

### Cluster auto-discovery of Vault config (IMPLEMENTED, automatic)

When `--vault-*` connection flags are absent, `--creds-from-vault` auto-discovers
the connection from the deployment using the same kubeconfig as `--creds-from-ns`:
ConfigMap `k0s-system/lcm-config` → `vault_addr` / `vault_namespace` /
`vault_app_role_id` / `vault_kv_region_engine` (mount) / `vault_kv_region_prefix`;
secret `cert-manager/vault-approle` key `secret-id` → secret_id. Explicit flags /
`VAULT_*` env always win (fill-if-unset). Discovery is attempted only when the
connection is incomplete (`vaultNeedsDiscovery`).

Vault TLS uses the **system roots** — the LCM Vault endpoint (`vault.itkey.com`)
presents a publicly-trusted certificate (verified: `curl` succeeds with system
roots, HTTP 200). Note `cert-manager/vault-ca` is the *PKI* CA, **not** the Vault
server's TLS CA, so it is deliberately not auto-loaded.

Result: on a cluster node, `koc --creds-from-vault <path>` runs with **zero**
Vault flags (verified end-to-end: AppRole login 200 → KV read 200 → openrc →
Keystone dial).

## Size assessment

**Not huge** — comparable to Part 1. One small REST client (login + KV read,
~120 lines), an `OS_*` mapper (~30 lines), flags/env wiring, and httptest tests
(fake Vault: `approle/login` + `kv/data/...`). **No new dependencies**, so the
air-gap build is unaffected. Recommend implementing alongside Part 1.

---

## AGENTS.md compliance checklist — IMPLEMENTED

Status: implemented on this branch (flags `--creds-from-ns` / `--creds-from-vault`).

- [x] **Air-gap / vendored**: no new modules; offline `-mod=vendor GOPROXY=off`
      build passes. No `vendor/` hand-edits.
- [x] **gophercloud v2**: core v2 only; standalone ironic client sets
      `Type: "baremetal"` + `Microversion` so `X-OpenStack-Ironic-API-Version` is
      emitted (asserted in a test).
- [x] **Tests via seam**: httptest tests added for `internal/kube` (secret decode,
      CR resolution incl. zero/multiple), `internal/vault` (AppRole login, token
      mode, namespace header, KV read, error path), and `internal/auth/credsfrom`
      (path resolution, openrc parse/map with flag precedence, basic-auth +
      microversion headers against a mock ironic). `go test ./...` green.
- [x] **Docs**: AGENTS.md Layout lists `internal/kube/`, `internal/vault/`,
      `credsfrom.go`, and a Conventions note documents both auth modes.
- [x] **Flags**: `--creds-from-ns` / `--creds-from-vault` are koc-specific
      extensions (not OSC parity); Vault flags use standard `VAULT_*` names.
- [x] **Gate**: `gofmt`/`go vet`/`golangci-lint` (0 issues)/`go test` green;
      offline static build succeeds.
- [x] **Real cluster E2E** (cluster `llm-sl972-k0s-rc10`):
      `koc --creds-from-ns lcm-ironic baremetal {node,conductor,driver} list`
      authenticated to the standalone ironic and returned live data; the
      non-baremetal guard fires as designed. `koc --creds-from-vault
      <rel-or-full-path> …` completed AppRole login → KV read → openrc → Keystone
      dial to the openrc's `OS_AUTH_URL` (final token blocked only by the
      deployment's mandatory kolla mTLS client cert, absent on the k0s node — an
      environment requirement, not a koc issue).

## Open questions

- If a namespace ever holds more than one `Ironic`, resolve by a name flag; for
  now require exactly one and error otherwise.
- Endpoint override: rely on `spec.networking.ipAddress` for now; add
  `--creds-from-endpoint` only if a deployment advertises an unreachable VIP.
- Vault KV path: the CLI arg is the path under the mount; the mount defaults to
  `secret_v2` (`--vault-kv-mount`). Decide whether to also accept a fully
  `mount/path` arg and split on the first segment.
- Vault Enterprise namespace: `--vault-namespace` sends `X-Vault-Namespace`;
  confirm whether the region KV lives in root or a child namespace per site.
