# Command coverage

How much of the upstream OpenStack CLI surface `koc` implements, measured against
primary sources rather than documentation.

**Snapshot:** 2026-08-12 · `koc` @ this commit (base `3986d3a`) · 548 leaf
commands (visible tree; 2 more are hidden duplicates).

**Keep this file current** — see "Updating this document" below. Any commit that
adds, renames, or removes a `koc` command must update the affected table row and
the gap list in the same commit.

## Baselines

| Baseline | Version | How it was obtained |
| --- | --- | --- |
| `python-openstackclient` | 10.2.1 | PyPI sdist → `python_openstackclient.egg-info/entry_points.txt` |
| `python-ironicclient` (OSC plugin) | 6.2.0 | PyPI sdist → `entry_points.txt` |
| `python-designateclient` (OSC plugin) | 7.0.0 | PyPI sdist → `entry_points.txt` |
| `python-octaviaclient` (OSC plugin) | 3.14.0 | PyPI sdist → `entry_points.txt` (82 commands under `[openstack.load_balancer.v2]`) |
| `osc-placement` (OSC plugin) | 4.9.0 | PyPI sdist → `entry_points.txt` |
| `gophercloud/v2` | v2.13.0 | module zip from `proxy.golang.org` (matches the `vendor/` pin) |
| `python-ironic-inspector-client` (OSC plugin) | 5.4.0 | PyPI sdist → `entry_points.txt` (13 commands under `baremetal introspection`) |

Entry points are the authoritative command list — `openstack`'s own docs lag the
code, and `opendev.org` is unreachable from CI/agent environments (HTTP 403), so
PyPI is the source of record.

## Headline

**513 of 844 in-scope upstream commands (61%).** Of `koc`'s 548 leaf commands,
513 are upstream-equivalent and 35 are koc-native.

The denominator grew by 13 against the 2026-08-07 snapshot without a single
command changing: `python-ironic-inspector-client` is now a **baseline** rather
than an untracked extra. Its 13 `baremetal introspection` commands used to be
scored against the `python-ironicclient` baseline, which never contained them —
so at that snapshot the ironic row read an inflated 35/118 where the honest split
was 29/118 for ironic and 6/13 for the inspector. Same six commands, two correct
rows; the ironic figure has since grown to 52/118.

`python-octaviaclient` became a baseline during the history-parity pass, adding
its 82 commands to the denominator; measured against the four baselines that
predate it and the inspector, the figure is 445/749 (59%).

In-scope now includes `python-ironic-inspector-client`. The inspector is
deprecated upstream — ironic removed the `inspector` inspect interface in 33.0.0
(the 2026.1 cycle) and moved inspection rules into its own API at 1.96 — but
`koc` supports clouds back to **Zed**, where the inspector is the only in-band
inspection there is. See "Minimum supported cloud" below.

Leaf counts are of the **visible** tree. Two more commands exist but are hidden
from `--help` because they duplicate a visible sibling exactly: `koc migration
list` and `koc baremetal node reboot` (see "Naming deviations").

The raw percentage understates practical parity: roughly 45% of the upstream
surface is niche subsystems (Glance metadefs, Cinder consistency/volume groups,
Neutron VPNaaS/FWaaS/metering, Keystone federation, ironic runbooks). Excluding
those, `koc` covers **81–97%** of each core OSC namespace, and 100% of designate
and placement — the commands operators actually run daily. The one outlier is
`openstack.common` at 6/11: it is a grab-bag of cross-service odds and ends
(`project cleanup`, `quota list/delete`, `configuration show`, `versions show`)
rather than a subsystem, so it admits no niche split. Both numbers are reported
below; neither alone is honest.

Counts are **leaf commands, not flags**. A command can be present and still lag
upstream's option surface, so a flag-parity pass changes no number here: `port
list` was counted from the start, but only carried 4 of upstream's 17 filters
until `055dc95`, and `subnet list` was counted while accepting no filters at all
until the history-parity pass. `image list` was counted while rejecting `--all`,
and every designate verb was counted while only some of them accepted
`--all-projects`, until the cross-project/name pass. When auditing a noun, diff
its flags against the upstream parser, not just its presence in these tables.

Two flag families have been diffed exhaustively across the whole tree, so a new
command that omits one is a regression rather than a known gap:

- **cross-project** — `--all`, `--all-projects`, `--all-tenants`, and the
  narrower `--all-properties` / `--all-tags` / `--all-rules` / `--all-supported`
  / `--all-stores` / `--target-all-projects`. Every command in this tree that
  upstream gives one of these now has it; `ALL_PROJECTS` in the environment
  defaults `--all-projects` on the compute and block-storage verbs upstream reads
  it for (`internal/cli/allprojects`).
- **`--name`** — both as a list filter and as the *create* spelling. Upstream
  python-designateclient and python-octaviaclient name a new resource with
  `--name` and take no positional for it, where koc grew the positional first;
  the affected create verbs accept either (`internal/cli/nameflag`), so the
  positional is optional in their usage strings. Note that `--name` is an
  **exact** match wherever the API makes it one, upstream and here alike — see
  `image list --name-contains` under "Naming deviations" for the one flag added
  because of it.

`internal/cli/dns/dns_test.go`, `internal/cli/server/flagparity_test.go` and
`internal/cli/loadbalancer/lb_test.go` assert the three lists by walking the
command tree, which is what keeps this claim true.

In-scope = OSC core (current API versions only — `identity.v2`, `volume.v2` and
`image.v1` are excluded as legacy) plus the five plugins above. 901 commands
including Swift + Manila; 844 excluding them, since `koc` targets neither.

## vs python-openstackclient (core)

| Namespace | Raw | Core (niche subsystems excluded) |
| --- | --- | --- |
| `openstack.compute.v2` | 73/100 (73%) | **71/88 (81%)** — `usage list/show` land outside the core denominator but are implemented |
| `openstack.image.v2` | 16/42 (38%) | **14/15 (93%)** — only `image member get` remains |
| `openstack.volume.v3` | 47/94 (50%) | **34/38 (89%)** — QoS and transfers are outside the "core" denominator but now implemented |
| `openstack.identity.v3` | 58/128 (45%) | **58/60 (97%)** — only `endpoint add/remove project` remain |
| `openstack.network.v2` | 102/165 (62%) | **85/94 (90%)** — QoS and RBAC land outside the "core" denominator but are implemented |
| `openstack.common` | 6/11 (55%) | 6/11 — `quota show/set`, `extension list/show`, `availability zone list`, `limits show` |
| `openstack.object_store.v1` (swift) | 0/17 | not targeted |
| `openstack.share.v2` (manila) | 0/40 | not targeted |

"Core" excludes, per namespace: compute — `compute agent`, `host`, `usage`,
`server share list/show`, `server dump` (12); identity — federation/IdP/mapping/
service provider, OAuth1 + EC2 credentials, trusts, limits, policies,
credentials, endpoint groups, access rules (68); image — metadefs, cached images,
tasks (27); network — QoS, metering, flavors, segment ranges, L3 conntrack
helpers, local IPs, RBAC, NDP proxies, auto-allocated topology, default SG
rules/statefulness (71); volume — `block storage *`, consistency groups, volume
groups, QoS, messages, backend capability/pools, `volume host failover/set`,
transfers (56). Nothing is excluded from `openstack.common`. A command that is
excluded from the denominator is excluded from the numerator too even when `koc`
implements it — hence the "land outside the core denominator" notes above.

Two things `koc` counts in `openstack.common` are spelled differently there:
`extension list` and `extension show` are reached as `koc network extension
list/show` (see "Naming deviations"). `usage list/show` are **compute.v2** entry
points, not common ones, and are counted in the compute row.

## vs OSC plugins

| Plugin | Coverage | Shape of the gap |
| --- | --- | --- |
| ironic (`baremetal`) | 52/118 (44%) | the full provision-state verb set, node lifecycle, power, ports, VIFs, BIOS settings, firmware, allocations, the driver and conductor nouns and stored inventory are solid; missing chassis, port groups, traits, history, deploy templates, runbooks, inspection rules, volume connectors/targets. Sequenced in `docs/proposals/coverage-tiers.md` |
| designate (`dns`) | **60/60 (100%)** | complete against `entry_points.txt`, diffed name-for-name — every upstream `openstack` dns command has a `koc` equivalent and no `koc` dns command is invented. `koc` additionally ships `dns pool list/show`, which designate's SDK supports but its CLI never exposed (see "koc-native commands") |
| python-octaviaclient (`load balancer`) | 62/82 (76%) | everything except availability zones and profiles (11 — `availabilityzone` ×6, `availabilityzoneprofile` ×5), eight of upstream's ten `unset` verbs (`loadbalancer`, `listener`, `pool`, `member`, `healthmonitor`, `l7policy`, `l7rule`, `flavor`; `quota unset` is implemented and `availability zone unset` is inside the 11) and `listener stats show` — 20 in all. Diffed name-for-name against `entry_points.txt`: all 62 `koc loadbalancer` leaves map to an upstream command, none is koc-invented |
| osc-placement | **31/31 (100%)** | complete against `entry_points.txt`, diffed name-for-name — resource providers, classes, traits, inventories, aggregates, allocations, allocation candidates and usages, reads and writes alike |
| python-ironic-inspector-client (`baremetal introspection`) | 6/13 (46%) | `start`, `status`, `list`, `abort`, `data save`, `interface list`; missing `interface show`, `reprocess` and the five `rule` verbs |

## vs gophercloud v2

`koc` imports **93 of 218** gophercloud service packages. Within services `koc`
already ships:

| Service | Packages used |
| --- | --- |
| `networking` | 23/50 |
| `identity` | 11/27 |
| `compute` | 15/20 |
| `blockstorage` | 11/24 |
| `baremetal` | 5/9 |
| `image` | 5/5 |
| `placement` | 6/6 |
| `dns` | 6/6 |
| `loadbalancer` | 10/13 |
| `baremetalintrospection` | 1/3 |

Ten services gophercloud supports have **zero** `koc` surface:
`sharedfilesystems` (14 pkgs), `orchestration` (7), `containerinfra` (6),
`db` (6), `objectstorage` (5), `keymanager` (4), `messaging` (3), `workflow` (3),
`metric` (1), `container` (1).

## Minimum supported cloud

**`koc` targets OpenStack Zed (2022.2) and newer.** The fleet includes old
clouds; a command that only works on the newest release is not done.

What each service's Zed release caps at, read from the Zed sdists on PyPI:

| Service | Zed version | Max microversion | Consequence |
| --- | --- | --- | --- |
| ironic | 21.4.0 | **1.82** | no `unhold` (1.85), `firmware` (1.86), `service` (1.87), child nodes (1.83), runbooks (1.92), inspection rules (1.96) |
| nova | 26.x | **2.93** | |
| cinder | 21.3.2 | **3.70** | |
| placement | 9.0.0 | **1.39** | |
| keystone, glance, neutron, designate, octavia | — | no microversions | capability is discovered per-extension (`network extension list`) |

`koc`'s defaults (`--os-*-api-version=latest`) already negotiate downward, so the
common path is safe. The hazard is the *version-gated* command: on Zed, ironic
answers `/v1/nodes/<n>/firmware` with a 404 because the endpoint does not exist
below 1.86, and a bare 404 reads like "no such node". Those commands route their
error through `internal/cli/baremetal.explainMicroversion`, which re-reads the
endpoint's version document and turns the 404/406 into "requires ironic API 1.86
(OpenStack 2023.2); this cloud supports up to 1.82".

The rule for new commands: **look up the microversion the API gated the feature
behind** (`ironic/api/controllers/v1/versions.py` in the sdist is the list) and,
if it is above the Zed cap in the table, wrap the call in that guard and say so
in the command's `Short`.

This is also why `python-ironic-inspector-client` is a baseline rather than a
deprecation note. Ironic removed the `inspector` inspect interface in 33.0.0
(the 2026.1 cycle) and moved inspection rules into its own API at 1.96, so on a
new cloud `baremetal node inspect` + `baremetal node inventory show` replace it.
On Zed the inspector is the only in-band inspection there is, so
`koc baremetal introspection …` stays.

## Prioritised gaps

Sequenced into commit-sized batches, with per-batch counts and a definition of
done, in **`docs/proposals/coverage-tiers.md`**. The tiers below are the
inventory; that document is the build order.

### Tier 1 — no vendor change (capability already in `vendor/`, just unwired)

**Empty — Tier 1 is fully implemented.** The 44 commands it listed shipped in
`docs/proposals/coverage-tiers.md` order: ironic provision verbs, node
read-outs, driver/conductor detail, identity writes, compute state verbs, and
the volume/network updates.

One entry was **dropped rather than implemented**: `security group unset`.
Tier 1 assumed it was a nil-update like `port unset`, but upstream's
`UnsetSecurityGroup` takes only `--tag`/`--all-tag` and does nothing else
(`openstackclient/network/v2/security_group.py`). Implementing it as a
nil-update would ship a command that shares upstream's name and does something
different — the deviation this project removed for `loadbalancer quota unset`.
It returns when security-group tag support does.

### Tier 2 — one `make tidy` (package exists upstream at the pinned v2.13.0)

**Empty — Tier 2 is fully implemented.** The last batch vendored
`networking/v2/extensions/{layer3/extraroutes,layer3/portforwarding,networkipavailabilities,rbacpolicies,segments,qos/policies,qos/ruletypes}`
for address scopes and groups, floating-IP port forwarding, `router add/remove
route`, `ip availability`, network RBAC, network segments and network QoS.

`qos/rules` was deliberately **not** vendored: every QoS rule type shares one
`{"<rule>": {…}}` envelope over `/qos/policies/{id}/<collection>`, and the typed
package models only three of the four types — it has no `minimum_packet_rate`
(neutron 2023.1) — so `internal/cli/network/qos.go` uses one raw path that
covers strictly more clouds than a four-way typed switch would.

### Tier 3 — no gophercloud package; needs a raw `ServiceClient` fallback

Glance metadefs and cached images; Cinder consistency groups, volume groups,
`block storage cluster/log level/manageable`; ironic **inspection rules** (API
1.96 — the replacement for the deleted introspection commands), chassis, port
groups, deploy templates, runbooks, traits, history, console, shards, volume
connectors/targets; Neutron metering, flavors, L3 conntrack helpers, local IPs,
NDP proxies, segment ranges, default SG rules; octavia availability zones and
profiles; all of Swift and Manila. (Designate is no longer on this list — the raw
fallback is written and its whole surface is covered; see below. Ironic VIFs and
BIOS settings are no longer here either — `baremetal/v1/nodes` carries typed
calls for both, so they are Tier 1.)

Already using it: `network extension list/show`, `quota show --default`
(compute), `loadbalancer quota defaults show`, `loadbalancer amphora
configure/delete/stats show`, `loadbalancer provider capability list`,
`loadbalancer flavor set --disable` (gophercloud tags the field `omitempty`, so a
`false` would be dropped), `network qos rule create/set/show/delete` (one raw
path per rule type, since `qos/rules` has no `minimum_packet_rate`), `dns quota reset` (gophercloud's `dns/v2/quotas` is
Get/Update only — no `DELETE /v2/quotas/<project>`), and **30 of the 60 designate
commands** — `zone export/import`, `zone blacklist`, `tld`, `zone
abandon/axfr/move`, `zone nameservers list`, `ptr record`, `dns pool`, `dns
service`, `dns limit list` — for which gophercloud has no package at all. Those
share the helpers in `internal/cli/dns/raw.go`: JSON GET/POST/PATCH/DELETE, a
`links.next` page walk standing in for the missing Pager, and designate's
`--all-projects`/`--sudo-project-id` header shim.

Follow the AGENTS.md raw-fallback rule: isolate behind a small helper, pin the
microversion, and comment why the typed package is unavailable.

## Naming deviations from upstream

`koc` groups some nouns differently from upstream. Since the history-parity pass
**the upstream spelling also works** for the first four — they are registered as
additional commands (or cobra aliases) running the same code, so a script written
against `openstack` is not broken by the deviation. The `koc` spelling is listed
first because it is what `--help` shows.

| `koc` | upstream | upstream spelling accepted? |
| --- | --- | --- |
| `koc server console log show` | `openstack console log show` | yes — `koc console log show` |
| `koc server console url show` | `openstack console url show` | yes — `koc console url show` |
| `koc baremetal node power reboot` | `openstack baremetal node reboot` | yes (hidden duplicate) |
| `koc recordset set` | `openstack recordset set` (designate CLI: `update`) | yes — `update` cobra alias |
| `koc server migration list` | `openstack server migration list` | also as `koc migration list` (hidden) |
| `koc network extension list` / `show` | `openstack extension list --network` / `extension show` | no |
| `koc network trunk subport list` | `openstack network subport list` | no |

One deviation was **removed** rather than documented: `koc loadbalancer quota
unset` used to take no flags and clear all seven quotas, which is upstream's
`quota reset`, not its `quota unset`. It now takes the seven upstream boolean
flags (`--loadbalancer`, `--listener`, `--pool`, `--member`, `--healthmonitor`,
`--l7policy`, `--l7rule`) and clears only the quotas named, and the
clear-everything behaviour moved to the new `koc loadbalancer quota reset`. This
is a breaking change for anyone who relied on the old flagless `unset`.

One flag deviates rather than a command: `koc dns service list --service-name`,
where upstream spells the same filter `--service_name` — the only underscored flag
in designate's CLI. Both work; the underscored form is registered hidden.

One flag is **koc-native**: `koc image list --name-contains`, a case-insensitive
substring filter applied client-side. Glance's query builder accepts only `in:`
and `eq:` on `name` and rejects every other operator
(`glance/db/sqlalchemy/api.py _make_conditions_from_filters`), so `--name sber`
is not a narrow search but a lookup for an image called literally "sber" — it
returns an empty table, which reads as "no such images". `--name` therefore keeps
upstream's exact semantics (`koc image list --name X` must return what `openstack
image list --name X` returns) and the substring search gets its own flag. The
`-contains` suffix follows ironic's `baremetal node list --description-contains`
rather than manila's `--name~`, since manila is not a service `koc` targets. The
same trap exists on other nouns whose API cannot do substring matching — `volume
list`, `network list`, `port list`, `subnet list` — and they have no equivalent
flag yet; nova is the exception, as `server list --name` is a server-side regex.

`--timing` deviates in **where it writes**, deliberately.
`osc_lib/command/timing.py` is a cliff Lister: it prints a "URL | Seconds"
table to **stdout**, ending in a Total row. `koc --timing` prints one plain
line per request to **stderr**, plus the same total. Timing output on stdout
would corrupt `koc … -f json | jq` and `koc … -f value > file`, which is
precisely what koc's output layer exists to guarantee; upstream can afford it
because its table is itself a cliff formatter, whereas koc's `-f` applies to the
command's result and not to its diagnostics.

`-f value` deviates in its **separator**, deliberately. `cliff`'s
`ValueFormatter` joins a row's cells with a single **space**; `koc` joins them
with a **tab** (`internal/output/output.go`, documented at the package comment,
and in README "Output formats"). A tab is unambiguous where a space is not — most
OpenStack values that show up in `-f value` output contain spaces (status
strings, flavor names, `Fixed IP Addresses`), so a space-joined row cannot be
split back into its cells. The cost is real and one-directional: the common
upstream idiom `openstack … -f value | cut -d' ' -f2` silently returns the wrong
field under `koc`. Use `cut -f2` (tab is `cut`'s default delimiter), `awk '{print
$2}'` (which splits on either), or `-c <column>` to select a single column and
avoid the question. Not changed, because a space separator would trade a loud
script fix for a quiet ambiguity in the data.

Cells in `-f value` are otherwise **unescaped and unquoted**, so a value that
itself contains a tab or a newline breaks the one-row-per-line, one-cell-per-tab
contract. `cliff` has the same weakness, so this is parity rather than a
deviation, but it is worth stating because `-f value` is the format scripts
consume. Newlines surviving is deliberate — `koc zone export showfile <id>
-f value > zone.txt` has to emit a zonefile that `zone import create` reads
back, which collapsing them would destroy (`internal/output/output.go
writeValue`). `koc` does strip control characters and ANSI escapes that upstream
passes through, so a hostile endpoint cannot rewrite the operator's terminal.
`-f csv` (RFC 4180 quoting) or `-f json` is the safe choice when a field may hold
arbitrary text — image descriptions, `properties`, server metadata.

**Zero-match name resolution passes the literal ref to the API**, rather than
erroring. `resolve.pick`/`pickID` implement one match → its ID, many → an error,
and **zero → the reference unchanged** (`internal/cli/resolve/resolve.go`). So
`koc network delete typo-name` issues `DELETE /v2.0/networks/typo-name` and the
operator sees neutron's 404 for a malformed UUID instead of koc's "no network
named …". Upstream OSC resolves the name itself and reports the miss before
issuing anything. This is inconsistent inside `koc` too: the `server` package
does it properly — `no server found with name "…"` — while every other
resolver falls through. Deliberately left as-is in this pass: the passthrough is
what lets an ID be given wherever a name is accepted without a per-command
`--id`, and on read paths (`--project`, `--domain` filters) it degrades to an
empty result rather than a wrong one. Also recorded in README "Known
limitations"; the fix is to make the other resolvers match `server`'s behaviour.

## koc-native commands

No upstream equivalent, by design — **35 leaves**, itemised so the total
reconciles with the headline (548 = 513 + 35):

| Count | Commands | Why it has no upstream equivalent |
| --- | --- | --- |
| 18 | `koc keyvrm …` — `app-config` ×2, `availability-zone` ×2, `event` ×4, `host-aggregate-config` ×5, `recommendation` ×5 | in-house KeyVRM catalog service; no gophercloud package and no OSC plugin |
| 5 | `koc vault kv list/get/copy/export/decrypt` | Vault is not an OpenStack service; `copy` fills a gap in the Vault CLI itself |
| 2 | `koc dns pool list/show` | designate's API and its Python SDK both expose `/v2/pools`, but `python-designateclient` registers no `openstack` command for it. Reads only — pool *writes* are a `designate-manage`/config operation on the servers |
| 2 | `koc server add/remove server-group` | KeyStack dynamic server groups |
| 2 | `koc network trunk subport add`/`remove` | upstream folds these into `network trunk set`/`unset --subport` flags rather than giving them verbs (`network subport list` does exist and is counted — see "Naming deviations") |
| 2 | `koc server console log show`, `koc server console url show` | the `koc` spellings of upstream `console log/url show`; the upstream form is the primary one and carries the count, so these two are extras |
| 1 | `koc image member set` | accept/reject/pending on an image membership. Upstream registers `image member get`/`list` only — the status write has no entry point |
| 1 | `koc baremetal node inventory show` | a table summary of the inventory upstream only offers as a raw `save` |
| 1 | `koc server password show` | see below |
| 1 | `koc volume extend` | `cinder extend` by its cinderclient name. OSC folds the resize into `volume set --size`, which `koc` also accepts (and which is what the `volume_set` entry point is counted against), so this is an extra verb rather than a deviation |

`server password show` is the read side of nova's `os-server-password`, i.e.
`nova get-password`. OSC never ported it — `python_openstackclient` 10.2.1
registers no server-password entry point at all — but the API is the only way to
recover the administrator password of a server whose guest agent generated one,
so the gap is upstream's, not the cloud's. The write side is already reachable:
`nova clear-password` is `koc server set --no-password`, and changing the
password is `koc server set --password`.

One **global flag** is koc-native too, and is deliberately not in the counts
above (the tables measure commands, not flags): `--timeout` / `OS_TIMEOUT` caps a
whole HTTP request/response exchange on every client `koc` builds. OSC has no
global equivalent — keystoneauth carries a session `timeout`, but
`python-openstackclient` registers no flag for it, so an operator's only recourse
upstream is `clouds.yaml`. See README "Timeouts" for the semantics.

## Updating this document

The tables are derived, not hand-maintained. To re-derive after a version bump
or a batch of new commands:

```sh
# 1. koc's own command tree (548 leaf commands at the snapshot above)
make build
# Walk `--help` recursively. Count a command when it is *runnable*, not merely when
# it is childless: `koc image import <image>` is a verb that also parents `koc image
# import info`. Read runnability off the Usage block — a pure group's only usage
# form is "<path> [command]".
# Exclude cobra's five built-ins — `help` and `completion {bash,zsh,fish,powershell}`
# appear under Available Commands but are not koc surface. Forgetting them is a
# reliable +5.

# 2. upstream baselines — PyPI is reachable where opendev.org is not
pip download --no-deps --no-binary :all: python-openstackclient -d osc
tar xzf osc/python_openstackclient-*.tar.gz
awk '/^\[/{s=$0} /^[a-z0-9_]+ *=/{if (s ~ /^\[openstack\./) print s"\t"$1}' \
  python_openstackclient-*/python_openstackclient.egg-info/entry_points.txt
# repeat for python-ironicclient, python-designateclient, python-octaviaclient,
# osc-placement, python-ironic-inspector-client

# 3. gophercloud surface vs what koc imports
curl -sSo gc.zip https://proxy.golang.org/github.com/gophercloud/gophercloud/v2/@v/v2.13.0.zip
unzip -Z1 gc.zip | grep -E 'openstack/.*\.go$' | sed 's|/[^/]*\.go$||' | sort -u
grep -rho 'github.com/gophercloud/gophercloud/v2/openstack/[a-z0-9/]*' \
  --include='*.go' internal cmd | sort -u
# (subtract openstack/config and openstack/config/clouds: not service packages)
```

Then **check the arithmetic**, because that is the only thing that makes these
tables worth reading. Three identities must hold at every snapshot:

1. every raw row numerator summed = the headline numerator (513);
2. leaf commands = headline numerator + koc-native (548 = 513 + 35);
3. every raw row denominator summed = 901, and minus the two not-targeted rows
   (swift 17 + manila 40) = the in-scope denominator (844).

A row whose numerator is asserted rather than diffed will break (1) or (2). If
either fails, the mechanical diff is right and the row is wrong — fix the row.
Two adjustments are legitimate and must be applied by hand after the diff:
**naming deviations**, where a `koc` leaf matches an upstream entry point under a
different spelling (currently `baremetal node power reboot` → `baremetal node
reboot`, `network extension list/show` → `extension list/show`, `network trunk
subport list` → `network subport list`), and the **core columns**, where a command
excluded from the denominator must also leave the numerator.

Normalise both sides to underscore form (`floating ip create` →
`floating_ip_create`) before diffing; entry points join every word with `_`.
Watch for false negatives from the naming deviations listed above.

This is a documentation-only workflow and needs network. It is **not** part of
the build, so the air-gap invariant (`GOPROXY=off`, `-mod=vendor`) is unaffected.
