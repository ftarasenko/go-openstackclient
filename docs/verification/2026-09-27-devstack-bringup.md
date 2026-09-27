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

What "all-in-one" contains, and why ML2/OVS instead of devstack's default OVN,
is in the header of `scripts/devstack/up.sh`. Read it before changing anything.

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
