# Cloud probe: release spread + Tier 1/2/3 feasibility

Prompt for the follow-up session that has access to the real KeyStack cloud. It
covers branch `claude/ironic-coverage-tier-plan-4h7zhh`.

Unlike `2026-08-07-parity-pass.md`, this run is **non-destructive and almost
entirely read-only**. It answers two questions that cannot be answered offline:

1. **Where in the Zed-to-current range does this cloud sit, and which inspection
   dialect does it speak?** (§1 — this decides which commands need a guard.)
2. **Which of the planned Tier 1/2/3 APIs does this cloud actually expose?**
   (§3 — this sets the build order in `docs/proposals/coverage-tiers.md`.)

Plus a smoke pass (§2) over the surface the tier work builds on.

**Run this once per distinct OpenStack release in the fleet.** `koc` supports
Zed (2022.2) and newer; the whole point is to find the oldest cloud's ceiling,
and one modern cloud's answers tell you nothing about it. Label each run with
the release it came from.

---

## How to use this document

You are probing, not developing. For each check:

1. Run it against the real cloud.
2. Compare against **Expected**.
3. Record PASS / FAIL / BLOCKED with the actual output pasted in.

Rules:

- **Nothing from this run gets committed.** Real endpoints, host names, project
  and node UUIDs, tokens and `--debug` captures are private data and the repo is
  public (AGENTS.md → "Private data never leaves the org"). Report *findings* —
  "1.96 supported", "no `baremetal-introspection` in the catalog", "chassis
  endpoint returns 404" — and keep the evidence in the conversation.
- Add `--debug` when the check is about a request shape. It redacts tokens; it
  does **not** redact host names, so redact those yourself before pasting.
- Checks marked **WRITE** create or change something. Each says how to undo it.
  Run them only in a project you own. Skip any you are not comfortable running
  and record BLOCKED — a skipped write is a fine outcome, a surprise write is
  not.
- If reality disagrees with Expected, **reality wins**: record the real payload
  and say which part of the plan needs revisiting.

Build first, from the branch:

```sh
make build && ./koc --version
```

---

## §0 — Baseline

**0.1 Auth and catalog**

```sh
./koc catalog list
```

**Record:** the list of service *types* only (not URLs). Specifically whether
each of these is present: `baremetal`, `baremetal-introspection`, `compute`,
`volume` / `volumev3`, `image`, `network`, `identity`, `placement`, `dns`,
`load-balancer`, `keyvrm`.

**0.2 A token and the ironic endpoint, for the raw probes below**

```sh
export OS_TOKEN=$(./koc token issue -c id -f value)
export IRONIC=$(./koc catalog show baremetal -f json)   # read the public URL out of this
```

Everything in §3 that `koc` has no command for is probed with
`curl -s -o /dev/null -w '%{http_code}\n' -H "X-Auth-Token: $OS_TOKEN" …`.
Report only the status code, never the URL.

---

## §1 — Where does this cloud sit, and which inspection dialect?

`koc` supports Zed (2022.2) and newer. This section establishes the ceiling that
every version-gated command has to respect.

**1.1 Service versions and microversion ceilings**

```sh
curl -s -H "X-Auth-Token: $OS_TOKEN" "$IRONIC_URL/v1/"    | python3 -m json.tool
curl -s -H "X-Auth-Token: $OS_TOKEN" "$NOVA_URL/"         | python3 -m json.tool
curl -s -H "X-Auth-Token: $OS_TOKEN" "$CINDER_URL/"       | python3 -m json.tool
curl -s -H "X-Auth-Token: $OS_TOKEN" "$PLACEMENT_URL/"    | python3 -m json.tool
```

**Record** `min_version` / `max_version` per service — nothing else from these
documents. Compare against the Zed floor:

| Service | Zed max | This cloud | ≥ Zed? |
| --- | --- | --- | --- |
| ironic | 1.82 | | |
| nova | 2.93 | | |
| cinder | 3.70 | | |
| placement | 1.39 | | |

**Expected:** every value ≥ the Zed column. **A value *below* it is a finding** —
`koc` claims Zed as the floor, and a cloud under it means the floor is wrong and
`docs/coverage.md` → "Minimum supported cloud" has to move.

Then mark which of these the cloud reaches, since each gates a planned command:

| Ironic microversion | Gates | Available? |
| --- | --- | --- |
| 1.83 | `node children list` | |
| 1.85 | `node unhold` | |
| 1.86 | `node firmware list` | |
| 1.87 | `node service` | |
| 1.92 | runbooks (T3.4) | |
| 1.96 | inspection rules (T3.1) | |

**1.2 Which inspection dialect does this cloud speak?**

From §0.1: is there a service of type `baremetal-introspection`? And:

```sh
./koc baremetal node show <node> -f json    # read inspect_interface
```

| Observation | Meaning for T3.1 |
| --- | --- |
| `baremetal-introspection` in the catalog, nodes on `inspect_interface: inspector` | inspector cloud — the `baremetal introspection` commands are the live path here |
| no inspector service, nodes on `agent` / `redfish`, ironic ≥ 1.96 | ironic-native cloud — `baremetal inspection rule *` is the live path |
| inspector deployed but nodes on `agent` | transitional — both paths must work |

All three are expected somewhere in the fleet. That is precisely why both
dialects ship.

**1.3 Does inspection work end-to-end?**

Pick a node safe to inspect, or record BLOCKED — inspection power-cycles the
node, so this is **WRITE** in the physical sense. Run whichever matches 1.2:

```sh
./koc baremetal node inspect <node> --wait      # ironic-native
./koc baremetal node inventory show <node>

./koc baremetal introspection start <node>      # inspector
./koc baremetal introspection status <node>
./koc baremetal introspection interface list <node>
```

**Expected:** the node returns to `manageable`, and the data command renders a
table. **Undo:** none needed.

---

## §2 — Smoke over the surface the tier work builds on

**2.1 Baremetal (36 commands)**

```sh
./koc baremetal node list
./koc baremetal node show <node>
./koc baremetal node list -f json | head -40
./koc baremetal port list
./koc baremetal driver list ; ./koc baremetal driver show <driver>
./koc baremetal conductor list
./koc baremetal node inventory show <node>
./koc baremetal introspection list
```

**Expected:** `node list` renders UUID / Name / Instance UUID / Power State /
Provisioning State / Maintenance. On an ironic-native cloud `introspection list`
will fail at client construction — record the exact message; it should name the
missing `baremetal-introspection` catalog entry, not stack-trace.

**2.2 Standalone ironic path** (only if you have a cluster node)

```sh
./koc --creds-from-ns <ironic-namespace> baremetal node list
```

**2.3 Everything else**

```sh
./koc server list ; ./koc volume list ; ./koc network list ; ./koc image list
./koc zone list ; ./koc loadbalancer list ; ./koc resource provider list
```

**Expected:** unchanged. This is a smoke test, not a parity check.

---

## §3 — Capability probe: which planned APIs exist here?

For each row: run the probe, record the HTTP status (or the `koc`/`openstack`
output), and mark **YES** (endpoint exists), **NO** (404 / not-implemented), or
**403** (exists but this account cannot see it — still a YES for planning).

A `404` means "do not build it yet". A `403` means "build it, test with an admin
account". Anything else, paste the code.

### 3.1 Tier 1 — no probe needed, but confirm the two riskiest

Tier 1 uses APIs `koc` already talks to, so it is safe by construction. Two
exceptions worth confirming:

| Check | Command | Expected |
| --- | --- | --- |
| ironic VIF support | `curl -s -o /dev/null -w '%{http_code}\n' -H "X-Auth-Token: $OS_TOKEN" "$IRONIC_URL/v1/nodes/<node>/vifs"` | 200 |
| ironic BIOS settings | `… "$IRONIC_URL/v1/nodes/<node>/bios"` | 200 (404 on drivers with no BIOS interface — record which driver) |

Also confirm the identity account can write, since T1.4 is 15 identity writes:

```sh
./koc role list ; ./koc region list ; ./koc service list
```

**Record:** whether the account is admin-scoped. If not, T1.4 lands untested
against the real cloud and that must be stated when it ships.

### 3.2 Tier 2 — one probe per batch

| Batch | Probe | Notes |
| --- | --- | --- |
| T2.1 server groups | `openstack server group list` **or** `GET <nova>/os-server-groups` | |
| T2.2 attach interfaces | `GET <nova>/servers/<id>/os-interface` | pick a server you own |
| T2.3 AZ + limits | `GET <nova>/os-availability-zone`, `GET <nova>/limits`, `GET <cinder>/limits` | |
| T2.4 usage | `GET <nova>/os-simple-tenant-usage` | often admin-only → 403 is fine |
| T2.5 ironic allocations | `GET $IRONIC_URL/v1/allocations` | needs API ≥ 1.52 |
| T2.6 volume transfers | `GET <cinder>/volume-transfers` | |
| T2.7 volume QoS | `GET <cinder>/qos-specs` | admin-only on most clouds |
| T2.8 image tasks / stores | `GET <glance>/v2/tasks`, `GET <glance>/v2/info/stores` | `stores` 404 ⇒ no multi-store, drop `image stores list` |
| T2.9 placement | `GET <placement>/resource_classes`, `GET <placement>/allocation_candidates?resources=VCPU:1` | |
| T2.10 neutron extensions | `./koc network extension list` | **the single most useful probe in this document** |

For T2.10, take the `alias` column of `network extension list` and report which
of these are present — each maps to one sub-batch:

`address-scope` · `address-group` · `floating-ip-port-forwarding` ·
`extraroute` (and `extraroute-atomic`) · `network-ip-availability` ·
`rbac-policies` · `segment` · `qos` (and `qos-rules-*`)

Anything absent gets dropped from T2.10 rather than built blind.

### 3.3 Tier 3 — which ironic nouns are real here?

All probed the same way, against `$IRONIC_URL`:

| Batch | Path | Min microversion |
| --- | --- | --- |
| T3.1 inspection rules | `/v1/inspection_rules` | 1.96 |
| T3.2 chassis | `/v1/chassis` | 1.1 |
| T3.2 port groups | `/v1/portgroups` | 1.23 |
| T3.2 volume connectors | `/v1/volume/connectors` | 1.32 |
| T3.2 volume targets | `/v1/volume/targets` | 1.32 |
| T3.3 node history | `/v1/nodes/<node>/history` | 1.78 |
| T3.3 shards | `/v1/shards` | 1.82 |
| T3.4 deploy templates | `/v1/deploy_templates` | 1.55 |
| T3.4 runbooks | `/v1/runbooks` | 1.92 |

Send each with `-H "X-OpenStack-Ironic-API-Version: <min>"`. A **406** means the
cloud is older than that microversion — record it, it is a different answer from
404 and it changes the plan.

Also worth one line each, since they decide whether T3.5 is ever worth starting:

```sh
curl -s -o /dev/null -w 'metadefs %{http_code}\n' -H "X-Auth-Token: $OS_TOKEN" "<glance>/v2/metadefs/namespaces"
curl -s -o /dev/null -w 'cgroups  %{http_code}\n' -H "X-Auth-Token: $OS_TOKEN" "<cinder>/groups"
```

---

## §4 — Report format

Finish with, in this order:

1. **The §1 result**, in one line per cloud: the release, the four microversion
   ceilings, and which inspection dialect it speaks. Across the fleet this is the
   deliverable — the *lowest* ceiling in the column is what every version-gated
   command has to tolerate.
2. **A PASS/FAIL table for §2** — one row per check.
3. **A YES/NO/403/406 table for §3** — one row per batch, which is directly the
   build order for `docs/proposals/coverage-tiers.md`.
4. **Batches to drop or defer**, with the status code that justifies each.
5. **Anything that surprised you** — a payload shaped differently from what the
   plan assumes is a finding, not a footnote. Those are what unit tests cannot
   catch, and they are the reason this document exists.

Remember: findings in the report, evidence in the conversation, nothing private
in the repo.
