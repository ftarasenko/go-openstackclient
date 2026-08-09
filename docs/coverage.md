# Command coverage

How much of the upstream OpenStack CLI surface `koc` implements, measured against
primary sources rather than documentation.

**Snapshot:** 2026-08-08 · `koc` @ this commit (base `d38f1a8`) · 516 leaf
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

**487 of 844 in-scope upstream commands (58%).** Of `koc`'s 516 leaf commands,
487 are upstream-equivalent and 29 are koc-native.

The denominator grew by 13 against the 2026-08-07 snapshot without a single
command changing: `python-ironic-inspector-client` is now a **baseline** rather
than an untracked extra. Its 13 `baremetal introspection` commands used to be
scored against the `python-ironicclient` baseline, which never contained them —
so the ironic row read an inflated 35/118 when the honest split is **29/118 for
ironic and 6/13 for the inspector**. Same six commands, two correct rows.

`python-octaviaclient` became a baseline during the history-parity pass, adding
its 82 commands to the denominator; measured against the previous four baselines
the figure is 305/749 (41%).

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
those, `koc` covers **52–79%** of each core service — the commands operators
actually run daily. Both numbers are reported below; neither alone is honest.

Counts are **leaf commands, not flags**. A command can be present and still lag
upstream's option surface, so a flag-parity pass changes no number here: `port
list` was counted from the start, but only carried 4 of upstream's 17 filters
until `055dc95`, and `subnet list` was counted while accepting no filters at all
until the history-parity pass. When auditing a noun, diff its flags against the
upstream parser, not just its presence in these tables.

In-scope = OSC core (current API versions only — `identity.v2`, `volume.v2` and
`image.v1` are excluded as legacy) plus the five plugins above. 901 commands
including Swift + Manila; 844 excluding them, since `koc` targets neither.

## vs python-openstackclient (core)

| Namespace | Raw | Core (niche subsystems excluded) |
| --- | --- | --- |
| `openstack.compute.v2` | 75/100 (75%) | **75/88 (85%)** |
| `openstack.image.v2` | 16/42 (38%) | **15/15 (100%)** — `image stage` and `stores list` land outside the core denominator |
| `openstack.volume.v3` | 48/94 (51%) | **35/38 (92%)** — QoS and transfers are outside the "core" denominator but now implemented |
| `openstack.identity.v3` | 58/128 (45%) | **58/60 (97%)** — only `endpoint add/remove project` remain |
| `openstack.network.v2` | 71/165 (43%) | **60/92 (65%)** — address scopes and groups land outside the "core" denominator |
| `openstack.common` | 8/11 (73%) | 8/11 — `quota show/set`, `extension list/show`, `availability zone list`, `limits show`, `usage list/show` |
| `openstack.object_store.v1` (swift) | 0/17 | not targeted |
| `openstack.share.v2` (manila) | 0/40 | not targeted |

"Core" excludes, per namespace: compute — `compute agent`, `host`, `usage`,
`server share`, `server dump`; identity — federation/IdP/mapping/service
provider, OAuth1 + EC2 credentials, trusts, limits, policies, credentials,
endpoint groups, access rules; image — metadefs, cached images, tasks; network —
QoS, metering, flavors, segment ranges, L3 conntrack helpers, local IPs,
RBAC, NDP proxies, auto-allocated topology, default SG rules/statefulness;
volume — `block storage *`, consistency groups, volume groups, QoS, messages,
backend capability/pools, host failover, transfers.

## vs OSC plugins

| Plugin | Coverage | Shape of the gap |
| --- | --- | --- |
| ironic (`baremetal`) | 52/118 (44%) | the full provision-state verb set, node lifecycle, power, ports, VIFs, BIOS settings, firmware, allocations, the driver and conductor nouns and stored inventory are solid; missing chassis, port groups, traits, history, deploy templates, runbooks, inspection rules, volume connectors/targets. Sequenced in `docs/proposals/coverage-tiers.md` |
| designate (`dns`) | **60/60 (100%)** | complete against `entry_points.txt`, diffed name-for-name — every upstream `openstack` dns command has a `koc` equivalent and no `koc` dns command is invented. `koc` additionally ships `dns pool list/show`, which designate's SDK supports but its CLI never exposed (see "koc-native commands") |
| python-octaviaclient (`load balancer`) | 63/82 (77%) | everything except availability zones and profiles (11), seven of the eight `unset` verbs (`quota unset` is implemented) and `listener stats show`. Diffed name-for-name against `entry_points.txt`: every `koc loadbalancer` leaf maps to an upstream command, none is koc-invented |
| osc-placement | **31/31 (100%)** | complete against `entry_points.txt`, diffed name-for-name — resource providers, classes, traits, inventories, aggregates, allocations, allocation candidates and usages, reads and writes alike |
| python-ironic-inspector-client (`baremetal introspection`) | 6/13 (46%) | `start`, `status`, `list`, `abort`, `data save`, `interface list`; missing `interface show`, `reprocess` and the five `rule` verbs |

## vs gophercloud v2

`koc` imports **86 of 218** gophercloud service packages. Within services `koc`
already ships:

| Service | Packages used |
| --- | --- |
| `networking` | 16/50 |
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

- `networking/v2/extensions/{layer3/addressscopes,security/addressgroups,layer3/portforwarding,layer3/extraroutes,networkipavailabilities,qos/*,rbacpolicies,segments}` (`subnetpools` and `trunks` are now wired)

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
`false` would be dropped), `dns quota reset` (gophercloud's `dns/v2/quotas` is
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

`--timing` deviates in **where it writes**, deliberately.
`osc_lib/command/timing.py` is a cliff Lister: it prints a "URL | Seconds"
table to **stdout**, ending in a Total row. `koc --timing` prints one plain
line per request to **stderr**, plus the same total. Timing output on stdout
would corrupt `koc … -f json | jq` and `koc … -f value > file`, which is
precisely what koc's output layer exists to guarantee; upstream can afford it
because its table is itself a cliff formatter, whereas koc's `-f` applies to the
command's result and not to its diagnostics.

## koc-native commands

No upstream equivalent, by design: 14 `koc keyvrm …` (in-house KeyVRM catalog
service), 5 `koc vault kv …`, `koc dns pool list/show` (designate's API and its
Python SDK both expose `/v2/pools`, but `python-designateclient` registers no
`openstack` command for it; reads only, since pool *writes* are a
`designate-manage`/config operation on the servers), `koc server add/remove server-group` (KeyStack
dynamic server groups), `koc image member set`, `koc baremetal node inventory show`
(a table summary of the inventory upstream only offers as a raw `save`), and
`koc network trunk subport add`/`remove` (upstream folds these into
`network trunk set`/`unset --subport` flags rather than giving them verbs). The
two `koc server console …` spellings also count here now that the upstream
`console …` form is the primary one.

## Updating this document

The tables are derived, not hand-maintained. To re-derive after a version bump
or a batch of new commands:

```sh
# 1. koc's own command tree (397 leaf commands at the snapshot above)
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
# osc-placement

# 3. gophercloud surface vs what koc imports
curl -sSo gc.zip https://proxy.golang.org/github.com/gophercloud/gophercloud/v2/@v/v2.13.0.zip
unzip -Z1 gc.zip | grep -E 'openstack/.*\.go$' | sed 's|/[^/]*\.go$||' | sort -u
grep -rho 'github.com/gophercloud/gophercloud/v2/openstack/[a-z0-9/]*' \
  --include='*.go' internal cmd | sort -u
```

Normalise both sides to underscore form (`floating ip create` →
`floating_ip_create`) before diffing; entry points join every word with `_`.
Watch for false negatives from the naming deviations listed above.

This is a documentation-only workflow and needs network. It is **not** part of
the build, so the air-gap invariant (`GOPROXY=off`, `-mod=vendor`) is unaffected.
