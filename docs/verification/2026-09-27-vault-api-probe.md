# Vault API probe (2026-09-27)

The reference `internal/vault/vaulttest` — koc's fake Vault server — is built
against. koc talks to Vault through a small slice of its HTTP API; a fake is only
worth having if it answers that slice the way a real server does, including the
unhelpful parts (an empty listing is a 404, a policy denial has a different body
from a missing token). This records what a real server said.

## How it was probed, and how to repeat it

Server: **OpenBao 2.7.0**, `bao server -dev` (release tarball
`openbao_2.7.0_linux_amd64.tar.gz`, SHA-256 verified against the release's
`checksums.txt`). OpenBao is the MPL-2.0 fork of Vault; the KV v2 and AppRole
APIs koc uses are Vault's. It was chosen over running Vault in CI because the
image is ~0.5 GB and the binary zip 176 MB — so no real server runs in CI at
all. The fake does, everywhere, and this probe is what keeps it honest.

The probe is not re-typed by hand: `TestContract` in
`internal/vault/vaulttest/contract_test.go` holds every case as a table and runs
it against the fake by default, or against a real dev server when told to:

```sh
bao server -dev -dev-root-token-id=root &     # or: vault server -dev -dev-root-token-id=root
VAULTTEST_ADDR=http://127.0.0.1:8200 go test ./internal/vault/vaulttest -run TestContract
```

It seeds the real server through its own API with the state the fake is seeded
with (a KV v1 mount, a `team` namespace, an AppRole with a read-only policy, a
read-only token). Use a throwaway dev server. Re-run it whenever koc starts using
another part of the Vault API, and extend the table first.

## Result

All 23 cases pass against OpenBao 2.7.0 **and** against the fake, unchanged.

| Case | Status | Body |
| --- | --- | --- |
| read latest / `?version=N` | 200 | `data.data` + `data.metadata` (`version`, `created_time`, `deletion_time: ""`) |
| read `?version=N` past the current version | 404 | `{"errors":[]}` |
| read a missing secret | 404 | `{"errors":[]}` |
| read a secret whose latest version is soft-deleted | **404** | still a body: `data.data: null`, `metadata.deletion_time` set |
| metadata of an existing / missing secret | 200 / 404 | `current_version`, `versions{}` / `{"errors":[]}` |
| LIST (verb or `GET ?list=true`, trailing `/` or not) | 200 | `data.keys`, folders with a trailing `/` |
| LIST of an empty or missing path | **404** | `{"errors":[]}` — not an empty 200 |
| LIST of a leaf secret | 404 | `{"errors":[]}` |
| write (POST or PUT `data/`) | 200 | the new version's metadata, `version` incremented |
| no token / unknown token | 403 | `{"errors":["permission denied"]}` |
| token without the capability | 403 | `{"errors":["1 error occurred:\n\t* permission denied\n\n"]}` |
| AppRole login, wrong role or secret ID | 400 | `{"errors":["invalid role or secret ID"]}` |
| AppRole login, no `role_id` | **500** | `{"errors":["missing role_id"]}` |
| AppRole login | 200 | `auth.client_token`, `token_type: service`, `lease_duration: 2764800` |
| unknown `X-Vault-Namespace` | 404 | `{"errors":["namespace not found"]}` |
| unknown mount | 404 | `{"errors":["no handler for route \"<path>\". route entry not found."]}` |
| KV v1 mount read through the v2 `data/` API | 404 | `{"errors":[]}` |

## What it says about koc

koc's client (`internal/vault`) already handles the surprising parts correctly:
it maps every 404 to `ErrNotFound`, so an empty or missing folder is "nothing
here" for `ListKV`/`WalkKV`, `HasKV` answers false, and folder keys keep the
trailing `/` that tells a subtree from a leaf.

One gap, since fixed: that same mapping threw the 404's body away, so a wrong
`--vault-kv-mount` ("no handler for route") or an unknown namespace ("namespace
not found") reported as a plain "not found", and a soft-deleted secret read as
missing. The error still wraps `ErrNotFound`, but now carries the reason
(`TestClient_404KeepsItsReason`).

## What neither the fake nor this probe covers

The fleet's actual Vault (HashiCorp, its policies, Enterprise namespaces), and
LCM auto-discovery of the Vault address and AppRole from Kubernetes
(`k0s-system/lcm-config`, `cert-manager/vault-approle`). Those stay on the
real-cloud verification path.
