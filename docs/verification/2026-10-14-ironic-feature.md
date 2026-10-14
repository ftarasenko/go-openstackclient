# Devstack `ironic` feature verification (2026-10-14)

`scripts/devstack/up.sh` gained an `ironic` feature, on by default, so the
functional suite can drive `koc baremetal …` against a real ironic. This
records how it was proved, in the shape of `2026-09-27-devstack-bringup.md`.

## What is stacked

ironic's API and conductor (`ir-api`, `ir-cond`) from the ironic devstack
plugin at the series' ref, with the **fake-hardware** type only:

| Setting | Why |
| --- | --- |
| `IRONIC_IS_HARDWARE=True`, `IRONIC_HWINFO_FILE=/dev/null` | enroll nothing; skip virtualbmc and the VMs |
| `IRONIC_BAREMETAL_BASIC_OPS=False`, `IRONIC_BUILD_DEPLOY_RAMDISK=False` | no deploy ramdisk build or download |
| `IRONIC_ENABLED_HARDWARE_TYPES=fake-hardware`, `IRONIC_DEPLOY_DRIVER=fake-hardware` | the tests enroll their own fake nodes |
| `IRONIC_ENABLED_NETWORK_INTERFACES=noop,flat` (default `noop`) | `flat` lets a test node hold a real neutron VIF (`node vif attach`, `node network show`) |
| `IRONIC_ENABLED_STORAGE_INTERFACES=noop,fake` | volume connectors/targets |
| `DOWNLOAD_DEFAULT_IMAGES=False` | the plugin queues its own cirros; devstack's default now comes from another host, and two images of one name make `--image <name>` ambiguous |

nova keeps libvirt; ironic is API-only. Under `--gha` the cirros disk and uec
images are prefetched like the core image. `smoke.sh` checks the catalog entry,
the `fake-hardware` driver, a fake node walking enroll → manageable →
available, and `koc baremetal driver list`.

## Results

Fresh nested-KVM VMs (8 vCPU / 16 GB / 60 GB), direct egress, no mirror,
`--features core,net,dns,ironic`:

| Cell | Ubuntu | Backend | `stack_seconds` | RAM peak MB | Disk MB | smoke FAIL / WARN | ironic API |
| --- | --- | --- | --- | --- | --- | --- | --- |
| zed | 22.04 | ML2/OVS | 1318 | 5904 | 3091 | 0 / 1 (tap-mirror) | 1.80 |
| latest (2026.2) | 24.04 | ML2/OVN | 1613 | 6997 | 7566 | 0 / 0 | 1.115 |

Against the 2026-09-27 runs without ironic (zed 1084 s / 5633 MB, latest
1057 s / 6613 MB) the feature costs roughly 4–9 minutes and under 0.4 GB of
peak RAM, inside the nightly job's 75-minute timeout and a runner's 16 GB.
caracal and epoxy render the same ironic block (`up.sh --dry-run`) and were not
stacked in this pass; the nightly run is their first proof.

Resolved ironic refs: `unmaintained/zed` on zed, `stable/2026.2` on latest.

### Functional tests

`go test -tags functional ./test/functional/`:

- **latest**, `clouds.yaml`: the whole suite passes (606 of 730 leaves
  exercised); the ironic tests also pass from the openrc.
- **zed**, both credential paths: every ironic test passes or skips on its
  version gate — inspection rules (1.96), runbooks (1.92) and shards (1.82)
  skip, since zed's ironic speaks 1.80.

## Findings

- **ironic's secure-RBAC defaults want a system-scoped token** for drivers,
  shards, deploy templates, inspection rules and vendor passthru; a project
  admin gets 403. The ironic tests run through `systemRunner`
  (`devstack-system-admin`, or the openrc with `OS_SYSTEM_SCOPE=all`).
- **neutron shows a system-scoped token no project's ports**, so `koc
  baremetal node network show` reads a project-owned node project-scoped; its
  warning for an unreadable VIF says so.
- **Zed's ironic is 1.80, not 1.82** (ironic 21.1; 21.4 is 2023.1). AGENTS.md,
  `docs/coverage.md` and `explainMicroversion`'s feature list were corrected,
  and `node inventory` (1.81) now names the version on Zed.
- **zed's clouds.yaml names `devstack-system-admin` with no user domain.** OSC
  falls back to `--os-default-domain`; koc did not, and now does.
- **`deploy template list --long` and `runbook list --long` requested
  `/…/detail`**, a route only nodes and ports have; they now send
  `?detail=True`.
