# Proposal: `koc vault kv` — KV v2 list/get, Vault→Vault copy, encrypted export

Status: implemented (list/get/copy/export/decrypt); `vault-helper.py --init` dropped by decision
Context: replaces `--copy-from` and `--junit-save` in `kolla-ansible-epoxy/.gitlab/kolla-ansible/e2e/scripts/vault-helper.py`

## Why

The KeyStack e2e pipeline copies a deployment's KV secrets from a source Vault
into the target Vault with `vault-helper.py --copy-from <env>`, which needs Python
plus `hvac` at runtime — exactly the dependency `koc` exists to remove for
air-gapped / FSTEC hosts.

Nothing upstream covers this:

- **python-openstackclient** has no Vault noun, so there is no surface to mirror.
  This group is koc-specific, like `--creds-from-vault` / `--creds-from-ns`.
- **The HashiCorp `vault` CLI has no `kv copy`** — only get/put/patch/list/
  metadata/…, so the manual alternative is `vault kv get -format=json | … |
  vault kv put -` per secret, juggling two sets of credentials in one shell.
- Community copy tools all build on the Vault Go SDK, which would break the
  vendored-deps / air-gap invariant in AGENTS.md.

koc already had the hard parts (`internal/vault`: AppRole login, KV v2 read,
Enterprise namespaces; `internal/auth/credsfrom.go`: `~/.vault-token` fallback and
LCM cluster auto-discovery), so only KV list/write, a second client and a command
group were missing.

## Mapping from `vault-helper.py`

| vault-helper.py | koc |
|---|---|
| `--copy-from <env>` (`copy_kv_secrets`) | `koc vault kv copy -r <src> <dst>` |
| `VAULT_SRC_ADDR` / `_TOKEN` / `_ENGINE` / `_PREFIX` | same env names, via `--src-vault-addr` / `-token` / `-kv-mount` / `-kv-prefix` |
| `vault_addr` / `VAULT_TOKEN` / `vault_engine` / `vault_prefix` (target) | the global `--vault-addr` / `--vault-token` / `--vault-kv-mount` / `--vault-kv-prefix` (or `--dst-vault-kv-prefix` for the prefix alone) |
| `hashicorp_vault_client()` token-xor-AppRole validation | `vault.New` (same rule) |
| `client.secrets.kv.v2.list_secrets` | `vault.Client.ListKV` (+ recursive `WalkKV`) |
| `client.secrets.kv.v2.read_secret_version` | `vault.Client.ReadKVDataAt` |
| `client.secrets.kv.v2.create_or_update_secret` | `vault.Client.WriteKVData` |
| `--init` (namespace, kv-v2, policy, PKI, AppRole, prefill) | **not ported** (decision) — privileged `sys/*` bootstrap, out of scope for koc |
| `--junit-save <file>` | `koc vault kv export <path> --recipient <pub.pem> -o <file>` — same report, payloads encrypted |
| (no equivalent) | `koc vault kv decrypt <file> -i <key.pem>` |

The pipeline invocation becomes, with the job's existing variables:

```sh
koc vault kv copy -r "$VAULT_SRC_PREFIX/$ENV" "$vault_prefix/$ENV"
```

### Bug fixed in passing

`copy_kv_secrets` lists **one level** and reads every returned key as a secret.
Vault returns subfolders as keys with a trailing `/`, so a nested path (e.g.
`secrets/deep/…`) makes the Python version fail on a read of a folder. `WalkKV`
descends instead, and tolerates a 404 on an empty subfolder mid-walk.

## Design notes

- **No Keystone.** The group builds only a `vault.Client` (via the exported
  `auth.Options.VaultConfig` / `VaultClient` seam), so it runs on a host with no
  cloud credentials. It is the only command group that never authenticates.
- **Destination = the global `--vault-*` flags**, so LCM auto-discovery (the
  `k0s-system/lcm-config` ConfigMap + `cert-manager/vault-approle` Secret) applies
  unchanged; **source = `--src-vault-*` overrides** that inherit the destination
  field by field, so a same-Vault copy needs no extra flags. Any explicit source
  credential replaces the destination's credentials as a group — otherwise an
  inherited token would silently win over an explicit source AppRole.
- **The KV prefix is the one destination field that also needs its own override.**
  The global `--vault-kv-prefix` is simultaneously the destination's prefix and the
  source's default, so setting it to name a destination prefix moves the source
  with it; and since it is usually auto-discovered, a copy to a *different* prefix
  had to restate the discovered value as `--src-vault-kv-prefix` (or spell the
  destination absolutely) to stand still. Hence `--dst-vault-kv-prefix`
  (`VAULT_DST_PREFIX`): both sides default to the global prefix, and each override
  moves only its own side. `copyPaths` is the seam that resolves the pair.
- **`-r` is opt-in**, like `cp`: a folder without `-r` is an error naming the fix,
  and `-r` on a leaf still copies that one secret.
- **Plan, then write.** The walk, the self-copy guard and the `--skip-existing`
  checks happen before any write, so `--dry-run` reports exactly the writes the
  real run performs. The source is read even under `--dry-run`, which makes the
  reported key count real and proves read access before the preview is trusted.
- **Self-copy guard**: same (addr, namespace, mount) plus an identical path, or a
  destination inside the source subtree, is refused. There is no `--force`: both
  are footguns, not use cases.
- **Secrets never printed.** `copy` reports paths and a key count only; the Vault
  transport logs just `method path -> status`, so `--debug` cannot leak secret
  data. `koc vault kv get` is the explicit, opt-in way to see values.
- **Data only.** `custom_metadata`, version history and `delete_version_after` are
  not copied — this is a copy of current values, not a replica. Stated in the
  command help and the README.
- Sequential and deterministic (sorted paths). No `--concurrency`: the e2e prefix
  is a handful of secrets, and ordering makes failures reproducible.

## The export is encrypted, by construction

`save_kv_content_as_junit` wrote every secret into `<system-out>` in cleartext.
That report is not private: `.gitlab-ci.yml` publishes `.junit/*.xml` both as
`artifacts:reports:junit` (line 167, 563) and as plain artifacts, and at one point
copied the whole `.junit` directory into `public/` — GitLab Pages (line 1037).
Anyone with project access could read every deployment credential.

koc's `export` therefore has **no plaintext mode**: `--recipient` is required, and
omitting it is an error rather than a fallback.

Scheme (standard library only — no new vendored module):

```
per secret:  AES-256-GCM(key=random 32B, nonce=random 12B, aad=<secret path>)
data key:    RSA-OAEP-SHA256 to the recipient's public key
envelope:    "koc-enc:v1:rsa-oaep-sha256:aes-256-gcm\n" + base64(
               uint16be len(wrapped) | wrapped | nonce | ciphertext+tag)
```

Design points:

- **Public-key, not passphrase.** CI holds only the public key, so a leaked
  runner, artifact or Pages copy yields nothing readable, and no decryption
  material has to exist as a CI variable. Recipients are RSA PEM (PKIX or PKCS#1)
  or a **certificate**, so a PKI-issued deployment cert works as-is; keys below
  2048 bits are refused.
- **Structure stays, values go.** One test case per secret path, so GitLab still
  renders the per-secret view (`skipped` for an empty secret, `failure` with the
  Vault error for an unreadable one). Paths are visible — that is what makes the
  report useful — but they are the GCM additional authenticated data, so a payload
  moved to a different test case fails to open instead of decrypting under the
  wrong name.
- **`ssl_certificates` keeps its per-key expansion** (`classname="vault.kv.ssl"`,
  `name="<path>:<key>"`), so each certificate is individually visible and
  individually encrypted, as in the Python version.
- **`decrypt` is read-only** and needs no Vault access; it renders Path/Key/Value
  rows through the output layer, so `-f json/yaml/csv` and `-c` work. Restoring
  into a Vault was deliberately left out — recovering a secret and re-injecting it
  should stay separate acts.
- Encrypted PEM identities are refused with the `openssl pkey` command to convert
  them, rather than reaching for the deprecated, insecure `x509.DecryptPEMBlock`.

## Verified

Against two local `vault server -dev` instances (Vault 1.20.2): recursive copy of
a nested tree within one Vault and across two Vaults (token on the destination,
AppRole on the source), cross-mount copy, `--dry-run` (zero writes),
`--skip-existing`, single-secret and `--src-version` copies, all four guard
errors, and `--debug` output containing no secret material. Namespaces
(`X-Vault-Namespace`) are Enterprise-only and are covered by unit tests rather
than the OSS dev server.

For the export: a real `openssl genrsa` 4096-bit recipient and a 3072-bit PKI
certificate recipient, over a subtree holding an openrc, an accounts secret, an
`ssl_certificates` secret with a real certificate and an empty value; the report
parses as JUnit XML, contains none of the seeded secret material, and decrypts
back to the exact values with the private key. Refusals confirmed: missing
`--recipient` (exit 1), wrong identity, public key given as `--identity` (and
private key as `--recipient`), and a payload moved to another test case.
