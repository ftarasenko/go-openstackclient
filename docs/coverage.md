# Command coverage

How much of the upstream OpenStack CLI surface `koc` implements, measured against
primary sources rather than documentation.

**Snapshot:** 2026-08-06 · `koc` @ `claude/history-parity-openstack-mcjryb` · 387 leaf commands.

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

Entry points are the authoritative command list — `openstack`'s own docs lag the
code, and `opendev.org` is unreachable from CI/agent environments (HTTP 403), so
PyPI is the source of record.

## Headline

**359 of 831 in-scope upstream commands (43%).** Of `koc`'s 387 leaf commands,
~360 are upstream-equivalent and 27 are koc-native.

`python-octaviaclient` became a baseline during the history-parity pass, adding
its 82 commands to the denominator; measured against the previous four baselines
the figure is 299/749 (40%).

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
`image.v1` are excluded as legacy) plus the four plugins above. 888 commands
including Swift + Manila; 831 excluding them, since `koc` targets neither.

## vs python-openstackclient (core)

| Namespace | Raw | Core (niche subsystems excluded) |
| --- | --- | --- |
| `openstack.compute.v2` | 60/100 (60%) | **60/88 (68%)** |
| `openstack.image.v2` | 12/42 (29%) | **12/15 (80%)** |
| `openstack.volume.v3` | 31/94 (33%) | **31/38 (82%)** |
| `openstack.identity.v3` | 42/128 (33%) | **42/60 (70%)** |
| `openstack.network.v2` | 57/165 (35%) | **57/92 (62%)** |
| `openstack.common` | 4/11 (36%) | 4/11 — `quota show/set`, `extension list/show` |
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
| ironic (`baremetal`) | 35/118 (30%) | node lifecycle, power, ports, driver details, stored inventory and inspector introspection are solid; missing allocations, chassis, port groups, traits, VIFs, BIOS settings, history, deploy templates, runbooks, inspection rules, introspection reprocess, volume connectors/targets |
| designate (`dns`) | 49/60 (82%) | zone + recordset CRUD, zone shares, zone transfer requests/accepts, zone exports/imports, blacklists, TLDs, `dns quota` and TSIG keys. Diffed name-for-name against `entry_points.txt`: every `koc` dns leaf maps to an upstream command, none is koc-invented. Missing: PTR records, `dns service`, `dns limit list`, `zone abandon/axfr/move`, `zone nameservers list` |
| python-octaviaclient (`load balancer`) | 60/82 (73%) | everything except availability zones and profiles (11), the eight `unset` verbs, `listener stats show`, `quota list` and `quota reset`. Diffed name-for-name against `entry_points.txt`: every `koc loadbalancer` leaf maps to an upstream command, none is koc-invented |
| osc-placement | 10/31 (32%) | read-only resource providers, traits, inventories, per-provider usages and aggregates; no inventory *writes*, resource classes, project/user usages, allocation candidates |

## vs gophercloud v2

`koc` imports **70 of 218** gophercloud service packages. Within services `koc`
already ships:

| Service | Packages used |
| --- | --- |
| `networking` | 14/50 |
| `identity` | 11/27 |
| `compute` | 10/20 |
| `blockstorage` | 7/24 |
| `baremetal` | 4/9 |
| `baremetalintrospection` | 1/3 |
| `image` | 4/5 |
| `placement` | 3/6 |
| `dns` | 6/6 |
| `loadbalancer` | 10/13 |

Ten services gophercloud supports have **zero** `koc` surface:
`sharedfilesystems` (14 pkgs), `orchestration` (7), `containerinfra` (6),
`db` (6), `objectstorage` (5), `keymanager` (4), `messaging` (3), `workflow` (3),
`metric` (1), `container` (1).

## Prioritised gaps

### Tier 1 — no vendor change (capability already in `vendor/`, just unwired)

| Vendored package | Unlocks |
| --- | --- |
| `identity/v3/roles` (Create/Update/Delete + role-inference rules) | `role create/delete/set`, `implied role create/delete/list` |
| `compute/v2/servers` (`Shelve`/`Unshelve`/`Rescue`/`Unrescue`/`CreateImage`/`GetPassword`) | `server shelve/unshelve/rescue/unrescue`, `server image create` |
| `identity/v3/{regions,services,users,tokens,catalog}` | `region create/delete/set/show`, `service create/delete/set`, `user password set`, `token revoke`, `catalog show` |
| `blockstorage/v3/{snapshots,backups}` (`Update`) | `volume snapshot set/unset`, `volume backup set/unset` |
| `networking/v2/{subnets,security/groups}` (nil-update) | `subnet unset`, `security group unset` (`port unset` is now wired) |
| `networking/v2/extensions/layer3/routers` (`GatewayInfo`) | `router add/remove gateway` |

### Tier 2 — one `make tidy` (package exists upstream at the pinned v2.13.0)

- `compute/v2/servergroups` → `server group create/delete/list/show`
- `compute/v2/attachinterfaces` → `server add/remove port|network|fixed ip`
- `compute/v2/usage` → `usage list/show`
- `compute/v2/availabilityzones` + `blockstorage/v3/availabilityzones` → `availability zone list`
- `compute/v2/limits` + `blockstorage/v3/limits` → `limits show`
- `blockstorage/v3/{transfers,qos}` → 12+ volume commands (`attachments` and
  `quotasets` are now wired — `volume attachment *`, `quota show/set`)
- `networking/v2/extensions/{layer3/addressscopes,security/addressgroups,layer3/portforwarding,layer3/extraroutes,networkipavailabilities,qos/*,rbacpolicies,segments}` (`subnetpools` and `trunks` are now wired)
- `image/v2/tasks` → `image task list/show` (`imageimport` is now wired — `image import`, `image import info`; `image stage` and `image stores list` remain)
- `baremetal/v1/allocations` → `baremetal allocation *`
- `placement/v1/{resourceclasses,usages,allocationcandidates}`

### Tier 3 — no gophercloud package; needs a raw `ServiceClient` fallback

Glance metadefs and cached images; Cinder consistency groups, volume groups,
`block storage cluster/log level/manageable`; ironic chassis, port groups, deploy
templates, runbooks, traits, VIFs, BIOS, history, volume connectors/targets;
designate PTR records and service statuses; Neutron metering, flavors, L3
conntrack helpers, local IPs, NDP proxies, segment ranges, default SG rules;
octavia availability zones and profiles; all of Swift and Manila.

Already using it: `network extension list/show`, `quota show --default`
(compute), `loadbalancer quota defaults show`, `loadbalancer amphora
configure/delete/stats show`, `loadbalancer provider capability list`,
`loadbalancer flavor set --disable` (gophercloud tags the field `omitempty`, so a
`false` would be dropped), `dns quota reset` (gophercloud's `dns/v2/quotas` is
Get/Update only — no `DELETE /v2/quotas/<project>`), and the whole of
`zone export/import`, `zone blacklist` and `tld` (no gophercloud package at all —
shared helpers in `internal/cli/dns/raw.go`, which also carries designate's
`--all-projects`/`--sudo-project-id` header shim).

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

## koc-native commands

No upstream equivalent, by design: 14 `koc keyvrm …` (in-house KeyVRM catalog
service), 5 `koc vault kv …`, `koc server add/remove server-group` (KeyStack
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
# 1. koc's own command tree (245 leaf commands at the snapshot above)
make build
# walk `--help` recursively, collecting leaves under "Available Commands:"

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
