# Closing the coverage gap: Tier 1 / 2 / 3 implementation plan

Companion to `docs/coverage.md`. That file measures the gap; this one says in
what order to close it, what each step costs, and what "done" means for each.

**Baseline for every number here:** 403 leaf commands, 374 upstream-equivalent
of 844 in-scope (44%), as of the commit that added this file. **Tier 1 has since
shipped in full** — 447 leaf commands, 418/844 (50%). Tier 2 is next.

## Scope

Out of scope, and staying out:

| Excluded | Why |
| --- | --- |
| Swift (`object store`, 17 cmds) | not deployed in the target clouds |
| Manila (`share`, 40 cmds) | not deployed in the target clouds |
| `identity.v2`, `volume.v2`, `image.v1` | legacy API versions, already excluded from the denominator |

Both are already out of the 844 denominator.

## The constraint that shapes everything below: Zed

**`koc` targets OpenStack Zed (2022.2) and newer.** The fleet includes old
clouds, so "works on the newest release" is not done. Per-service caps, read
from the Zed sdists on PyPI:

| Service | Zed version | Max microversion |
| --- | --- | --- |
| ironic | 21.4.0 | **1.82** |
| nova | 26.x | **2.93** |
| cinder | 21.3.2 | **3.70** |
| placement | 9.0.0 | **1.39** |
| keystone, glance, neutron, designate, octavia | — | no microversions; capability is per-extension |

Three consequences for this plan:

1. **ironic-inspector stays.** Ironic removed the `inspector` inspect interface
   in 33.0.0 (2026.1) and moved inspection rules into its own API at 1.96, so on
   a *new* cloud `baremetal node inspect` + `baremetal node inventory show`
   replace it. On Zed the inspector is the only in-band inspection there is.
   `koc baremetal introspection …` is not going anywhere, and
   `python-ironic-inspector-client` is a real baseline in `docs/coverage.md`
   (6/13) rather than a deprecation note.
2. **Some Tier 1/3 commands are post-Zed** and must say so instead of returning a
   confusing 404: `node unhold` (1.85), `node firmware list` (1.86), `node
   service` (1.87), `node children list` (1.83), runbooks (1.92), inspection
   rules (1.96). They ship, guarded.
3. **Every new command needs its gating microversion looked up**, from
   `ironic/api/controllers/v1/versions.py` in the sdist (or the service's
   equivalent). If it is above the cap in the table, wrap the call in the guard
   and note the requirement in the command's `Short`.

### The guard

`internal/cli/baremetal/microversion.go` — `explainMicroversion` turns the
404/406 a version-gated endpoint returns on an older cloud into a real message:

```
baremetal node firmware list requires ironic API 1.86 (OpenStack 2023.2);
this cloud supports up to 1.82
```

It re-reads the endpoint's version document (`GET /v1/`) only on the error path,
so the happy path costs nothing. `koc`'s `--os-*-api-version` defaults are
`latest`, which negotiates downward on its own — the guard exists for the case
negotiation cannot fix, where the endpoint simply is not there.

## How the work is sequenced

Three tiers, ordered by **cost of the first commit in each**, not by value:

- **Tier 1** — the gophercloud package is already in `vendor/`. No `make tidy`,
  no network, no vendor churn. Pure wiring.
- **Tier 2** — the package exists upstream at the pinned v2.13.0 but is not
  vendored. Each batch costs one `make tidy` (needs network once) plus the
  wiring. Group batches so `vendor/` moves as few times as possible.
- **Tier 3** — no gophercloud package exists. Needs a raw `ServiceClient`
  fallback, isolated behind a helper, per AGENTS.md.

Within a tier, ordered by how often an operator actually runs the command.

### Definition of done for every batch

Non-negotiable, per AGENTS.md — a batch is one `feat:` commit containing:

1. Command files following `internal/cli/baremetal/node.go`: a
   `newXxxCommand(a, o)` constructor and a `runXxx(ctx, client, o, …, w)` seam.
2. Tests against the seam via `httptest` + gophercloud fixtures, asserting
   method, URL, microversion header, request body and rendered output — the
   primary list plus at least one write verb per noun.
3. `docs/coverage.md` updated **in the same commit**: per-service row (raw and
   core), headline, snapshot line, and the gap tier the command was listed
   under.
4. Gates green: `gofmt`, `go vet`, `golangci-lint` 0 issues, `go test ./...`,
   and the offline static build (`GOFLAGS=-mod=vendor GOPROXY=off`).
5. Flags diffed against the upstream parser, not just the command name — see
   `docs/coverage.md` on why presence ≠ parity. New flag surfaces carry the
   **UNVERIFIED against KeyStack** doc comment.

---

## Tier 1 — no vendor change (≈45 commands) — **DONE**

Every package below was already in `vendor/`; the capability was verified by
reading the vendored `requests.go`, not assumed. All six batches shipped;
44 commands landed and one was dropped (see T1.6).

### T1.1 — ironic provision-state verbs (6 commands)

`baremetal node adopt` · `clean` · `rescue` · `unrescue` · `service` · `unhold`

`nodes.ChangeProvisionState` with a different `Target`, plus per-verb payloads:
`clean` takes `--clean-steps` (JSON file or inline), `service` takes
`--service-steps`, `rescue` takes `--rescue-password`. The `--wait` polling
machinery already exists in `internal/cli/baremetal/node_provision.go`; reuse it
unchanged. **Highest value in Tier 1** — `clean` and `rescue` are daily ops and
the file already has everything but the verb table entries.

### T1.2 — ironic node read-outs (8 commands)

| Command | Vendored call |
| --- | --- |
| `baremetal node validate` | `nodes.Validate` |
| `baremetal node vif list` / `attach` / `detach` | `nodes.ListVirtualInterfaces` / `AttachVirtualInterface` / `DetachVirtualInterface` |
| `baremetal node bios setting list` / `show` | `nodes.ListBIOSSettings` / `GetBIOSSetting` |
| `baremetal node firmware list` | `nodes.ListFirmware` |
| `baremetal node inject nmi` | `nodes.InjectNMI` |

`vif attach/detach` is the one that unblocks provisioning workflows; the rest
are diagnostics.

### T1.3 — ironic driver and conductor detail (3 commands)

`baremetal driver property list` (`drivers.GetDriverProperties`) ·
`baremetal driver raid property list` (`drivers.GetDriverDiskProperties`) ·
`baremetal conductor show` (`conductors.Get`)

Small, and it completes the two nouns `koc` already lists.

### T1.4 — identity writes (15 commands)

| Command | Vendored call |
| --- | --- |
| `role create` / `delete` / `set` | `roles.Create` / `Delete` / `Update` |
| `implied role create` / `delete` / `list` | `roles.CreateRoleInferenceRule` / `DeleteRoleInferenceRule` / `ListRoleInferenceRules` |
| `region create` / `delete` / `set` / `show` | `regions.*` (full CRUD vendored; `koc` only wires `list`) |
| `service create` / `delete` / `set` | `services.*` |
| `user password set` | `users.ChangePassword` |
| `token revoke` | `tokens.Revoke` |

The largest single Tier 1 batch and the one that turns `koc` from read-mostly to
usable for Keystone administration. Split into two commits if the diff gets
unwieldy: roles+implied roles, then regions/services/users/tokens.

### T1.5 — compute server state verbs (5 commands)

`server shelve` · `server unshelve` · `server rescue` · `server unrescue` ·
`server image create`

`servers.Shelve` / `Unshelve` / `Rescue` / `Unrescue` / `CreateImage`, all
vendored. `server image create --wait` should poll glance for `active` the way
`node_provision.go` polls ironic — reuse the polling shape, not the code.

### T1.6 — volume and network metadata updates (7 commands, 1 dropped)

`volume snapshot set` / `unset` · `volume backup set` / `unset`
(`snapshots.Update` / `backups.Update`) ·
`subnet unset` (list-entry removal, exactly like the shipped `port unset`) ·
`router add gateway` / `remove gateway` (`routers.Update` with `GatewayInfo`).

Mechanical: each mirrors a `set`/`unset` pair `koc` already ships for a sibling
noun.

**Tier 1 total: 44 commands → 418/844 (50%).** Landed. `security group unset`
was dropped rather than implemented: upstream's `UnsetSecurityGroup` takes only
`--tag`/`--all-tag`, so the nil-update this plan assumed would have shipped a
command sharing upstream's name with different behaviour. It returns with
security-group tag support.

Two gophercloud bugs surfaced on the way, both fixed behind a local helper and
pinned by a test — `nodes.GetBIOSSetting` decodes a key ironic never sends, and
`backups.Update` builds its body with an empty parent so cinder's
`body['backup']` raises. Both are the failure mode AGENTS.md warns about: a
fixture encoding a payload the service never produces.

---

## Tier 2 — one `make tidy` per batch (≈100 commands)

All packages confirmed present in the gophercloud v2.13.0 module zip. Land the
batches back-to-back so `vendor/` churns once per batch and never mid-review.

| Batch | New vendored package(s) | Commands | Count |
| --- | --- | --- | --- |
| **T2.1** | `compute/v2/servergroups` | `server group create/delete/list/show` | 4 |
| **T2.2** | `compute/v2/attachinterfaces` | `server add/remove port`, `… network`, `… fixed ip` | 6 |
| **T2.3** | `compute/v2/availabilityzones` + `blockstorage/v3/availabilityzones` + `compute/v2/limits` + `blockstorage/v3/limits` | `availability zone list`, `limits show` | 2 |
| **T2.4** | `compute/v2/usage` | `usage list/show` | 2 |
| **T2.5** | `baremetal/v1/allocations` | `baremetal allocation create/delete/list/set/show/unset` | 6 |
| **T2.6** | `blockstorage/v3/transfers` | `volume transfer request accept/create/delete/list/show` | 5 |
| **T2.7** | `blockstorage/v3/qos` | `volume qos associate/create/delete/disassociate/list/set/show/unset` | 8 |
| **T2.8** | `image/v2/tasks` (+ `imagedata` stage) | `image task list/show`, `image stage`, `image stores list` | 4 |
| **T2.9** | `placement/v1/{resourceclasses,usages,allocationcandidates}` | the 21 missing osc-placement commands → **31/31** | 21 |
| **T2.10** | `networking/v2/extensions/{layer3/addressscopes,security/addressgroups,layer3/portforwarding,layer3/extraroutes,networkipavailabilities,rbacpolicies,segments,qos/*}` | address scopes (5), address groups (6), FIP port forwarding (5), `router add/remove route` (2), `ip availability list/show` (2), network RBAC (5), network segments (5), network QoS (12) | 42 |

Notes that will bite if ignored:

- **T2.3 spans two services.** `availability zone list` and `limits show` live in
  `openstack.common` upstream and merge nova + cinder (+ neutron for AZs). Follow
  `internal/cli/quota` — the existing cross-service noun — rather than inventing
  a second pattern.
- **T2.9 finishes a plugin.** osc-placement goes 10/31 → 31/31, matching what was
  done for designate. Worth doing in one pass while the API is in your head.
- **T2.10 is not one commit.** Split it per extension (8 commits); network QoS
  alone is 12 commands across three nouns. It is also the batch most likely to
  hit extensions the cloud does not enable — §2 of the verification prompt tells
  you which ones exist before you write any of it.
- Re-run `make tidy` **once per batch**, never per file, and never hand-edit
  `vendor/`.

**Tier 2 total: ≈100 commands → ≈519/844 (61%).**

---

## Tier 3 — raw `ServiceClient` fallback

No gophercloud package exists. AGENTS.md rule applies: isolate behind a small
helper, pin the microversion, comment why the typed package is unavailable.

### First: extract the raw helper

`internal/cli/dns/raw.go` already contains the pattern — JSON GET/POST/PATCH/
DELETE plus a `links.next` page walk standing in for the missing Pager — and it
carries 30 of the 60 designate commands. Tier 3 needs the same thing for ironic,
glance and neutron.

**Do this first, as a `refactor:` commit:** lift the transport half of
`dns/raw.go` into `internal/cli/rawapi`, leaving designate's `--all-projects`
header shim behind in the dns package. Then every Tier 3 batch below is a thin
DTO file plus verbs, not a fourth copy of the same HTTP code. Skipping this step
is how you end up with four subtly different page-walkers.

### T3.1 — inspection rules, both dialects (11 commands)

`baremetal inspection rule create/delete/list/set/show/unset` over ironic's
`/v1/inspection_rules`, **API ≥ 1.96** — plus the five inspector-side verbs `koc`
still lacks (`baremetal introspection rule delete/import/list/purge/show`) over
the inspector's `/v1/rules`, which is what a Zed cloud actually has.

The pair is the clearest illustration of the Zed constraint: same operator task,
two endpoints, split by release. Ship both, guard the ironic one with
`explainMicroversion` so a Zed cloud is told to use the inspector spelling
instead of getting a bare 406, and pin the microversion by shallow-copying the
service client (see `server/actions.go serverActionRaw`).

Also worth folding in here: `baremetal introspection interface show` and
`introspection reprocess`, the last two inspector commands missing from the 6/13
row in `docs/coverage.md`. Both are small and finish that baseline.

### T3.2 — ironic inventory nouns (24 commands)

`baremetal chassis *` (6) · `baremetal port group *` (6) ·
`baremetal volume connector *` (6) · `baremetal volume target *` (6)

Four near-identical CRUD nouns over `/v1/chassis`, `/v1/portgroups`,
`/v1/volume/connectors`, `/v1/volume/targets`. Once the first is written the
other three are a table.

### T3.3 — ironic node extras (10 commands)

`node trait add/list/remove` (3) · `node history list/get` (2) ·
`node console enable/disable/show` (3) · `node children list` (1) ·
`baremetal shard list` (1)

Plus `node secure boot on/off`, `node boot mode set` and `node passthru
list/call` if the cloud's hardware types support them — check §2 first, these are
the most likely to be unimplemented on a given driver.

### T3.4 — ironic templates (15 commands)

`baremetal deploy template *` (6, API 1.55 — present on Zed) ·
`baremetal runbook *` (9, including the three `trait` verbs; **API 1.92, so
2025.2 and newer only** — guard it). Lower priority: both are opt-in features
many clouds never enable, and runbooks exist on none of the older fleet.

### T3.5 — the rest, in descending value

Glance metadefs and cached images; Cinder consistency groups, volume groups,
`block storage cluster/log level/manageable`; Neutron metering, flavors, L3
conntrack helpers, local IPs, NDP proxies, segment ranges, default SG rules;
Octavia availability zones and profiles, the seven remaining `unset` verbs and
`listener stats show`.

Nothing here is daily-ops. Pull items forward only when the cloud actually uses
the subsystem — §2 of the verification prompt is what tells you.

---

## Projected coverage

| After | Leaf commands | Upstream-equivalent | of 844 |
| --- | --- | --- | --- |
| today | 403 | 374 | 44% |
| Tier 1 | ≈448 | ≈419 | 50% |
| Tier 2 | ≈548 | ≈519 | 61% |
| Tier 3 (T3.1–T3.4) | ≈603 | ≈574 | 68% |

Per-service, Tier 1 + Tier 2 + T3.1–T3.4 takes ironic from 29/118 (25%) to
≈95/118 (81%), placement to 31/31, and compute/identity/volume each past 80% of
their core surface. The tail after that is the niche 45% the headline section of
`docs/coverage.md` already discounts.

These are projections from counted command lists, not estimates — but they
assume every command lands, and flag parity is a separate axis that moves none
of these numbers.

## Verifying against the fleet

`docs/verification/2026-08-08-tier-plan-probe.md` is the prompt for a session
with cloud access. It settles the question this plan cannot answer offline —
which of these APIs each cloud actually exposes — and because the fleet spans
Zed to current, **run it once per distinct release**, not once. Its output is a
per-cloud capability matrix: the batches every cloud can take, and the ones that
ship guarded because the oldest cloud cannot.
