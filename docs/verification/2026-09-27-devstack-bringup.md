# Devstack bring-up verification (2026-09-27)

Prompt for a session running on **your own Linux servers**. It proves that
`scripts/devstack/up.sh` stacks a working single-node devstack for every
release koc's nightly functional run targets, and fixes the script where it
does not. Nothing in CI is built until this passes: the nightly workflow will be
a thin wrapper that calls this same script, so a config that stacks here is the
config CI runs.

This session is **verifying and repairing the bring-up**, not writing koc
tests. The deliverables are a corrected `scripts/devstack/*.sh`, committed, and
the results table in §6.

---

## 1. What is being verified

Four releases, each as **one all-in-one node** (`--features core,net,dns`, the
script's default):

| Cell | `--series` | Resolves to | Ubuntu | Known going in |
| --- | --- | --- | --- | --- |
| Z | `zed` | `unmaintained/zed` | 22.04 | neutron-fwaas and neutron-vpnaas are EOL on Zed; the script pins them to the `zed-eol` tag. No `tap-mirror` extension on Zed (expected WARN) |
| C | `caracal` | `unmaintained/2024.1` | 22.04 | devstack 2024.1 supports jammy only |
| E | `epoxy` | `stable/2025.1` — **becomes `unmaintained/2025.1` on 2026-10-02** | 24.04 | If you run after the rename, the script must follow it with no edit; that is part of the test |
| L | `latest` | the newest `stable/*` devstack branch (2026.2, released 2026-09-30) | 24.04 | — |

What "all-in-one" contains, and why the neutron backend is ML2/OVS through
2024.1 and ML2/OVN from 2025.1, is in the header of `scripts/devstack/up.sh`.
Read it before changing anything.

Out of scope for now: ironic, octavia (deliberately skipped), KeyVRM,
`--creds-from-ns`.

## 2. Rules

- **Fresh VM for every attempt.** Never re-run `stack.sh` on a node that has
  stacked or half-stacked; `unstack.sh`/`clean.sh` leave state behind and the
  result then says nothing about CI, which always starts from a clean image.
- **Fix the script, not the node.** Every change goes into
  `scripts/devstack/up.sh` (or `smoke.sh`), never hand-edited into a
  `local.conf` or applied with a manual `apt`/`pip` on the VM. A fix that only
  exists on your VM does not exist.
- **Keep fixes minimal and explained.** Each commit fixes one failure and says,
  in the body, which cell failed, the error line from `stack.sh.log`, and why
  the fix is right. Conventional Commits, e.g. `fix(devstack): pin
  networking-bgpvpn requirements on zed`.
- **Do not silently drop a feature.** If a plugin genuinely cannot work on a
  release (not merely "did not work on the first try"), make `up.sh` skip it
  for that release *explicitly*, with a comment citing the reason, and record
  it in §6. Scope reductions are decisions for the maintainer, so list them in
  the report.
- **Private data never leaves the org** (AGENTS.md). Nothing from your servers
  goes into a commit: no host names, IPs, internal mirrors, proxy URLs, or log
  excerpts that contain them. The numbers in §6 (seconds, MB, PASS/FAIL) are
  fine. If you need an internal mirror or proxy to reach GitHub/PyPI, set it in
  the environment (`GIT_BASE`, `PIP_INDEX_URL`, `http_proxy`), never in the
  script, and **say so in the report** — CI has no such mirror, so a cell that
  only stacks through one is not proven.

## 3. VM shape

Match a GitHub-hosted public-repo runner, because that is what CI gets:

- **4 vCPU, 16 GB RAM.** Do not give the VM more; the point is to learn
  whether all-in-one fits.
- **Disk: 40 GB**, but record the space devstack consumed (`disk_delta_mb` in
  `bringup.json`). The runner has **14 GB** of free SSD, so anything near that
  is a finding.
- **Nested virtualisation on if you can** (`/dev/kvm` present). Record it
  either way (`kvm` in `bringup.json`). Without it nova falls back to QEMU TCG
  and the server-boot time is not comparable to a runner.
- Ubuntu per §1, cloud image, fully updated, nothing else installed.
- Unrestricted egress to github.com and pypi.org (see the mirror rule in §2).

Running the four cells in parallel on four VMs is fine and saves ~2 hours.

## 4. Procedure (per cell)

On the fresh VM:

```sh
# 1. A non-root user with passwordless sudo (devstack refuses root).
sudo useradd -s /bin/bash -d /opt/stack -m stack
echo "stack ALL=(ALL) NOPASSWD: ALL" | sudo tee /etc/sudoers.d/stack
sudo -u stack -i

# 2. The branch under test.
git clone -b claude/quirky-carson-1o8rgn https://github.com/ftarasenko/go-openstackclient.git koc
cd koc

# 3. koc itself, for smoke.sh's koc checks. Either build it here (Go 1.27):
#      CGO_ENABLED=0 GOFLAGS=-mod=vendor go build -o ~/koc-bin ./cmd/koc
#    or cross-build on your workstation and copy it over:
#      CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS=-mod=vendor go build -o koc-linux ./cmd/koc

# 4. Preview what will be deployed; check refs.txt looks right.
scripts/devstack/up.sh --series zed --dry-run --log-dir ~/logs

# 5. Stack. Takes 20–60 minutes.
scripts/devstack/up.sh --series zed --log-dir ~/logs --devstack-dir ~/devstack

# 6. Prove it works.
scripts/devstack/smoke.sh --log-dir ~/logs --devstack-dir ~/devstack --koc ~/koc-bin

# 7. On any failure in 5 or 6:
scripts/devstack/collect-logs.sh --log-dir ~/logs --devstack-dir ~/devstack
```

Then keep, from `~/logs`: `bringup.json`, `refs.txt`, `smoke.txt`,
`extensions.txt`, and the **"DevStack Component Timing"** table printed at the
end of `stack.sh.log` (it splits wall time into apt, pip, git, service start —
this is what decides whether CI should cache anything).

A cell is **done** when `stack_rc` is 0 and `smoke.sh` exits 0 (WARN lines
allowed, FAIL lines not).

## 5. If all-in-one does not fit

Only if a cell stacks but does not fit — peak RAM above ~14 GB of 16
(`mem_peak_used_mb`), disk above ~12 GB (`disk_delta_mb`), `stack.sh` over 45
minutes — or if two plugins conflict (fwaas and vpnaas share the L3 agent, the
most likely clash), try the split on **two fresh VMs**:

```sh
scripts/devstack/up.sh --series <s> --features core,dns …
scripts/devstack/up.sh --series <s> --features net …
```

Report which split worked. CI then runs two jobs per release instead of one;
that is acceptable, just more expensive, so prove all-in-one does not work
before falling back.

## 6. Also measure (after all four cells pass)

Ordered by how much they would change the CI design; skip any you run out of
time for and say so.

1. **Warm pip cache.** On a fresh VM of your fastest cell, copy `~/.cache/pip`
   from a finished run, stack again, and compare `stack_seconds` and the pip
   line of the timing table. If it saves more than ~3 minutes, CI will cache it
   per series; if not, CI stays cache-free (simpler, nothing to poison).
2. **Vault round trip** (koc's `--creds-from-vault`, on any stacked cell):
   ```sh
   docker run -d --name vault -p 8200:8200 -e VAULT_DEV_ROOT_TOKEN_ID=root hashicorp/vault server -dev
   export VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root
   . ~/devstack/openrc admin admin
   docker exec -e VAULT_ADDR=http://127.0.0.1:8200 -e VAULT_TOKEN=root vault \
     vault kv put secret/koc-ft/openrc OS_AUTH_URL="$OS_AUTH_URL" OS_USERNAME=admin \
       OS_PASSWORD=secret OS_PROJECT_NAME=admin OS_USER_DOMAIN_NAME=Default OS_PROJECT_DOMAIN_NAME=Default \
       OS_REGION_NAME="$OS_REGION_NAME"
   env -u OS_AUTH_URL -u OS_USERNAME -u OS_PASSWORD -u OS_PROJECT_NAME -u OS_CLOUD \
     ~/koc-bin --creds-from-vault /koc-ft/openrc --vault-kv-mount secret network list
   ~/koc-bin vault kv list --vault-kv-mount secret /koc-ft
   ```
   Expected: the network list matches `openstack network list`; the kv list
   shows `openrc`. The domain is given by **name** on purpose: koc's openrc
   parser reads `OS_{USER,PROJECT}_DOMAIN_NAME` but not the `*_DOMAIN_ID`
   spelling devstack's own openrc uses — note in the report whether a
   `*_DOMAIN_ID`-only secret fails, since that is a koc gap, not a devstack one.
3. **Server boot time with and without `/dev/kvm`**, if you have both kinds of
   host: the `server boots to ACTIVE` seconds in `smoke.txt`. This sets the
   compute tests' timeouts.

## 7. Report

Fill this table (append it to this file under a `## Results` heading and
commit it as `docs(devstack): record bring-up results` — numbers only, see §2),
then summarise in the conversation:

| Cell | Attempts | `stack_seconds` | RAM peak MB | Disk MB | kvm | smoke FAIL / WARN | Fixes (commit) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Z zed | | | | | | | |
| C caracal | | | | | | | |
| E epoxy | | | | | | | |
| L latest | | | | | | | |

Plus:

- `refs.txt` for each cell (it names only public GitHub refs, so it can be pasted).
- Each cell's DevStack Component Timing table.
- Every feature that had to be skipped on a release, with the reason.
- The §6 measurements.
- Anything in `up.sh`'s design that turned out to be wrong: ML2/OVS for all
  releases, `GIT_DEPTH=1`, `API_WORKERS=1`, the ref fallback order
  (`stable/` → `unmaintained/` → `-eol`), the Ubuntu-per-series table.

## Results

Run 2026-09-27 on nested-KVM VMs (qemu user-mode networking, 4 vCPU / 16 GB /
40 GB, Ubuntu cloud images fully updated) on one host, with direct egress to
GitHub and PyPI — **no mirror or proxy**. Every cell below passed on a fresh VM
with `smoke.sh` as of `2eab854`: zed and caracal with `up.sh` as of `dd81368`,
epoxy and latest with `8d798a1`, which moved them to ML2/OVN (zed and caracal
render the same `local.conf` under `8d798a1` as in their verified runs).

| Cell | Backend | Attempts | `stack_seconds` | RAM peak MB | Disk MB | kvm | smoke FAIL / WARN | Fixes (commit) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Z zed | ML2/OVS | 5 | 1084 | 5633 | 6029 | true | 0 / 1 (tap-mirror) | `eb9026f` `6b09af2` `a938406` `c9acee7` `f82e83a` |
| C caracal | ML2/OVS | 5 | 1008 ¹ | 5560 | 5857 | true | 0 / 1 (tap-mirror) | `eb9026f` `6b09af2` `2f44087` `a938406` `c9acee7` `f82e83a` `dd81368` |
| E epoxy | ML2/OVN | 2 ² | 1076 | 5544 | 6228 | true | 0 / 0 | `2eab854` `8d798a1` |
| L latest | ML2/OVN | 2 ² | 1057 | 6613 | 6131 | true | 0 / 0 | `2eab854` `8d798a1` |

Attempts count fresh VMs. On zed and caracal the first was the harness, not the
script: qemu's default user network is 10.0.2.0/24, inside devstack's
`FIXED_RANGE` (10.0.0.0/22), so devstack could not pick a `HOST_IP`. Moving
the VMs to 192.168.76.0/24 fixed it; runners are not affected. The second
failed on `GIT_DEPTH=1`, the third on the ec2 `read`, and the fourth stacked
and surfaced the smoke bugs (and, on caracal, the s3token gap); that changed
`up.sh`, so the fifth re-proved both cells under the final script.

¹ Includes ~100 s lost to a harness artefact: the VM had an unroutable `fec0::`
IPv6 address, so the cirros download tried IPv6 first and waited out the TCP
timeout before falling back. Runners have no IPv6.
² Epoxy and latest first stacked cleanly on ML2/OVS (913 s / 6332 MB and
903 s / 7253 MB; smoke's only FAIL was the `volumev3` catalog probe, fixed in
`smoke.sh` and re-run on the same node). They were then moved to ML2/OVN and
re-proved on fresh VMs; the row shows the OVN run, which is what CI runs.
Under OVN every agent reports alive (ovn-controller, the OVN metadata/agent,
`neutron-ovn-vpn-agent`, `neutron-bgp-dragent`, metering), vpnaas and fwaas
load their OVN drivers, and all 31 smoke checks pass.

Resolved refs (`refs.txt`):

| Project | zed | caracal | epoxy | latest |
| --- | --- | --- | --- | --- |
| devstack, neutron, designate, networking-bgpvpn, neutron-dynamic-routing, tap-as-a-service | `unmaintained/zed` | `unmaintained/2024.1` | `stable/2025.1` | `stable/2026.2` |
| neutron-fwaas, neutron-vpnaas | `zed-eol` | `unmaintained/2024.1` | `stable/2025.1` | `stable/2026.2` |

Epoxy ran before the 2026-10-02 rename, so the `unmaintained/2025.1` follow is
still unproven by a run; the fallback order it relies on is the one zed and
caracal exercised.

DevStack Component Timing (seconds):

| Component | zed (OVS) | caracal (OVS) | epoxy (OVN) | latest (OVN) |
| --- | --- | --- | --- | --- |
| pip_install | 297 | 180 | 254 | 171 |
| osc | 339 | 288 | 292 | 346 |
| apt-get | 155 | 156 | 175 | 170 |
| git_timed | 129 | 127 | 146 | 169 |
| async_wait | 91 | 79 | 82 | 94 |
| run_process | 36 | 45 | 48 | 62 |
| wait_for_service | 17 | 27 | 31 | 26 |
| dbsync / test_with_retry / apt-get-update | 7 | 10 | 19 | 14 |
| Unaccounted | 13 | 96 ¹ | 29 | 5 |
| **Total** | **1084** | **1008** | **1076** | **1057** |

**Every cell fits all-in-one**, so §5's split was not needed: peak RAM is at
most 7.3 GB of 16 (6.6 GB on OVN), disk at most 6.2 GB of a runner's 14, and
the slowest stack is 18 minutes.

### Skipped features

None. Every feature stacks on every release. The only per-release absence is
`tap-mirror`, which tap-as-a-service first ships on 2025.1 (smoke WARNs on zed
and caracal as designed).

### §6 measurements

1. **Warm pip cache** (latest): **not worth caching.** Seeding a fresh VM with
   the 91 MB `~/.cache/pip` of a finished run cut `pip_install` from 164 / 158 s
   (two cold runs) to 124 s, but `stack_seconds` was 880 against 903 / 858
   cold — inside run-to-run noise, far below the ~3 minutes that would justify
   a cache. Wheels are small next to `osc` (~280–340 s) and apt/git, which a pip
   cache does not touch. CI stays cache-free.
2. **Vault round trip** (zed): with the domain given by name, `koc
   --creds-from-vault /koc-ft/openrc --vault-kv-mount secret network list`
   returns the same networks (same IDs) as `openstack network list`, and `koc
   vault kv list` shows `openrc`. A `*_DOMAIN_ID`-only secret **fails**: `You
   must provide exactly one of DomainID or DomainName in a Scope with
   ProjectName` — the koc gap this section predicted.
3. **Boot time with and without `/dev/kvm`**: not measured. Every VM had
   nested KVM; there was no TCG host to compare. With KVM, `server boots to
   ACTIVE` took 11–22 s (zed 14, caracal 13; epoxy 18 on OVS / 22 on OVN,
   latest 11 / 14).

### What in `up.sh`'s design was wrong

- **`GIT_DEPTH=1` — wrong** (`eb9026f`). Tagless clones make pbr version every
  source project 0.0.0: networking-bgpvpn became unresolvable (bagpipe needs
  `>=12`) and pip silently replaced the source neutron with a PyPI wheel. Full
  clones cost `git_timed` ≈ 130–146 s.
- **devstack's s3token configuration** is incomplete on 2024.1 and 2025.1
  (`2f44087`): swift's security backport requires service credentials that
  devstack writes only from 2026.2.
- **ML2/OVS for all releases**: works, but replaced by a per-series choice
  (`8d798a1`) so CI covers both backends the fleet runs: OVS for zed and
  2024.1, OVN from 2025.1. Both stack every plugin on their releases. Under
  OVN, taas still loads its OVS-agent RPC driver: the tap API works, but no
  traffic is mirrored, so tap tests there are API-only.
- **`API_WORKERS=1`**: held; RAM stays under half the budget.
- **Ref fallback order** (`stable/` → `unmaintained/` → `-eol`): held; zed
  mixes `unmaintained/zed` and `zed-eol` in one deployment.
- **Ubuntu per series** (jammy for zed/2024.1, noble after): held.
- **`neutron-network-segment-range` — dropped.** On latest (2026.2) an 8 vCPU
  VM stacked in 472 s and then failed at stack.sh's first `network create`:
  neutron answered 503 "No project network is available for allocation", with
  the segment-range table empty. Each neutron process (API workers, rpc-server,
  periodic and OVN maintenance workers) seeds the default ranges stamped with
  its own start time and deletes every default stamped otherwise; the start
  time is shared per parent PID, and processes that start together can leave
  none. Restarting `neutron-api` repopulated them. koc has no segment-range
  commands, so `up.sh` no longer enables the plugin; the re-stack on a fresh
  VM passed (1281 s, smoke 31/31). Earlier cells passed with it only because
  their slower starts did not race.

### On GitHub-hosted runners (nightly workflow)

The first run of `.github/workflows/functional.yml` (run `36314099437`, on
`71762d6`, 2026-09-27) passed all four cells on the first attempt with `up.sh`
**unchanged** — no `--gha`-only fix was needed beyond the workarounds already
in the script. Hosted runners are 4 vCPU / 16 GB with `/dev/kvm` opened by the
workflow's udev rule; `kvm` was `true` in every `bringup.json`.

| Cell | Runner | `stack_seconds` | RAM peak MB | Disk MB | Server boot | smoke FAIL / WARN | Job wall time |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Z zed | ubuntu-22.04 | 727 | 5846 | 4114 | 11 s | 0 / 1 (tap-mirror) | 870 s |
| C caracal | ubuntu-22.04 | 430 | 5554 | 3944 | 9 s | 0 / 1 (tap-mirror) | 553 s |
| E epoxy | ubuntu-24.04 | 698 | 6065 | 4036 | 18 s | 0 / 0 | 836 s |
| L latest | ubuntu-24.04 | 706 | 7264 | 3942 | 13 s | 0 / 0 | 842 s |

Runners stack faster than the verification VMs (7–12 minutes against 17–18)
and use a third less disk, so the workflow's job (now 75 minutes, for the Go
suites) and 40-minute stack timeouts leave ample room. Peak RAM matches the VMs
(≤ 7.3 GB of 16).

The first runner-only failure came later (run `36323792743`, zed): stack.sh's
single `wget` of cirros from download.cirros-cloud.net timed out on IPv4 after
two minutes and the cell failed with `stack_rc` 4. `up.sh --gha` now
prefetches the image stack.sh would fetch into `files/`, which stack.sh then
skips, from cirros's GitHub release mirror with the original host as a
fallback and retries on both (`prefetch_cirros`). A fresh VM keeps stack.sh's
own fetch.

### koc gaps found (not devstack's)

Found by the Go functional suites on the runners, identically on every cell
that stacked (run `36323792743`):

- `hypervisor show <id>` answered "not found" for the ID `hypervisor list`
  prints: the list runs at the negotiated microversion (UUIDs from 2.53), the
  show looks up at 2.1 (integer IDs). **Fixed** by `fix(server): resolve a
  hypervisor UUID taken from hypervisor list`.
- `server add/remove volume <server> <name>` sent the name to nova, which takes
  only a UUID. **Fixed** by `fix(server): resolve volume names in server
  add/remove volume`.
- `user create --project` set the default project but did not print it.
  **Fixed** by `fix(identity): print the default project user create sets`.

And by the run that first stacked every suite (`36324712864`):

- `zone move` without `--pool-id` posted no body and designate answered 415.
  **Fixed** by `fix(dns): send a JSON body with zone move`.
- `resource usage show` printed placement 1.38's consumer-type grouping as if
  `INSTANCE` were a resource class. **Fixed** by `fix(placement): report
  resource usage per class from placement 1.38`.
- `quota show` (and `dns`/`loadbalancer quota`) with no project failed on the
  `clouds.yaml` cells, where `OS_PROJECT_*` are empty. **Fixed** by `fix(quota):
  default to the token's project when none is given`.

And by the first run with all of those in (`36327991276`), where zed was down
to two failing tests:

- `server rebuild --image <name>` sent the name as nova's `imageRef`. **Fixed**
  by `fix(server): resolve image names in server rebuild`.
- `server add volume` on the latest cell: nova answered 202 (asynchronous
  attach) and koc, through gophercloud's 200-only `volumeattach.Create`,
  reported failure. **Fixed** by `fix(server): accept nova's asynchronous
  answer to server add volume`.

- `OS_VOLUME_API_VERSION=3` (devstack's openrc) is sent verbatim as the cinder
  microversion and cinder answers 400; upstream OSC accepts a major-only
  version. smoke now runs koc from `clouds.yaml` alone (`f82e83a`). **Fixed**
  on master by `2d139cd` / `7b67ee7`: a bare major means "latest" for cinder,
  nova, ironic and placement alike.
- The Vault openrc parser ignores `OS_{USER,PROJECT}_DOMAIN_ID` (above).
  **Fixed** by `fix(auth): honour OS_{USER,PROJECT}_DOMAIN_ID and
  OS_DOMAIN_ID`. The gap was wider than Vault: koc had no domain-ID support at
  all, so the environment path and the `--os-*-domain-id` flags were missing
  too.
- `-f value` separates columns with a tab; `openstack` uses a space. Upstream
  cliff confirmed (`ValueFormatter.emit_list` joins with `' '`). It was a
  recorded deliberate deviation; the maintainer chose parity. **Fixed** by
  `feat(output)!: join -f value cells with a space, like openstack`, with the
  unambiguous tab-separated job moved to the new koc-native `-f tsv`
  (`feat(output): add -f tsv, an unambiguous tab-separated format`).
