# Proposal: `koc` as a cross-instance sync tool — NetBox, GitLab, Nexus

Status: **design proposal**, not implemented. Supersedes nothing; extends the
pattern `koc vault kv copy` established.
Context: `koc` already moves KV secrets from a source Vault to a destination
Vault (`docs/proposals/vault-kv.md`). The same air-gapped fleet needs the same
"copy this from the source instance to the destination instance" operation for
three more systems, and today each is a bespoke Python script or a manual UI
session.

## The ask, restated

| System | Selector | Recursion | Version skew to survive |
| --- | --- | --- | --- |
| Vault | KV path | subtree (`-r`) | — (done) |
| **NetBox** | device / config context | dependency closure + children | NetBox **4.x** across minors |
| **GitLab** | project(s), or everything | group subtree, all projects | **17 / 18 / 19**, src ≠ dst |
| **Nexus** | repository(-ies) | every component in the repo | Nexus Repository **3.x** across minors |

Everything below is one-way, additive, src → dst. Two-way reconciliation and
deletion are explicit non-goals (see "Non-goals").

## Why koc, and why not the obvious alternatives

The same argument that produced `koc vault kv copy` applies unchanged:

- **The vendor CLIs do not do this.** `vault` has no `kv copy`; `glab` has no
  cross-instance project sync; Nexus ships no CLI at all; NetBox's `netbox-cli`
  ecosystem is `pynetbox` scripts. Every existing answer is Python plus an SDK.
- **The target is air-gapped.** A node that can reach both instances typically
  cannot reach PyPI. One static binary is the whole deployment story, and
  `koc` is already on those nodes.
- **The SDKs are not vendorable here.** `go-netbox` is a generated OpenAPI
  client pinned to **one** NetBox minor — the exact thing this work must span —
  and adds six figures of generated code to `vendor/`. `go-gitlab` and the
  Nexus clients are smaller but still pull dependency graphs that the
  vendored/offline invariant in AGENTS.md would have to absorb. `internal/vault`,
  `internal/kube` and `internal/s3` are the precedent: hand-rolled, stdlib-only,
  a few hundred lines each, and they cover exactly the endpoints koc calls.

Estimated new dependency count for all three systems: **zero.** No `make tidy`,
no `vendor/` churn, `vendor-integrity` stays green by construction.

## Cross-cutting design

These decisions apply to all three and should be settled before the first line
of NetBox code, because retrofitting them across three command groups is the
expensive version of this work.

### 1. Command shape

Per-system groups, like `koc vault kv` and `koc s3` — not one `koc sync`
multiplexer. Each system has its own credentials, its own nouns and its own
read verbs, and cobra resolves `koc netbox device list` unambiguously where
`koc sync netbox device` reads as a verb parenting a noun.

```
koc netbox status | device list|show | config-context list|show | sync device|config-context
koc gitlab status | project list|show | group list          | sync project|group
koc nexus  status | repository list  | component list       | sync repository
```

`status` first, in every group: it is one authenticated call to each side, it
prints both versions and the compatibility verdict, and it is the command an
operator runs before a sync and a support engineer runs when a sync misbehaves.
It is also the cheapest possible end-to-end test of a new client.

The read verbs are not padding. A `sync` whose plan cannot be independently
listed is unauditable, and the list verbs are what the sync's own enumeration
code is tested through.

### 2. Source/destination flags: copy the Vault convention exactly

`internal/cli/vault/client.go` already solved this, and the solution should not
be re-invented per system:

- **The destination is the group's own `--<tool>-*` flags** (env-defaulted).
- **The source is `--src-<tool>-*` overrides that inherit the destination field
  by field**, so a same-instance operation needs no extra flags.
- **Credentials are replaced as a group** — if any source credential is given,
  none of the destination's are inherited. Otherwise an inherited token
  silently wins over an explicit source credential, which is a security bug,
  not an ergonomic one.
- "Explicitly set" means the flag was typed **or** its env var is non-empty, so
  a value can be deliberately cleared.

Env names follow what already exists in each ecosystem's tooling, so a CI job
that has variables set for `glab`/`pynetbox`/`curl` needs no rewiring:

| System | Destination env | Source env |
| --- | --- | --- |
| NetBox | `NETBOX_URL`, `NETBOX_TOKEN` | `NETBOX_SRC_URL`, `NETBOX_SRC_TOKEN` |
| GitLab | `GITLAB_URL`, `GITLAB_TOKEN` | `GITLAB_SRC_URL`, `GITLAB_SRC_TOKEN` |
| Nexus | `NEXUS_URL`, `NEXUS_USERNAME`, `NEXUS_PASSWORD` | `NEXUS_SRC_*` |

Group-persistent, not global: `koc --help` is already long, and no OpenStack
command needs any of these.

### 3. Credentials come from Vault, because they already can

koc reads KV v2 today and auto-discovers the LCM cluster's Vault. Each group
gets `--<tool>-creds-from-vault <path>`, reading a secret whose keys are the
env names above. That is ~20 lines per group over `auth.Options.VaultClient`,
and it removes the worst operational property of this whole feature: three more
long-lived admin tokens pasted into CI variables and shell history.

Precedent for the shape: `--s3-creds-from-ns` in `internal/cli/s3/client.go`.

### 4. Plan, then apply — with the same guarantees Vault's copy gives

Non-negotiable, and the reason `vault kv copy` is trusted:

- The **whole plan is built before the first write**, so `--dry-run` reports
  exactly the writes the real run performs.
- The source is **read even under `--dry-run`**, so the preview proves read
  access and reports real counts rather than optimistic ones.
- Ordering is **deterministic** (sorted, or topological where there are
  dependencies), so a failure is reproducible.
- **Self-sync guards**: same instance + same object, or a destination inside
  the source's own subtree, is refused with no `--force`.
- **Nothing is ever deleted.** No `--prune`, no `--mirror-delete`.
- Structured output through `internal/output` (`Source | Destination | … |
  Status`); the human summary goes to **stderr** so `-f json` stays pipeable.

### 5. Conflict policy: one flag, three systems

`vault kv copy` has `--skip-existing`, which is one bit where three systems need
three behaviours. Introduce the canonical spelling:

```
--on-conflict {skip|update|fail}      # default: skip
```

`update` means **PATCH only the fields that differ** (NetBox, GitLab) or
**re-upload only when the checksum differs** (Nexus) — never a blind overwrite,
so destination-local fields that the source has no opinion about survive.
`--skip-existing` stays as a hidden alias on `vault kv copy` for compatibility.

### 6. Error policy and concurrency: Vault's answer does not generalise

`vault kv copy` stops at the first error and runs sequentially. That is correct
for a handful of e2e secrets and wrong for a 10,000-artifact Nexus repository.

- `--continue-on-error` (default **off**; **on** for `nexus sync`, documented as
  the deviation it is) collects failures into the result table and still exits
  non-zero.
- `--concurrency N` (default 1; **4** for `nexus sync`) with results collected
  and **sorted before rendering**, so output is deterministic even when
  execution is not. Precedent: `--ne-concurrency` in
  `internal/cli/server/hypervisor.go`.

### 7. Version skew is handled in one place per system, never at call sites

This is the requirement that distinguishes this work from three CRUD scripts.
The rule mirrors `internal/cli/baremetal.explainMicroversion`, which turns an
ironic 404 caused by a microversion gate into a sentence naming the required
version:

- Every client exposes `ServerInfo(ctx) (Version, error)` read from the
  system's own endpoint, and both sides are probed **before planning**.
- Each system gets a single `capabilities.go` mapping feature → minimum
  version, consulted by the planner. No `if version >= x` scattered through
  request code.
- A direction the system genuinely cannot support is **refused before any
  write**, naming both versions (GitLab, below, is where this bites).
- A 404/400 that a version difference explains is re-rendered as
  "`<endpoint>` requires <tool> >= X; the destination reports Y", not surfaced
  raw.
- A capability the destination lacks produces a **skipped row with a reason**,
  not an aborted sync.

### 8. Transport: extract `internal/httpx` first

`internal/vault/transport.go`, `internal/kube/transport.go` and
`internal/s3/transport.go` are three copies of the same file, each carrying the
comment "the three copies exist because internal/auth imports this package …
Keep them in sync." Three more systems makes six copies of a file that decides,
among other things, **which headers are stripped when a redirect leaves the
origin host** — i.e. whether a redirect can harvest an admin token.

A leaf package importing nothing internal breaks the cycle that forced the
duplication. It owns: the `http.DefaultTransport` clone, TLS config assembly
(CA bundle, mTLS, `--insecure`, TLS 1.2 floor), the response-header timeout, the
same-host redirect policy with a **per-client credential-header list**, the
insecure/cleartext warnings, and the `--debug` redaction hook.

This is Phase 0 and it pays for itself the moment the second new client lands.

### 9. Secrets

GitLab CI/CD variables, Nexus repository credentials and NetBox secrets-plugin
data are secrets that a sync necessarily *carries*. The Vault rules extend
verbatim: values are never printed (keys and a masked marker only), the debug
transport logs `method path -> status` and never bodies for those endpoints, and
writing secret material to an endpoint reached over plain `http://` is refused
rather than warned about.

And the repo rule, which applies to every fixture and every commit message in
this work: **no real hostnames, tokens, tenant names or captured traffic** —
`example.com`, RFC 5737 addresses, synthetic IDs.

### 10. Testing and docs, per verb

Unchanged from AGENTS.md, with one addition: **a per-version fixture set**.
Each system gets `testdata/<version>/…` recorded from a real instance and
scrubbed, and one table test that drives the same sync against every version in
the set. That table is what makes "supports 4.x" and "supports 17/18/19" a
claim the build checks rather than a claim in a README.

Every commit that adds a leaf updates `docs/coverage.md` in the same commit —
all of these are **koc-native** rows, and the three arithmetic identities in
"Updating this document" must still hold afterwards.

## Phase 0 — foundations (do this first)

| # | Commit | What |
| --- | --- | --- |
| 0.1 | `refactor(httpx): extract the shared HTTP transport policy` | new `internal/httpx`; vault/kube/s3 adopt it; behaviour identical, their tests unchanged |
| 0.2 | `feat(sync): shared plan/apply vocabulary` | `internal/sync`: statuses, result rows, the dry-run/summary/table helper, `--dry-run`/`--on-conflict`/`--continue-on-error`/`--concurrency` flag set |
| 0.3 | `refactor(vault): move kv copy onto internal/sync` | proves 0.2 against the one existing user; `internal/cli/vault` tests must pass **unchanged** |

Deliberately **not** in Phase 0: a generic traversal/graph engine. Vault is a
flat namespace, NetBox is a dependency graph and Nexus is a paged content list;
there is no shared traversal until at least two of them exist. The rule of two
applies — extract after Phase 2, not before Phase 1.

## Phase 1 — NetBox (`koc netbox`)

### Scope

Devices and config contexts, as asked, plus what a device cannot exist without.

### The identity problem, which is the whole design

A NetBox object's `id` is an instance-local primary key. **A sync that carries
IDs across instances is wrong by construction.** Every object type gets a
natural key, in `naturalkey.go`:

| Type | Natural key |
| --- | --- |
| site, region, tenant, role, platform, manufacturer, cluster | `slug` |
| device type | (`manufacturer.slug`, `slug`) |
| device | (`name`, `site.slug`) with `serial`/`asset_tag` as the tiebreak |
| interface, inventory item | (`device`, `name`) |
| config context | `name` |
| tag, custom field | `slug` / `name` |

Resolution follows the existing `internal/cli/resolve` convention: exactly one
match wins, several is an error, zero is a create (or a refusal under
`--require-existing`).

### Prerequisites vs. children — two different recursions

`vault kv copy` has one `-r` because a KV subtree is one relationship. A device
has two:

- **Prerequisites** (site, role, device type, manufacturer, platform, tenant,
  rack, cluster) — the device *cannot be created without them*, so they are
  always resolved, and created when missing unless `--require-existing` says
  otherwise. Topologically ordered; a cycle is a bug, not a config.
- **Children** (interfaces, IP addresses, inventory items, device bays,
  console/power ports, `local_context_data`) — opt-in with `-r/--recursive`,
  because "copy this device" and "copy this device and its 96 interfaces" are
  different intentions with different blast radii.

This split is the one place this design deliberately departs from the Vault
precedent, and it should be stated in the command's help.

### Surviving 4.x skew without a per-version field table

Decode into `map[string]any`, not a struct pinned to one minor:

1. Strip **server-owned** keys: `id`, `url`, `display`, `display_url`, `created`,
   `last_updated`, `_depth`, `*_count`, `notes_url`.
2. Rewrite **relation** fields to the destination's identity — preferring
   NetBox's own nested natural-key form (`"site": {"slug": "…"}`), falling back
   to a resolved destination `id`.
3. **Pass unknown fields through untouched**, so a field that exists on both
   ends survives even if koc has never heard of it.
4. When the destination rejects a field it does not have, `--drop-unknown-fields`
   consults the destination's **OpenAPI schema at `/api/schema/`** and removes
   exactly the fields its serializer does not declare — data-driven, so a new
   NetBox minor needs no koc release.

Version reporting: the `API-Version` response header (major.minor) plus
`/api/status/`'s `netbox-version` (full string, may carry build suffixes such as
`4.2.3-Docker-3.2.0` — parse defensively). Note the known trap that the
`API-Version` header is absent unless the request sets `Content-Type:
application/json`; koc must set it and must not treat a missing header as a
missing NetBox.

**To verify before coding** (fixtures, not assumptions): the exact 4.x versions
deployed in the fleet; whether nested natural-key writes are accepted on all of
them; the token header form (`Authorization: Token <key>` on 4.0–4.4 vs. the
newer `Bearer nbt_<key>.<token>` v2 tokens — auto-detect from the prefix);
and whether `/api/extras/object-types/` vs `/api/core/object-types/` is on any
code path koc touches (it moved in 4.4, removed in 4.5).

### Custom fields and tags

- **Tags** are created on demand by slug — cheap, obviously safe.
- **Custom field definitions** are a *schema* change to the destination, not
  data. Off by default; `--with-custom-fields` opts in; otherwise a device
  carrying custom-field values whose definitions are missing is a named refusal.
- **Config context assignments** (regions, sites, roles, platforms, tenants,
  cluster groups, tags …) are resolved by natural key and a missing target is
  refused, never silently dropped — a context that applies to fewer objects than
  intended is the failure mode nobody notices.

### Commands and rough cost

| Commit | Leaves | Size |
| --- | --- | --- |
| `feat(netbox): minimal NetBox REST client and "koc netbox status"` | 1 | M |
| `feat(netbox): list and show devices and config contexts` | 4 | M |
| `feat(netbox): sync devices with their prerequisites` | 1 | L |
| `feat(netbox): sync config contexts and device children` | 1 | M |

## Phase 2 — Nexus (`koc nexus`)

### Scope

Component/asset content of **hosted** repositories, by format. Repository
*configuration* sync and blob-store/cleanup policy are platform concerns and are
out of scope (a later `koc nexus repository create --from-src` is the natural
follow-up).

### Formats

In scope for Phase 2: `raw`, `maven2`, `yum`, `apt`, `pypi`, `npm`, `helm` —
everything the components API can upload with format-specific fields.

**Out of scope, deliberately: `docker`.** Sonatype's own guidance is that Docker
is not uploadable through the components API; a Docker repository has to be
driven through the Registry v2 API (blobs, manifests, tags), which is a
different client and arguably a different feature (`koc oci copy`). Pretending
otherwise produces a sync that silently skips the repositories operators care
most about.

### Mechanics

- **Enumerate**: `GET /service/rest/v1/components?repository=<r>`, paged by
  `continuationToken` until it comes back null. (Default page size became 50 in
  3.74; do not assume it.)
- **Compare by checksum** — every asset carries `sha1`/`sha256`/`md5`. Equal
  checksum → `skip`, and that is what makes a re-run of a large repository cheap
  and makes `--on-conflict update` mean something precise.
- **Transfer streams.** `GET <downloadUrl>` piped through `io.Pipe` +
  `multipart.Writer` into `POST /service/rest/v1/components?repository=<dst>`.
  Nexus authenticates with basic auth, so unlike S3's SigV4 there is no payload
  hash forcing an `io.ReadSeeker` and no spooling. **Verify** whether Nexus
  requires `Content-Length` on that multipart POST; if it does, fall back to a
  temp file under `--spool-dir` and say so in the help.
- **Refuse a non-hosted destination** (proxy/group), read from the repositories
  API's `type`, with the reason.
- Version skew: the `Server: Nexus/3.x.y` response header, with
  `/service/rest/v1/status` as the liveness probe. Two known skew points to
  encode in `capabilities.go`: pagination defaults changed in **3.74**, and
  **3.88** replaced Elasticsearch with SQL search, changing wildcard behaviour —
  which is a reason to enumerate via `components` rather than `search` wherever
  possible.

### Commands and rough cost

| Commit | Leaves | Size |
| --- | --- | --- |
| `feat(nexus): minimal Nexus REST client, status and repository list` | 2 | M |
| `feat(nexus): list components and assets` | 2 | S |
| `feat(nexus): sync a hosted repository's components` | 1 | L |

## Phase 3 — GitLab (`koc gitlab`)

The largest, the riskiest, and the one whose scope must be decided before
estimating. The version requirement in the ask — "different 17/18/19 src
target" — is not a detail; it eliminates one of the three candidate mechanisms
outright.

### The four ways to move a GitLab project, and what each costs

| Mechanism | What moves | Version rule | Verdict |
| --- | --- | --- | --- |
| **REST API v4, object by object** | project + settings, members, CI/CD variables, protected branches/tags, labels, milestones, hooks, environments, deploy keys, badges, approval rules (EE) | v4 is stable across 17→19; deprecated attributes are removed at majors; feature-detect per endpoint | **koc's lane** |
| Project export/import (`POST /projects/:id/export`, `POST /projects/import`) | repo, issues, MRs, wiki, snippets, as a tarball | target must be **the same or newer**, and imports are supported only from up to **two minor versions** behind | fails exactly the 17→19 and 19→17 cases the ask names |
| Direct transfer (`POST /bulk_imports`) | groups and projects | source must be **no more than two minors behind** the destination; project-level migration only became GA in **18.3** | destination must reach the source over the network; unavailable on a 17 destination |
| `git` itself | repository content | none | koc has no git implementation, and vendoring one contradicts the whole premise |

### Recommended scope

**`koc gitlab sync` synchronises project configuration and metadata over API
v4, and does not move repository content.** Instead it offers the honest
handoff:

- `koc gitlab status` prints both versions **and the verdict**: whether
  export/import or direct transfer is even legal in this direction, with the
  rule quoted. Today that question is answered by trying it and reading a
  failure hours later.
- `--set-push-mirror` configures GitLab's own push mirroring
  (`POST /projects/:id/remote_mirrors`) on the source project, so the bytes move
  over git between the two GitLabs — koc configures the pipe rather than
  becoming it.
- Repository content otherwise stays a documented two-line
  `git clone --mirror` / `git push --mirror`.

This keeps koc doing what it is good at (auth, enumeration, planning, idempotent
writes, deterministic reporting) and out of the business of reimplementing git
or shuffling multi-gigabyte tarballs it adds nothing to.

The alternative scope — koc orchestrating export/import or `bulk_imports` — is
implementable as a **later** phase and is genuinely useful as a *version-aware
wrapper*, but it cannot satisfy "17/18/19 in any direction" and should not be
sold as if it could. See "Decisions needed".

### Sub-resource families, selectable

`--include` / `--exclude` over: `settings`, `members`, `variables`, `branches`
(protected), `tags` (protected), `labels`, `milestones`, `hooks`,
`environments`, `deploy-keys`, `badges`, `approvals`. Default: everything except
`hooks` and `deploy-keys`, which point at infrastructure that is usually
environment-specific and whose blind copy creates callbacks from the wrong
instance.

### Selecting projects

- `koc gitlab sync project <path-with-namespace>…`
- `koc gitlab sync group <path> -r` — every project beneath a group
- `koc gitlab sync group --all` — the "sync all" case, which for any real
  instance means the plan is thousands of rows: `--dry-run` output must be
  usable at that size (it is a table through `internal/output`, so `-f csv`
  handles it), and `--concurrency` and `--continue-on-error` matter here as much
  as in Nexus.

Missing destination namespaces follow the NetBox prerequisite rule: created
unless `--require-existing`.

### Secrets

CI/CD variables are the payload most worth having and the one koc must handle
most carefully: keys and a masked marker in output, never values; no writing
them to a plain-HTTP destination; `--debug` redaction on those endpoints. A
`--variables=names-only` mode is worth having for an audit run.

### Version handling

`GET /api/v4/version` on both ends; `capabilities.go` maps each sub-resource to
its minimum version and to CE/EE. An EE-only endpoint against a CE destination
is a **skipped row with a reason**, not a 403 traceback — the single most common
way a cross-instance script fails today.

### Commands and rough cost

| Commit | Leaves | Size |
| --- | --- | --- |
| `feat(gitlab): minimal GitLab API v4 client, status and version verdict` | 1 | M |
| `feat(gitlab): list projects and groups` | 3 | S |
| `feat(gitlab): sync project settings and metadata` | 1 | L |
| `feat(gitlab): sync the sub-resource families` | — | L |
| `feat(gitlab): sync a whole group, and --set-push-mirror` | 1 | M |

## Sequencing and why

Phase 0 → **NetBox** → **Nexus** → **GitLab**, matching the order asked for, and
it happens to be the right capability ramp:

1. NetBox is pure JSON at small volume — it exercises planning, dependency
   ordering and version-skew handling without binary streaming.
2. Nexus adds streaming, checksum comparison, paging at scale, concurrency and
   continue-on-error. After it, `internal/sync` has three real users and the
   traversal engine can be extracted from evidence rather than guessed.
3. GitLab is last because it is the largest and because its scope decision is
   the one still open.

Rough sizing, in the repo's own commit granularity: **Phase 0 ≈ 3 commits,
NetBox ≈ 4, Nexus ≈ 3, GitLab ≈ 5**, plus a `docs:` refresh per phase. Every
`feat:` commit carries its `docs/coverage.md` row.

## Risks

| Risk | Mitigation |
| --- | --- |
| Scope creep into "koc replaces git / koc replaces the migration tooling" | the GitLab scope decision, taken explicitly and written into the command help |
| Version-matrix maintenance cost | per-version `testdata/`, one table test per system; a version only counts as supported if it has a fixture set |
| Binary size — three clients plus three command groups | `make size` already records it in CI; budget and check it per phase |
| `gocognit` 25 on planner/apply functions | plan for small named functions from the start; the Vault copy's `copySession.copyOne` split is the model |
| A destination write path that a dry-run did not predict | plan-before-write is a hard rule, tested per system |
| Secret leakage through output, `--debug` or fixtures | redaction on the secret-carrying endpoints; masked rendering; the private-data rule applied to every fixture |
| Nexus large-repo runtime vs. the 60s response-header timeout | streaming plus per-asset progress; `--timeout` defaults to unbounded, keep it that way for sync |

## Non-goals

- Two-way / bidirectional reconciliation.
- Deletion of destination objects (`--prune`) — additive only.
- A replacement for each system's own backup and restore.
- Docker/OCI content (Nexus) and git repository content (GitLab), both named
  above with the reason.
- Repository/instance *configuration* management (Nexus blob stores, GitLab
  instance settings, NetBox plugins).

## Decisions needed before Phase 3 (and two before Phase 1)

1. **GitLab scope** — configuration + metadata over API v4 (recommended), or
   also orchestrating export/import / direct transfer with their version rules
   enforced? This changes Phase 3 substantially.
2. **Is the destination always at or ahead of the source's version?** If yes,
   the export/import path becomes viable as an add-on; if 19 → 17 must work, it
   is off the table permanently and should be documented as such.
3. **Nexus formats actually in use**, and whether Docker repositories are a
   requirement — if they are, `koc oci copy` is a fourth phase, not a flag.
4. **NetBox object scope beyond devices and config contexts** — virtual
   machines, IPAM prefixes/IPs, circuits? The engine is the same; the natural-key
   and prerequisite tables grow per type.
5. **Where these run** — CI job, operator workstation, or air-gapped cluster
   node? It decides how much of §3 (credentials from Vault, auto-discovery)
   is Phase 0 work rather than a later convenience.
6. **Is additive-only sufficient long term**, or is a reconcile/prune mode
   eventually needed? It does not change Phase 1–3, but it changes whether the
   plan structure should carry a "would delete" row type from the start.
