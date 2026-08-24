# History parity — close the gap against commands actually used

Derived from `~/.zsh_history` (15 295 entries) on 2026-08-06: every invocation of
`openstack`, `koc`, `cinder`, `nova`, `baremetal` (standalone ironic CLI),
`designate` was extracted, flags stripped, and the noun/verb chain matched
against `koc`'s 245-leaf command tree (`koc` @ `cdc382e`).

**Result: 1 319 invocations, 123 distinct commands already covered (1 168
invocations, 89%).** The remaining 11% is 20 missing commands / aliases plus a
long tail of flag gaps on commands that *do* exist. This document is the work
list to reach ~100% coverage of the surface this operator actually uses.

Phases 6 and 7 go beyond the history diff: **Octavia (`loadbalancer`) and the
full Designate (`dns`) surface are in scope** by decision, not by usage count —
history shows only `loadbalancer list` (1) and `designate pool update` (2), but
both services are now first-class targets rather than deferred.

Two things the raw diff surfaced that are not in `docs/coverage.md`:

- **Flag gaps outweigh command gaps.** `volume list --project` (26 uses),
  `port set --allowed-address` (8), `volume create --backup` (8) are all
  "command present, flag absent". Presence in the coverage table hides these.
- **`python-openstackclient` accepts unique flag prefixes** (argparse
  `allow_abbrev`), so muscle memory is full of `--all` (= `--all-projects`,
  30 uses) and `--fit` (= `--fit-width`, 21 uses). `pflag` does not abbreviate,
  so those invocations fail against `koc` today. See Phase 5.

Baselines for anything marked UNVERIFIED: upstream OSC 10.2.1,
python-ironicclient 6.2.0, python-designateclient 7.0.0 and
python-octaviaclient (version TBD — a new baseline, see Phase 6) parsers, plus the
local docs clone at `~/code/project_k/keystack_documentation`. Flag and command
names for Phases 6–7 must be read off those `entry_points.txt` / argparse sources,
not from memory.

---

## Phase 1 — missing commands, already vendored (no `make tidy`)

Every package below is already in `vendor/`; these are pure wiring. Ordered by
observed usage.

| # | Command | Uses | Package / call | Notes |
| --- | --- | --- | --- | --- |
| 1.1 | `baremetal driver show <driver>` | 16 | `baremetal/v1/drivers.Get` | Highest-usage single gap. `koc baremetal driver list` already exists — same client, one file. Show `hosts`, `type`, `default_*_interface`, `enabled_*_interfaces`. |
| 1.2 | `baremetal node inventory save <node> [--file]` | 6 | `baremetal/v1/nodes.GetInventory` | Ironic-native inventory (API ≥ 1.81). Writes JSON to stdout or `--file`, mirroring `image save`. Also add `baremetal node inventory show` for table output. |
| 1.3 | `baremetal node abort <node> [--wait]` | 6 | `nodes.ChangeProvisionState` with `nodes.TargetAbort` | Slots straight into `baremetal/node_provision.go` next to deploy/manage/provide; reuse its `--wait` polling. |
| 1.4 | `baremetal introspection interface list <node> [--long]` | 4 | `baremetalintrospection/v1/introspection.GetIntrospectionData` | Interfaces are projected out of the introspection data blob (`inventory.interfaces` + `all_interfaces` LLDP/switch info). `--long` adds switch chassis/port ID. |
| 1.5 | `baremetal introspection data save <node> [--file]` | 2 | `introspection.GetIntrospectionData` | Same raw-JSON dump pattern as 1.2. |
| 1.6 | `group create/show/delete/set`, `group add/remove user` | 3 | `identity/v3/groups` | Already listed as Tier 1 in `coverage.md`; history confirms `group create`, `group show`, `group add user` are used. |
| 1.7 | `baremetal introspection abort <node>`, `baremetal introspection start/status/list` | 1 | `introspection.AbortIntrospection` / `StartIntrospection` / `GetIntrospectionStatus` / `ListIntrospections` | The whole package is vendored — wire the set in one file, not just abort. Endpoint is the separate `ironic-inspector` catalog entry; add an `Introspection()` factory in `internal/auth/services.go`. |
| 1.8 | `resource provider inventory list <rp>` (+ `show <rp> <class>`) | 1 | `placement/v1/resourceproviders.GetInventories` / `GetInventory` | `resource provider usage show` (`GetUsages`) and `resource provider aggregate list` (`GetAggregates`) are the same call shape — add them while the file is open. |
| 1.9 | `network extension list` (+ `show`) | 1 | raw `ServiceClient.Get("extensions")` | gophercloud vendors only the `extensions/*` subpackages, not the root `extensions` list. Small raw helper per the AGENTS.md fallback rule. |

**Global auth flag in this phase:**

| # | Flag | Uses | Notes |
| --- | --- | --- | --- |
| 1.10 | `--os-system-scope all` (env `OS_SYSTEM_SCOPE`) | 4 | `gophercloud.AuthScope.System` and `tokens.Scope.System` are both vendored; `internal/auth` has no system-scope path today. Needed for the ironic/admin reads above (`baremetal driver show --os-system-scope all` is how it was actually invoked). Mutually exclusive with project/domain scope — validate in `options.go`. |

## Phase 2 — missing commands needing one `make tidy`

| # | Command | Uses | New package |
| --- | --- | --- | --- |
| 2.1 | `image import <image> --method web-download --uri <url>`; `image import info` | 6 | `image/v2/imageimport` |
| 2.2 | `subnet pool list`, `subnet pool show` (+ `create/set/delete`) | 4 | `networking/v2/extensions/subnetpools` |
| 2.3 | `quota set` — `--cores --ram --instances --gigabytes --volumes --snapshots` | 8 | `blockstorage/v3/quotasets` (+ `networking/v2/quotas` for ports/networks/routers). `compute/v2/quotasets` is already vendored and already used by `quota show`. |
| 2.4 | `network trunk list` (+ `show/create/delete`, `subport list/add/remove`) | 2 | `networking/v2/extensions/trunks` |

Notes:

- **2.1** is the real workflow gap: images are pulled by URL
  (`--method web-download --uri …`), never uploaded from a local file. Support
  both `--method`/`--import-method` spellings — history shows both, and OSC
  accepts `--import-method`. `image import info` renders the
  `/v2/info/import` discovery doc (methods + stores).
- **2.3** `quota show` currently reads nova quotasets only
  (`internal/cli/server/quota.go`). Extend it to volume + network in the same
  change so `quota show` and `quota set` cover the same keys; keep the nova
  `GetDefaults` raw fallback already noted there.
- Run `make tidy` **once** for all four (needs network), then confirm
  `GOFLAGS=-mod=vendor GOPROXY=off go build ./...`.

## Phase 3 — flag gaps on commands that already exist

These are the invocations that would fail today against a command `koc` claims to
cover. Ordered by usage.

| # | Command | Missing flags | Uses |
| --- | --- | --- | --- |
| 3.1 | `volume list` | `--project` / `--project-domain`, `--user`, `--all` (alias of `--all-projects`) | 56 |
| 3.2 | `port set` | `--allowed-address ip-address=…[,mac-address=…]` (repeatable), `--no-allowed-address`, `--enable-port-security` / `--disable-port-security`, `--host`, `--device`, `--device-owner`; plus new `port unset` | 13 |
| 3.3 | `subnet list` | `--name`, `--network`, `--project`/`--project-domain`, `--subnet-pool`, `--ip-version`, `--dhcp`/`--no-dhcp`, `--gateway`, `--long` (today: `--help` only) | 10 |
| 3.4 | `volume create` | `--backup <backup>` (restore-from-backup source) | 8 |
| 3.5 | `group list` | `--user` / `--user-domain` (list groups a user belongs to) | 5 |
| 3.6 | `server list` | `--project` / `--project-domain`, `--user` | 5 |
| 3.7 | `baremetal node set` | `--power-interface`, `--boot-interface`, `--deploy-interface`, `--network-interface`, `--management-interface`, `--raid-interface`, `--inspect-interface`, `--console-interface`, `--rescue-interface`, `--vendor-interface`, `--storage-interface`, `--automated-clean`/`--no-automated-clean` | 6 |
| 3.8 | `network list` | `--project`/`--project-domain`, `--provider-network-type`, `--provider-physical-network`, `--provider-segment`, `--share`/`--no-share`, `--status` | 4 |
| 3.9 | `image create` | `--visibility {public,private,shared,community}`, `--id`, `--tag` | 4 |
| 3.10 | `keypair list` / `keypair show` | `--user`/`--user-domain`, `--project` (nova ≥ 2.10), `keypair show --public-key` | 4 |
| 3.11 | `router set` | `--enable-snat` / `--disable-snat`, `--route`/`--no-route`, `--qos-policy` | 2 |
| 3.12 | `compute service list` | `--status` (filter `enabled`/`disabled`) | 1 |
| 3.13 | `baremetal node list` / `node show` | `--fields`/`--field` as aliases of `-c` (ironic CLI spelling; used 4×) | 4 |

For each: add the flag, extend the `runXxx` seam test to assert the resulting
query string / request body, and — per AGENTS.md — keep the
"UNVERIFIED against KeyStack" note near new flag definitions.

## Phase 4 — aliases for upstream names `koc` renamed or omitted

Cheap `cobra` aliases / hidden top-level commands; each is a real invocation from
history that fails today.

| # | Alias to add | Target | Uses |
| --- | --- | --- | --- |
| 4.1 | `console log show <server>` | `server console log show` | 4 |
| 4.2 | `console url show <server>` | `server console url show` | 2 |
| 4.3 | `recordset update` | `recordset set` (designate CLI spelling) | 1 |
| 4.4 | `volume extend <vol> <size>` | `volume set --size` (cinder CLI spelling) | 1 |
| 4.5 | `migration list` | `server migration list` (nova `migration-list`, cloud-wide — `--server` is already optional) | 2 |
| 4.6 | `baremetal node reboot` | `baremetal node power reboot` (existing naming deviation, listed in `coverage.md`) | — |

Register these as `Aliases:` on the existing command where cobra allows it, and
as thin top-level parents where the word order differs (`console …`). Update the
"Naming deviations" table in `coverage.md` to say the upstream spelling now works.

## Phase 5 — OSC ergonomics: unique flag-prefix abbreviation

`openstack` (argparse) resolves any unambiguous flag prefix. 51 history
invocations rely on it — `--all` for `--all-projects` (30), `--fit` for
`--fit-width` (21) — and every one of them errors under `pflag`.

Options, in order of preference:

1. **Pre-parse normalization hook.** In `cmd/koc/main.go`, before
   `Execute()`, walk `os.Args`, and for each `--foo` not defined on the resolved
   command, expand it if exactly one defined flag (local + inherited) has it as a
   prefix; leave it untouched if zero or ≥ 2 match, so cobra still produces the
   normal "unknown flag" error. ~60 lines plus a table test.
2. **Explicit aliases only** for the two observed cases: `--all` on every
   `--all-projects` command, `--fit` for `--fit-width`. Cheaper, but the next
   abbreviation an operator types still fails.

Recommend (1), with (2) as the fallback if the hook proves fiddly with
`--flag=value` and short-flag clusters. Either way document the behavior in
`README.md` — abbreviation-compatibility is a migration selling point.

Also missing and used: `--sort-column <col>` (7 uses, on `subnet list`) and
`--timing` (5 uses). `--sort-column` belongs in `internal/output` as a
format-agnostic sort applied before rendering — not per-command. `--timing`
prints per-request wall time; the `--debug` transport in `internal/auth/debug.go`
already sees every round trip, so it is a small addition there.

## Phase 6 — Octavia (`loadbalancer`), a new service from zero

`koc` has **no** load-balancer surface today (`coverage.md`: `loadbalancer` is one
of the twelve gophercloud services with zero `koc` commands). History has a single
`openstack loadbalancer list`, so this phase is scope-driven, not usage-driven —
build the operator-facing core first, leave the long tail for later.

**Groundwork (do this first, one commit):**

- `make tidy` to vendor `loadbalancer/v2/{loadbalancers,listeners,pools,monitors,
  l7policies,l7rules,amphorae,providers,quotas,flavors,flavorprofiles}` — 11
  packages. `openstack.NewLoadBalancerV2` is *already* in the vendored
  `openstack/client.go`; only the subpackages are missing.
- `internal/auth/services.go`: add a `LoadBalancer()` factory. Catalog type is
  **`load-balancer`** (not `octavia`); no microversion header — Octavia versions
  via the URL, so leave `sc.Microversion` empty, like network/dns/image.
- New package `internal/cli/loadbalancer/` with `client.go` + one file per noun,
  wired into `internal/cli/root.go`. Two-word nouns follow the existing nested-parent
  pattern (`loadbalancer stats show`, `loadbalancer status show`,
  `loadbalancer quota show`).
- Upstream names come from `python-octaviaclient` (namespace
  `openstack.load_balancer.v2`), which becomes a **fourth OSC-plugin baseline** in
  `coverage.md`. Its command count is UNVERIFIED here (~85) — derive it from
  `entry_points.txt` per the coverage recipe before writing the table row.

| # | Commands | Package | Notes |
| --- | --- | --- | --- |
| 6.1 | `loadbalancer list/show/create/set/delete`, `loadbalancer stats show`, `loadbalancer status show`, `loadbalancer failover` | `loadbalancers` (`List/Get/Create/Update/Delete/GetStats/GetStatuses/Failover`) | The whole verb set is typed. `--wait` on create/delete polls `provisioning_status` until `ACTIVE`/gone, mirroring `baremetal/node_provision.go`. |
| 6.2 | `loadbalancer listener list/show/create/set/delete` | `listeners` | |
| 6.3 | `loadbalancer pool list/show/create/set/delete` | `pools` | Name clash to watch: `loadbalancer pool` vs `subnet pool` (2.2) vs `dns pool` (7.6) — all distinct parents, no ambiguity for cobra, but keep help text explicit. |
| 6.4 | `loadbalancer member list/show/create/set/delete` | `pools.{List,Get,Create,Update,Delete}Member` | Members are pool subresources — first positional is the pool. |
| 6.5 | `loadbalancer healthmonitor list/show/create/set/delete` | `monitors` | |
| 6.6 | `loadbalancer l7policy …`, `loadbalancer l7rule …` | `l7policies`, `l7rules` | l7rule is an l7policy subresource. |
| 6.7 | `loadbalancer quota list/show/set/unset` | `quotas` (`Get/Update/Delete`) | `quota defaults show` has no typed call — raw `GET /v2.0/lbaas/quotas/defaults`. Keep it next to Phase 2.3's quota work so all `quota` verbs read alike. |
| 6.8 | `loadbalancer amphora list/show/failover` | `amphorae` (`List/Get/Failover`) | `amphora configure`, `amphora delete`, `amphora stats show` have no typed calls → raw fallback, isolated in one helper with the usual comment. |
| 6.9 | `loadbalancer provider list` | `providers` | `provider capability list` is raw (`GET /v2.0/lbaas/providers/<p>/capabilities`). |
| 6.10 | `loadbalancer flavor …`, `loadbalancer flavorprofile …` | `flavors`, `flavorprofiles` | Full CRUD is typed. |
| 6.11 | `loadbalancer availabilityzone …`, `loadbalancer availabilityzoneprofile …` | — | No gophercloud package at v2.13.0. Raw fallback, or defer — lowest operator value in the phase. |

Land 6.1 alone first (it is the only command in history and validates the client
factory + catalog type against the real cloud), then 6.2–6.6 as one "listener /
pool / member / healthmonitor" commit, then the admin tail.

## Phase 7 — Designate (`dns`), from 10/60 to the full plugin surface

`koc` covers `zone` and `recordset` CRUD only — 10 of designate's 60 commands.
`designate pool update` in history is actually `designate-manage pool update`,
**server-side tooling with no API equivalent**; the client-visible piece is the
read-only `/v2/pools` endpoint (7.6).

Vendored today: `dns/v2/{zones,recordsets}`. Available in gophercloud v2.13.0 and
needing one `make tidy`: `dns/v2/{quotas,tsigkeys,transfer/request,transfer/accept}`.
Everything else is a raw `ServiceClient` fallback — the designate API is small and
uniform, so a single `internal/cli/dns/raw.go` helper (typed DTOs + list/get/
create/update/delete over a path prefix) covers all of 7.4–7.8 without repetition.

| # | Commands | Source | Notes |
| --- | --- | --- | --- |
| 7.1 | `zone share create/list/show/delete` | `zones.{Share,ListShares,GetShare,Unshare}` | **Already vendored** — no `make tidy`, pure wiring. Upstream spells these `openstack dns zone share …`; register both (`zone share …` + a `dns zone share …` alias) per Phase 4's alias policy. |
| 7.2 | `zone transfer request create/list/show/set/delete`, `zone transfer accept request/list/show` | `transfer/request`, `transfer/accept` | `make tidy`. `accept` has `Create/List/Get` only — matches the upstream verb set. |
| 7.3 | `dns quota list/set/reset`, `tsigkey create/list/show/set/delete` | `quotas` (`Get/Update`), `tsigkeys` (full CRUD) | `make tidy`. `dns quota reset` = `DELETE /v2/quotas/<project>` (no typed call) — raw. |
| 7.4 | `zone export create/list/show/delete`, `zone export showfile`, `zone import create/list/show/delete` | raw | `/v2/zones/tasks/{exports,imports}`. `showfile` returns `text/dns` — bypass the table layer and stream the zone file to stdout. |
| 7.5 | `zone blacklist create/list/show/set/delete`, `tld create/list/show/set/delete` | raw | `/v2/blacklists`, `/v2/tlds`. Plain CRUD, admin-scoped. |
| 7.6 | `dns pool list`, `dns pool show` | raw | `/v2/pools`, read-only. Document in the command help that pool *writes* are `designate-manage pool update` on the controller and deliberately absent — this is what the history invocation was reaching for. |
| 7.7 | `ptr record list/show/set/unset` | raw | `/v2/reverse/floatingips/<region>:<fip-id>`. Needs the region prefix — take `--os-region-name`, and resolve the floating IP through `internal/cli/resolve`. |
| 7.8 | `dns service list`, `dns service show` | raw | `/v2/service_statuses` — per-service designate health (`central`, `api`, `producer`, `worker`, `mdns`). |

Add `--all-projects` / `--sudo-project-id` where upstream has it (designate's
cross-project reads use the `X-Auth-All-Projects` and `X-Auth-Sudo-Project-ID`
headers, not query parameters — a small `MoreHeaders` shim in `client.go`).

## Out of scope (recorded so it is not re-litigated)

| Invocation | Uses | Why not |
| --- | --- | --- |
| `designate-manage pool update` (typed as `designate pool update`) | 2 | Server-side tooling against `pools.yaml`, not an API call. The readable half ships as `dns pool list/show` in 7.6. |
| `openstack network service list` | 1 | Almost certainly a mistyped `network service provider list`. Tier 3 (raw fallback) — do it only if it recurs. |
| `nova help`, `openstack migration`, `port sgiw`, `baremetal show`, `server depete` | ~10 | Typos / partial lines. |

## Suggested commit sequence

One `feat:` commit per row group, each carrying its own tests **and** its
`docs/coverage.md` update (per AGENTS.md, a command-surface change without a
coverage bump is incomplete):

1. `feat(baremetal): add "driver show", "node abort", "node inventory save"` — 1.1–1.3
2. `feat(baremetal): add "introspection" start/status/list/abort/data save/interface list` — 1.4, 1.5, 1.7
3. `feat(auth): add --os-system-scope for system-scoped tokens` — 1.10
4. `feat(identity): add "group" create/show/delete/set and add/remove user` — 1.6
5. `feat(placement): add "resource provider inventory/usage/aggregate" reads` — 1.8
6. `feat(image): add "image import" and "image import info"` — 2.1 (`make tidy`)
7. `feat(network): add "subnet pool" and "network trunk" commands; "network extension list"` — 2.2, 2.4, 1.9 (`make tidy`)
8. `feat: add "quota set" and extend "quota show" to volume and network quotas` — 2.3 (`make tidy`)
9. `feat(network): flag parity for port set / subnet list / network list` — 3.2, 3.3, 3.8
10. `feat(volume): add "volume list --project" and "volume create --backup"` — 3.1, 3.4
11. `feat(compute): flag parity for server list, keypair, compute service list` — 3.6, 3.10, 3.12
12. `feat(identity): add "group list --user"` — 3.5
13. `feat(baremetal): add hardware-interface and automated-clean flags to "node set"` — 3.7, 3.13
14. `feat(image): add "image create --visibility/--id/--tag"` — 3.9
15. `feat(network): add "router set --enable-snat/--disable-snat/--route"` — 3.11
16. `feat: accept upstream command spellings as aliases` — Phase 4
17. `feat: resolve unique flag-name prefixes like openstack does` — Phase 5 + `--sort-column`, `--timing`
18. `feat(dns): add "zone share" create/list/show/delete` — 7.1 (vendored already)
19. `feat(loadbalancer): add the octavia client and "loadbalancer" list/show/create/set/delete/failover/stats/status` — Phase 6 groundwork + 6.1 (`make tidy`)
20. `feat(loadbalancer): add listener, pool, member and healthmonitor commands` — 6.2–6.5
21. `feat(loadbalancer): add l7policy, l7rule, quota, amphora, provider, flavor commands` — 6.6–6.10
22. `feat(dns): add zone transfer requests/accepts, dns quota and tsigkey commands` — 7.2, 7.3 (`make tidy`)
23. `feat(dns): add zone export/import, blacklist and tld commands` — 7.4, 7.5
24. `feat(dns): add "dns pool", "ptr record" and "dns service" reads` — 7.6–7.8

Steps 6–8 share one `make tidy`, and 19 + 22 share another (octavia's 11 packages
and designate's 4 land in one `vendor/` move if you do them adjacently) — so
group them: 6–8, then 19+22 before their dependent commits. Aim for three
`vendor/` bumps total across the whole plan, not seven.

Phases 6–7 are the largest single jump in the coverage table: designate goes
10/60 → ~56/60, and `loadbalancer` stops being a zero-surface service, which also
drops it (and `baremetalintrospection`, via Phase 1.7) from the "twelve services
with zero `koc` surface" list. Add the `python-octaviaclient` baseline row and a
`load balancer` row to "vs OSC plugins" in the same commit as step 19.

## Verification per step

- `make fmt vet lint test` clean; `golangci-lint` at **0 issues**.
- Offline invariant: `GOFLAGS=-mod=vendor GOPROXY=off CGO_ENABLED=0 go build ./...`.
- `runXxx`-seam test asserting method, URL, microversion header, request body,
  rendered output (AGENTS.md → Testing).
- End-to-end against the real cloud for at least the write verbs
  (`quota set`, `image import`, `port set --allowed-address`,
  `baremetal node abort`) — these were the ones being run by hand in the first
  place, so replay the exact history invocation with `koc`.
- Phases 6–7 have no history to replay, so they need explicit e2e: confirm the
  `load-balancer` catalog entry resolves on a cloud that actually runs Octavia
  (`koc loadbalancer list`), and create → set → delete one throwaway LB and one
  throwaway zone share rather than trusting mock tests alone.
- `docs/coverage.md`: per-service row (raw + core), headline total, snapshot line
  (date, commit, leaf count), gap-tier removal, and a "Naming deviations" row for
  every Phase 4 alias.

## How to regenerate this list

```sh
strings ~/.zsh_history | sed -E 's/^: [0-9]+:[0-9]+;//' > hist.txt
# split each line on ; | && $( ` , skip env-assignments/sudo/time/watch,
# keep segments whose first token is openstack|koc|nova|cinder|neutron|glance|
# designate|ironic|baremetal|placement, strip flags, then match the longest
# leading noun/verb prefix against `koc`'s leaf tree (walk --help recursively).
```

The scratch scripts used for this pass are throwaway; the recipe above plus
`docs/coverage.md` → "Updating this document" is enough to redo it.
