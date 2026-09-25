# Network (neutron) feature parity: commands *and* flags

Companion to `docs/coverage.md` and `docs/proposals/coverage-tiers.md`. Those
measure and close the gap in **commands**; this one also measures the gap in
**flags on commands koc already has** — which is where the network surface is
actually thin. The trigger was a user report that `koc floating ip list
--project` is rejected: the command exists and is counted as covered, but it
takes no filter at all.

**Measured 2026-09-25** at `e13309e`, against `python-openstackclient` **10.3.0**
(PyPI sdist, now the latest; `coverage.md` still cites 10.2.1) and
`gophercloud/v2` **v2.15.0** (the `go.mod` pin; `coverage.md` still cites
v2.13.0 — the networking package set is identical between the two).

## Status (2026-09-25)

Steps 1–6 of §5 have shipped on this branch, and step 8 (the post-Zed flags) is
in progress:

| Step | Commit(s) | Result |
| --- | --- | --- |
| 1–2 foundation + floating IP | `da62b6b` | tag helpers, `--extra-property`, body-merge adapters, `attributestags` vendored; `floating ip list --project` and the rest of the floating-IP surface |
| 4–5 per-noun flags | `5cc80b1` network · `1e00ae6` security groups · `112e15d` router · `0b2ec2b` QoS/RBAC/segments/port forwarding/address scopes+groups · `b4e68e9` subnet + subnet pool · `5e3cee9` port | every N1–N9 row of §2b |
| 3 core commands | `1e00ae6`, `58aecab`, `b4e68e9` | all 10 of §3a — core is **97/97** |
| 6 shape deviations | `58aecab`, `0b2ec2b`, `b4e68e9`, `5cc80b1` | every row of §4 |
| foundation fixes | `352526a` | If-Match kept through the adapters; missing-extension errors named |
| column parity | `53c44aa` | `ip availability list`, `network qos rule list` |
| 8 post-Zed flags | `7181533` router · network/subnet and port in progress | §2c |

Command surface: **114/168** raw (core 97/97), up from 104/168. Flag surface:
every upstream option of every implemented command is registered except the §2c
set still in flight — `internal/cli/network/upstream_flags_test.go` walks the
cobra tree against all of them. §6 below is the second audit: what is left.

## Summary (as measured before the pass)

| | Count |
| --- | --- |
| Upstream `openstack.network.v2` commands | 168 (was 165 at 10.2.1) |
| … implemented by koc under the same name | **104 (62%)** |
| … missing — "core" (per `coverage.md`'s definition) | **10** |
| … missing — niche subsystems | **54** |
| Implemented commands with at least one missing upstream flag (pagination and `--variable` excluded) | **54 of 104** |
| Substantive missing flags (excluding the cross-cutting groups below) | **218, in 38 commands** |
| Commands missing the tag flags (`--tag`/`--no-tag`/`--all-tag`, `--tags`/`--any-tags`/…) | 23 |
| Commands missing `--project-domain` next to an existing or missing `--project` | 20 |
| Commands missing `--extra-property` | 34 |
| Upstream network-plugin commands (VPNaaS/FWaaS/BGP/BGPVPN/TaaS) counted nowhere | 100 |

The headline point: **command coverage (62% raw, ~90% core) overstates what an
operator can do.** `router create` takes only `--enable/--disable`; `network
create` has no `--project`, `--description` or `--qos-policy`; `floating ip
list` and `security group rule list` have no filters; `subnet create` cannot
allocate from a subnet pool. Each of these counts as "covered" today.

The good news: **almost all of the flag gap needs no new gophercloud package.**
The `ListOpts`/`CreateOpts` already vendored model the fields, or koc's existing
local `*OptsExt` merge pattern (`port.go` `portCreateOptsExt`, `PortSecurityExt`)
covers the rest.

## 1. Baseline drift since `coverage.md` was written

- **OSC 10.3.0 added `network trunk subport add/list/remove`** as real entry
  points. koc already has all three — so they move from "koc-native" to
  upstream-equivalent, and the `network trunk subport list` → `network subport
  list` naming deviation stops applying (the same koc leaf cannot be counted
  twice). `network subport list --trunk` is still registered upstream, now as
  the older spelling; it becomes an ordinary missing command.
- **The upstream shape of those three differs from koc's.** Upstream takes
  positionals — `network trunk subport add <trunk> <port> [--segmentation-type
  T --segmentation-id N]` and `… remove <trunk> <port>` — while koc takes
  `<trunk> --subport port=…,segmentation-type=…,segmentation-id=…` (repeatable).
  See §4.
- **OSC's own entry points carry five network plugin namespaces** that
  `coverage.md` counts nowhere — neither in scope nor in the "not targeted"
  rows: `openstack.network.v2.bgpvpn` (22), `.dynamic_routing` (18), `.fwaas`
  (20), `.taas` (15), `.vpnaas` (25). They were already present at 10.2.1, so
  the 901/844 denominators silently omit 100 commands. gophercloud v2.15.0 has
  typed packages for all five (`bgp/{peers,speakers}`, `bgpvpns`,
  `fwaas_v2/{groups,policies,rules}`, `taas/tapmirrors`, `vpnaas/*`). Proposal:
  add them as a **"not targeted"** row (like Swift/Manila) unless a KeyStack
  deployment actually runs one of these services — the omission, not the
  decision, is the bug.
- `coverage.md`'s gophercloud pin reads v2.13.0; `go.mod` is at v2.15.0.

Updated rows once the above is applied (no command added):

| Namespace | Raw | Core |
| --- | --- | --- |
| `openstack.network.v2` (10.3.0) | **104/168 (62%)** | **87/97 (90%)** |

(Core denominator 94 → 97 for the three new trunk subport entry points; the
numerator gains `add`/`remove` and keeps `list`, now by exact name.)

## 2. Flag parity on existing commands — the main work

### 2a. Cross-cutting building blocks (do these first; every batch below uses them)

1. **Tags.** Neutron tags are a separate resource (`PUT
   /v2.0/<collection>/<id>/tags`), so create/set are "write the resource, then
   replace its tags" — which is exactly what upstream does (`osc_lib/utils/tags.py`
   `update_tags_for_set`).
   Vendor **`networking/v2/extensions/attributestags`** (one `make tidy`; it is
   small and typed) and write one helper pair:
   - `addTagFlags(fl, &f)` → `--tag` (repeatable), `--no-tag`, and for unset
     `--tag`/`--all-tag`;
   - `addTagFilterFlags(fl, &f)` → `--tags`, `--any-tags`, `--not-tags`,
     `--not-any-tags`. Every vendored `ListOpts` for these resources already has
     `Tags/TagsAny/NotTags/NotTagsAny`; `port list` wires them today — lift that
     into the helper.
   This also unblocks the two core commands deliberately dropped until tag
   support existed: **`security group unset`** and **`subnet pool unset`**
   (both are `--tag`/`--all-tag` only upstream). 23 commands gain flags.
2. **`--project` / `--project-domain`.** `resolveProjectRef(ctx, session, ref,
   domainRef)` in `helpers.go` already does both; `security group list`, `port
   list`, `router list` and `network list` use it. Twenty commands lack the
   domain half and twelve lack `--project` entirely (the create verbs of
   network, router, subnet, port, floating IP, trunk, security group, SG rule,
   RBAC, plus `address scope list`, `security group rule list` and
   **`floating ip list`**). Mechanical.
3. **`--extra-property type=<type>,name=<name>,value=<value>`.** Upstream's
   escape hatch for attributes the CLI does not model (`network/common.py`,
   `NeutronCommandWithExtraArgs`) on 34 create/set/unset verbs. koc already
   merges extension fields into request maps by hand; generalise that into one
   `mergeAttrs(builder, map[string]any)` helper and `--extra-property` falls
   out of it for free — and it becomes the implementation vehicle for every
   single-field extension flag in 2b.
4. **Pagination** (`--limit`, `--marker`, `--max-items` from osc-lib): out of
   scope for parity counting. koc already drains pages; `--limit` as a hard cap
   exists where it matters.

Not a gap: `--variable` (cliff's `-f shell` formatter option; koc has no shell
formatter).

### 2b. Per-noun batches

Each row is one commit-sized batch. "gophercloud" says where the fields live.

| # | Batch | Flags | gophercloud | Zed |
| --- | --- | --- | --- | --- |
| N1 | **`floating ip list` filters** (the reported case) | `--network` `--port` `--fixed-ip-address` `--floating-ip-address` `--router` `--status` `--project` `--project-domain` `--long` + tag filters | all in `floatingips.ListOpts` | ✓ |
| N2 | other list filters | `security group rule list --ingress/--egress --ethertype --protocol --project --long`; `network list --agent --enable/--disable --internal`; `network agent list --network --router --long`; `subnet list --subnet-range --service-type`; `address scope list --project`; `network rbac list --long`; `network segment list --long`; `floating ip port forwarding list --port --external-protocol-port --protocol` | `ListOpts` (rules, networks, subnets `cidr`, portforwarding); `agents.ListDHCPNetworks`, `routers.ListL3Agents`; `service_types` and `/networks/{id}/dhcp-agents` via local ext/raw | ✓ |
| N3 | **`router create/set/unset`** | `--description` `--project` `--external-gateway` `--fixed-ip` `--enable-snat/--disable-snat` `--distributed/--centralized` `--ha/--no-ha` `--availability-zone-hint` `--flavor/--flavor-id` `--qos-policy/--no-qos-policy`; `unset --route --qos-policy`; `remove gateway --fixed-ip`; `add subnet --advertise-host` | `routers.CreateOpts` (description, distributed, gateway info incl. SNAT/fixed IPs, AZ hints); `ha`, `flavor_id` via `mergeAttrs` | ✓ core; see 2c |
| N4 | **`network create/set`** | `--description` `--project` `--qos-policy/--no-qos-policy` `--availability-zone-hint` `--dns-domain` `--enable/--disable-port-security` `--default/--no-default` `--internal` `--no-share` `--transparent-vlan/--no-transparent-vlan`; `set` also `--external` `--provider-*` | `networks.CreateOpts`; `external`/`provider` already vendored; `is_default`, `dns_domain`, `port_security_enabled`, `qos_policy_id`, `vlan_transparent` via `mergeAttrs` | ✓ |
| N5 | **`subnet create/set`** | `--subnet-pool` `--use-default-subnet-pool` `--prefix-length` `--project` `--description` `--host-route` `--ipv6-ra-mode` `--ipv6-address-mode` `--network-segment` `--service-type` `--dns-publish-fixed-ip/--no-…` `--use-prefix-delegation`; `set` also `--allocation-pool/--no-allocation-pool` `--no-dns-nameservers` `--host-route/--no-host-route` | `subnets.CreateOpts`/`UpdateOpts` model all but `use_default_subnetpool` (→ `mergeAttrs`) | ✓ |
| N6 | **`port create/set/unset`** (core half) | `--host` (create) `--vnic-type` `--binding-profile` `--device` `--project` `--qos-policy` `--dns-name` `--dns-domain` `--extra-dhcp-option` `--no-fixed-ip` `--mac-address` (set) `--enable/--disable-uplink-status-propagation` `--data-plane-status`; unset counterparts | `binding:*` and `port_security_enabled` already hand-merged in `port.go`; rest via `mergeAttrs` | ✓ |
| N7 | **`floating ip create/set/unset`** | `--project` `--description` `--qos-policy/--no-qos-policy` `--dns-name` `--dns-domain` + tags | `floatingips.CreateOpts` has project/description; `qos_policy_id`, `dns_*` via `mergeAttrs` | ✓ |
| N8 | security groups | `security group create/set --stateful/--stateless --project`; `rule create --description --icmp-type --icmp-code --remote-address-group --project` | `groups.CreateOpts.Stateful`; `rules.CreateOpts` has description, remote_address_group_id; ICMP maps onto port_range_min/max | ✓ |
| N9 | smaller verbs | `network agent set --description`; `address scope create --no-share`; `subnet pool set --no-address-scope`; `network trunk create --project`; `network rbac create --project --target-project-domain` | existing opts | ✓ |

### 2c. Post-Zed and driver-specific flags — implement last, gate by extension

Neutron has no microversions: an unsupported attribute returns neutron's own
400, which `coverage-tiers.md` already accepts as the right behaviour for an
absent extension. These still belong at the back of the queue because the older
clouds in the fleet will reject them, and each needs a "requires the `<alias>`
neutron extension" note in its help text:

- router: `--enable/--disable-ndp-proxy`, `--enable/--disable-default-route-bfd`,
  `--enable/--disable-default-route-ecmp`, `--evpn-vni`, `--auto-evpn-vni`;
- network/port: `--pvlan*`, `--qinq-vlan`, `--pvlan-type`, `--pvlan-community`;
- port: `--trusted/--not-trusted`, `--hardware-offload-type`, `--hint(s)`,
  `--numa-policy-*`, `--device-profile`.

The exact neutron release each alias landed in should be read off the neutron
sdist (`neutron_lib/api/definitions/*.py`) at implementation time, the same way
ironic's microversions are read off `versions.py` — not from memory.

## 3. Missing commands

### 3a. Core (10) — all cheap

| Command | Implementation | gophercloud |
| --- | --- | --- |
| `network agent add network --dhcp <agent> <network>` | POST `/agents/{id}/dhcp-networks` | `agents.ScheduleDHCPNetwork` (vendored) |
| `network agent remove network --dhcp <agent> <network>` | DELETE | `agents.RemoveDHCPNetwork` |
| `network agent add router --l3 <agent> <router>` | POST `/agents/{id}/l3-routers` | `agents.ScheduleL3Router` |
| `network agent remove router --l3 <agent> <router>` | DELETE | `agents.RemoveL3Router` |
| `network agent router set --ha-chassis-priority N <agent> <router>` | OVN-only, recent | raw |
| `network service provider list` | GET `/service-providers` | raw (one call) |
| `network trunk unset --subport <port> <trunk>` | | `trunks.RemoveSubports` (vendored) |
| `network subport list --trunk <trunk>` | alias of `network trunk subport list` | — |
| `security group unset --tag/--all-tag` | needs §2a.1 | `attributestags` |
| `subnet pool unset --tag/--all-tag` | needs §2a.1 | `attributestags` |

`--ha-chassis-priority` on `network agent add router` rides with the same batch.
Doing 3a brings core to **97/97**.

### 3b. Niche (54) — Tier 3, raw `ServiceClient`

No gophercloud package exists for any of these; each is a plain CRUD REST
resource, so each noun is one raw helper plus thin verbs, following
`qos.go`'s pattern.

| Noun | Cmds | Zed |
| --- | --- | --- |
| `network flavor` + `network flavor profile` (+ add/remove profile) | 12 | ✓ (old API) |
| `network meter` + `network meter rule` | 8 | ✓ |
| `local ip` + `local ip association` | 8 | ✓ (Yoga) |
| `router ndp proxy` | 5 | ✓ (Yoga) |
| `network l3 conntrack helper` | 5 | ✓ |
| `network segment range` | 5 | ✓ |
| `network auto allocated topology create/delete` | 2 | ✓ |
| `default security group rule` | 4 | ✗ post-Zed (2023.2) — verify alias |
| `security group default statefulness` | 5 | ✗ post-Zed (recent neutron) — verify alias |

Suggested order by operator value: segment ranges and auto-allocated topology
(admins provisioning tenant networks), then flavors (needed for `router create
--flavor` to be useful), then the rest.

## 4. Shape deviations to reconcile

Same name, different behaviour — the class of deviation this project has
removed before (`loadbalancer quota unset`):

| Command | Upstream | koc | Proposal |
| --- | --- | --- | --- |
| `network trunk subport add` | `<trunk> <port> --segmentation-type T --segmentation-id N` | `<trunk> --subport port=…,…` (repeatable) | accept both: optional 2nd positional + the two flags; keep `--subport` for multi-add |
| `network trunk subport remove` | `<trunk> <port>` | `<trunk> --subport <port>` | accept both |
| `network trunk set` | `--subport port=…,…` adds subports | no `--subport` | add it (same parser as above) |
| `network qos rule create/set` | `--ingress` / `--egress` / `--any` | `--direction <d>` | add upstream spellings; keep `--direction` as a koc extra |
| `network unset --share` | not upstream (`network set --no-share` is) | koc-only | add `network set --no-share` (N4); keep `unset --share` as a documented extra or drop it |
| `subnet set --no-gateway` | upstream spells it `--gateway none` | koc-only | accept `--gateway none` too |

The remaining koc-only flags (`--all-projects`, `--name` on list verbs,
`--ip-version` on `subnet pool list`, port-range flags on port forwarding,
trunk/segment list filters) are additive and already documented or harmless;
record any undocumented ones in `coverage.md` "koc-native".

## 5. Proposed order and cost

| Step | What | New commands | Vendor change |
| --- | --- | --- | --- |
| 1 | N1 `floating ip list` filters + `--project-domain` everywhere | 0 | none |
| 2 | §2a helpers: `attributestags` + tag flags on 23 verbs, `mergeAttrs` + `--extra-property` | 0 | `make tidy` (one package) |
| 3 | §3a core commands incl. `security group unset`, `subnet pool unset` | 10 | none |
| 4 | N3 router, N4 network, N5 subnet (the create-path gaps operators hit most) | 0 | none |
| 5 | N2 remaining list filters, N6–N9 | 0 | none |
| 6 | §4 shape reconciliation | 0 | none |
| 7 | §3b Tier 3 nouns, one commit each | 54 | none |
| 8 | §2c post-Zed flags, as clouds need them | 0 | none |
| — | `coverage.md`: OSC 10.3.0 / gophercloud v2.15.0 baseline, trunk subport reclassification, plugin-namespace row | — | — |

Steps 1–6 close the gap operators actually feel with **no new raw fallbacks**
and one small vendored package. Each step follows AGENTS.md: the `runXxx`
seam, request-query/body assertions in tests, `docs/coverage.md` in the same
commit when a command lands.

To keep this from regressing, extend `internal/cli/network/flagparity_test.go`
(or add a sibling) with a table of upstream flag names per command, walked
against the cobra tree, the way `server/flagparity_test.go` does for compute —
so a future "counted as covered" command cannot silently lack its filters.

## 6. Second audit — what is left after the pass (2026-09-25)

Measured on the merged tree against the same 10.3.0 parsers, plus two new
checks: every list verb's headers against upstream's own `take_action` run on a
mocked client (`$SP/np/cols.py`), and the behaviour notes from each batch.

### 6a. Flags

Nothing outside §2c. The regression test is the gate from here on.

### 6b. List columns

Upstream and koc headers now agree for every implemented list verb, default and
`--long`, with these exceptions:

| Command | Difference | Proposal |
| --- | --- | --- |
| `router list --long` | `Availability zones` appears when the routers carry the attribute; upstream keys it on the `router_availability_zone` extension, so the two differ only on an empty list (header without rows) | keep — matching exactly costs an extension lookup per listing |
| `network rbac list --long`, `network trunk list --long`, `subnet list --long`, `subnet pool list --long` | koc appends one or two extra columns after upstream's (Target Project; Sub Ports, Project; Subnet Pool; Project) | keep: they sit after upstream's columns, so positional `-f value`/`csv` consumers are unaffected; record them in `coverage.md` next to the other koc-native flags |

### 6c. Missing commands — 54, all Tier 3 (no gophercloud package)

Every one is plain REST CRUD, so each noun is one raw helper plus thin verbs in
the `qos.go` style. Extension aliases and dates are from neutron-lib 5.0.1
(`UPDATED_TIMESTAMP`); paths from openstacksdk.

| Noun | Cmds | API | Extension | On Zed? | Proposal |
| --- | --- | --- | --- | --- | --- |
| `network segment range` | 5 | `/network_segment_ranges` | `network-segment-range` (2018) | ✓ | **next**: admins carve VLAN/VNI pools with it |
| `network auto allocated topology create/delete` | 2 | `/auto-allocated-topology/{project}` | `auto-allocated-topology` (2016) | ✓ | **next**: `--or-show`, `--check-resources` are the whole surface |
| `network flavor` + `network flavor profile` + add/remove profile | 12 | `/flavors`, `/service_profiles`, `/flavors/{id}/service_profiles` | `flavors` (2015) | ✓ | next; also unlocks name lookup for `router create --flavor` |
| `network meter` + `network meter rule` | 8 | `/metering/metering-labels`, `/metering/metering-label-rules` | `metering` (2013) | ✓ | after flavors |
| `local ip` + `local ip association` | 8 | `/local_ips`, `/local_ips/{id}/port_associations` | `local_ip` (2021, Yoga) | ✓ | after metering |
| `router ndp proxy` | 5 | `/ndp_proxies` | `l3-ndp-proxy` (2021, Yoga) | ✓ | with the router ndp-proxy flags |
| `network l3 conntrack helper` | 5 | `/routers/{id}/conntrack_helpers` | `l3-conntrack-helper` (2019) | ✓ | after local IP |
| `default security group rule` | 4 | `/default-security-group-rules` | neutron 2023.2 (defined in neutron, not neutron-lib) | ✗ | last; name the missing extension via `explainMissingExtension` |
| `security group default statefulness` | 5 | `/security-groups-default-statefulness` | `security-groups-default-statefulness` (2026) | ✗ | last, same |

Every row except the last two works on Zed, so 45 of the 54 are reachable by the
whole fleet. At roughly one noun per commit, raw coverage goes 114 → 168/168.

### 6d. The neutron plugin namespaces — 100 commands

`python-openstackclient` 10.3.0 registers them in its own entry points (they were
absorbed from python-neutronclient), so they are upstream `openstack network …`
surface too. Unlike §6c, gophercloud already has typed packages for almost all of
it:

| Namespace | Cmds | gophercloud v2.15.0 | Gap |
| --- | --- | --- | --- |
| `vpnaas` (VPN service, IKE/IPsec policy, endpoint group, site connection) | 25 | `vpnaas/{services,ikepolicies,ipsecpolicies,endpointgroups,siteconnections}` — full CRUD | none |
| `fwaas` (firewall group, policy, rule, add/remove rule) | 20 | `fwaas_v2/{groups,policies,rules}` incl. `InsertRule`/`RemoveRule` | none |
| `bgpvpn` (+ network/port/router associations) | 22 | `bgpvpns` incl. all three association kinds | none |
| `dynamic_routing` (BGP speaker, peer, dragent) | 18 | `bgp/{speakers,peers}` incl. add/remove peer and network, advertised routes; dragent scheduling in `agents` (`ScheduleBGPSpeaker`, `RemoveBGPSpeaker`, `ListDRAgentHostingBGPSpeakers`) | none |
| `taas` (tap service, flow, mirror) | 15 | `taas/tapmirrors` only | tap service + tap flow (10) need raw |

**Proposal:** treat these as Tier 2 (one `make tidy` each, no raw fallback) *if*
a KeyStack deployment runs the service; otherwise keep them in the
"not targeted" row where `coverage.md` now lists them. The decision is a
deployment fact, not a code one — `koc network extension list` on each cloud
(`vpnaas`, `fwaas_v2`, `bgpvpn`, `bgp`, `taas`) answers it. VPNaaS and FWaaS are
the likeliest to be deployed and the cheapest (45 commands, zero raw code).

### 6e. Behaviour — deviations the batches recorded

Same command, same flags, different result. Each is small; grouped by how much
it matters:

**Fix (correctness):**

1. **Zero-match name lookup.** Every neutron resolver falls back to the literal
   reference when nothing matches by name, so `koc network delete typo` sends
   `DELETE /v2.0/networks/typo` and surfaces neutron's 404 (`coverage.md` "Known
   limitations"). Upstream's `find_*` tries `GET /{coll}/{ref}`, then a name
   list, then errors "No Network found for typo". Proposal: give
   `resolveByName` that exact order and error on zero matches — one change in
   `helpers.go`, every noun inherits it. It also removes the needless name-list
   GET the older resolvers make for a UUID (the newer ones already short-circuit).
2. **`ip availability list` sends no `ip_version` by default**; upstream sends
   `ip_version=4` unless told otherwise, so an IPv4+IPv6 network is listed once
   upstream and twice by koc. Proposal: default `--ip-version` to 4, as upstream.
3. **`port unset --fixed-ip/--security-group/--allowed-address` partial match.**
   koc removes anything matching and succeeds when nothing does; upstream wants an
   exact entry and errors "Port does not contain …" (subnet unset already does
   this). Proposal: exact match + error, as subnet unset.
4. **`network qos rule create/set` flag validation.** Upstream rejects a flag
   that does not belong to the rule type (`--max-kbps` on `dscp-marking`) and a
   missing required one; koc ignores the stray flag and lets neutron 400 on the
   missing one. Proposal: port upstream's per-type required/optional table.

**Fix (name resolution upstream does and koc does not):**

5. `network rbac create <object>` by name (upstream resolves per `--type`).
6. `floating ip port forwarding … <floating-ip>` by address (upstream accepts
   address or ID; koc takes ID).
7. `--internal-protocol-port`/`--external-protocol-port` accepting `N:M`
   (upstream's spelling of a range; koc has separate `*-range` flags — keep both).

**Keep and document (deliberate):**

8. set/unset print the resource afterwards and error when given no flag;
   upstream prints nothing and silently no-ops. koc's form is scriptable
   (`-f value -c id`) and catches typos — keep, list under "Naming deviations".
9. `security group rule list --long` is a documented no-op without upstream's
   deprecation warning (a stderr line would garble `--watch`).
10. `rule list --ethertype` is sent as a filter — upstream 10.3.0 parses it and
    never sends it (an upstream bug).
11. `network agent add/remove network` return the error upstream builds and then
    drops (upstream exits 0 on failure).
12. `port create` sets tags with a second call rather than in the POST when the
    `tag-ports-during-bulk-creation` extension exists — one extra request, same
    result.

Suggested order: 1 (one file, removes a documented limitation), 2–4, then §6c
top to bottom, then 5–7; §6d when a deployment asks for it.

## Appendix A — per-command flag gap (as measured before the pass)

Excludes tag flags, `--project-domain`, `--extra-property`, pagination and
`--variable` (see §2a). "koc-only" lists flags upstream does not define.

| Command | Missing upstream flags | koc-only flags |
| --- | --- | --- |
| `address scope create` | `--no-share` |  |
| `address scope list` | `--project` |  |
| `floating ip create` | `--dns-domain` `--dns-name` `--project` `--qos-policy` |  |
| `floating ip list` | `--fixed-ip-address` `--floating-ip-address` `--long` `--network` `--port` `--project` `--router` `--status` |  |
| `floating ip port forwarding create` | — | `--external-protocol-port-range` `--internal-protocol-port-range` |
| `floating ip port forwarding list` | `--external-protocol-port` `--port` `--protocol` |  |
| `floating ip port forwarding set` | — | `--external-protocol-port-range` `--internal-protocol-port-range` |
| `floating ip set` | `--description` `--no-qos-policy` `--qos-policy` |  |
| `floating ip unset` | `--qos-policy` |  |
| `network agent list` | `--long` `--network` `--router` |  |
| `network agent set` | `--description` |  |
| `network create` | `--availability-zone-hint` `--default` `--description` `--disable-port-security` `--dns-domain` `--enable-port-security` `--internal` `--no-default` `--no-pvlan` `--no-qinq-vlan` `--no-share` `--no-transparent-vlan` `--project` `--pvlan` `--qinq-vlan` `--qos-policy` `--transparent-vlan` |  |
| `network list` | `--agent` `--disable` `--enable` `--internal` `--no-pvlan` `--pvlan` |  |
| `network qos rule create` | `--any` `--egress` `--ingress` | `--direction` |
| `network qos rule set` | `--any` `--egress` `--ingress` | `--direction` |
| `network rbac create` | `--project` `--target-project-domain` |  |
| `network rbac list` | `--long` |  |
| `network rbac set` | `--target-project-domain` |  |
| `network segment list` | `--long` | `--network-type` `--physical-network` |
| `network segment set` | — | `--segment` |
| `network set` | `--default` `--description` `--disable-port-security` `--dns-domain` `--enable-port-security` `--external` `--internal` `--no-default` `--no-pvlan` `--no-qos-policy` `--no-share` `--provider-network-type` `--provider-physical-network` `--provider-segment` `--pvlan` `--qos-policy` |  |
| `network trunk create` | `--project` |  |
| `network trunk list` | — | `--disable` `--enable` `--name` `--port` `--project` `--status` |
| `network trunk set` | `--subport` |  |
| `network trunk subport add` | `--segmentation-id` `--segmentation-type` | `--subport` |
| `network trunk subport remove` | — | `--subport` |
| `network unset` | — | `--share` |
| `port create` | `--binding-profile` `--device` `--device-profile` `--disable-uplink-status-propagation` `--dns-domain` `--dns-name` `--enable-uplink-status-propagation` `--extra-dhcp-option` `--hardware-offload-type` `--hint` `--host` `--no-fixed-ip` `--not-trusted` `--numa-policy-legacy` `--numa-policy-preferred` `--numa-policy-required` `--numa-policy-socket` `--project` `--pvlan-community` `--pvlan-type` `--qos-policy` `--trusted` `--vnic-type` |  |
| `port list` | `--no-pvlan` `--pvlan` `--pvlan-community` `--pvlan-type` | `--all-projects` |
| `port set` | `--binding-profile` `--data-plane-status` `--disable-uplink-status-propagation` `--dns-domain` `--dns-name` `--enable-uplink-status-propagation` `--extra-dhcp-option` `--hint` `--mac-address` `--no-binding-profile` `--no-fixed-ip` `--not-trusted` `--numa-policy-legacy` `--numa-policy-preferred` `--numa-policy-required` `--numa-policy-socket` `--pvlan-community` `--pvlan-type` `--qos-policy` `--trusted` `--vnic-type` |  |
| `port unset` | `--binding-profile` `--data-plane-status` `--device` `--device-owner` `--hints` `--numa-policy` `--pvlan-community` `--qos-policy` |  |
| `router add subnet` | `--advertise-host` |  |
| `router create` | `--auto-evpn-vni` `--availability-zone-hint` `--centralized` `--description` `--disable-default-route-bfd` `--disable-default-route-ecmp` `--disable-ndp-proxy` `--disable-snat` `--distributed` `--enable-default-route-bfd` `--enable-default-route-ecmp` `--enable-ndp-proxy` `--enable-snat` `--evpn-vni` `--external-gateway` `--fixed-ip` `--flavor` `--flavor-id` `--ha` `--no-ha` `--project` `--qos-policy` |  |
| `router list` | — | `--all-projects` |
| `router remove gateway` | `--fixed-ip` |  |
| `router set` | `--centralized` `--description` `--disable-default-route-bfd` `--disable-default-route-ecmp` `--disable-ndp-proxy` `--distributed` `--enable-default-route-bfd` `--enable-default-route-ecmp` `--enable-ndp-proxy` `--fixed-ip` `--ha` `--no-ha` `--no-qos-policy` |  |
| `router unset` | `--qos-policy` `--route` |  |
| `security group create` | `--project` `--stateful` `--stateless` |  |
| `security group list` | — | `--all-projects` `--name` |
| `security group rule create` | `--description` `--icmp-code` `--icmp-type` `--project` `--remote-address-group` |  |
| `security group rule list` | `--egress` `--ethertype` `--ingress` `--long` `--project` `--protocol` |  |
| `security group set` | `--stateful` `--stateless` |  |
| `subnet create` | `--description` `--dns-publish-fixed-ip` `--host-route` `--ipv6-address-mode` `--ipv6-ra-mode` `--network-segment` `--no-dns-publish-fixed-ip` `--prefix-length` `--project` `--service-type` `--subnet-pool` `--use-default-subnet-pool` `--use-prefix-delegation` |  |
| `subnet list` | `--service-type` `--subnet-range` |  |
| `subnet pool list` | — | `--ip-version` |
| `subnet pool set` | `--no-address-scope` |  |
| `subnet set` | `--allocation-pool` `--description` `--dns-publish-fixed-ip` `--host-route` `--leak-routes` `--network-segment` `--no-allocation-pool` `--no-dns-nameservers` `--no-dns-publish-fixed-ip` `--no-host-route` `--no-leak-routes` `--service-type` | `--no-gateway` |

substantive missing flags 218 in 38 commands
tag cmds 23 floating_ip_create floating_ip_list floating_ip_set floating_ip_unset network_create network_list network_set network_unset port_create port_set port_unset router_create router_set router_unset security_group_create security_group_set subnet_create subnet_list subnet_pool_create subnet_pool_list subnet_pool_set subnet_set subnet_unset

## Appendix B — how this was measured

Upstream flags come from OSC's own argparse parsers, not from docs
(`docs.openstack.org` and `opendev.org` are unreachable from agent
environments; PyPI is not):

```sh
python3 -m venv venv && ./venv/bin/pip install python-openstackclient==10.3.0
./venv/bin/python - <<'PY' > osc_flags.json
import json, sys
from unittest import mock
from importlib.metadata import entry_points
app = mock.MagicMock()
app.client_manager.is_network_endpoint_enabled.return_value = True
out = {}
for ep in entry_points(group='openstack.network.v2'):
    p = ep.load()(app, None).get_parser('openstack ' + ep.name.replace('_', ' '))
    out[ep.name] = sorted(s for a in p._actions for s in a.option_strings[:1]
                          if s.startswith('--'))
json.dump(out, sys.stdout, indent=1)
PY
```

koc flags come from walking `koc <cmd> --help` over the built tree (the same
walk as `coverage.md` "Updating this document", step 1) and reading each
`Flags:` block, dropping the `--watch*` family every read verb inherits.
Commands are matched in underscore form; a flag counts as present when any of
an upstream option's long aliases is.
