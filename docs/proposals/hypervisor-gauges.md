# Proposal: rich hypervisor list with gauges (port of `lesha-giper4.py`)

Status: IMPLEMENTED — `koc hypervisor list --gauge` (+ `--check-actual`).
Terminal-width detection uses `golang.org/x/term` (the chosen option). Validated
end-to-end against a live nova region at 80/160/240 widths (compact/wide/full
profiles), ASCII bars, color thresholds, and json/csv raw output. Files:
`internal/cli/server/hypervisor_gauge.go` (render), `nodeexporter.go` (scrape),
`hypervisor.go` (flags + gather); tests in `*_gauge_test.go` / `nodeexporter_test.go`.
Reference: `~/lesha-giper4.py` — lists Nova hypervisors with allocation gauges,
threshold colors, overcommit, and optional node_exporter actual-usage comparison.

## Feasibility — yes

| Piece | koc/Go path |
|---|---|
| vCPU/RAM/Disk totals + usage | **placement** `resourceproviders` inventories+usages (nova removed these fields at microversion 2.88 — `latest` returns 0). RP matched to hypervisor by name; `VCPU.total`=cores, `usages.VCPU`=allocated, same for `MEMORY_MB`/`DISK_GB`. Best-effort: nova values kept if placement lacks a class. |
| VMs, type, state/status, cpu_model, host_ip | nova `hypervisors` list at the **default microversion** (still present there); placement has no equivalent |
| Aggregate membership + `--aggregate` filter | gophercloud `compute/v2/aggregates` (needs vendoring → one `make tidy`) |
| Gauges (Unicode `█`/space or ASCII `#`/`-`), overcommit, colors, thresholds | pure Go string building; ANSI like the script |
| node_exporter actual usage (`--check-actual`) | stdlib `net/http` GET `:9100/metrics` + Prometheus text parse; two CPU samples → idle→utilization; `node_memory_*` → RAM%. Concurrent via goroutines. No deps. |
| json / csv output | reuse `internal/output` with raw numeric columns (no gauges/color), matching the script |

**No new dependencies except** the `aggregates` gophercloud subpackage (already in
the module) and — only if we want auto terminal-width — a width helper (below).

## Command surface

Extend the existing OSC-compatible command, keeping the plain default:

```
koc server hypervisor list                 # unchanged plain table (OSC-compatible)
koc server hypervisor list --gauge         # rich: gauges + color, width-aware
koc server hypervisor list --gauge --check-actual   # + node_exporter real usage (wide view)
```

Flags (mirror the script): `--gauge`, `--bar-width N` (0 = auto-scale),
`--ascii`, `--color/--no-color` (auto by TTY, honors `NO_COLOR`), `--warn-pct`,
`--crit-pct`, `--include-ironic`, `--aggregate NAME`, `--sort COL`, `--reverse`,
`--width N` (override detected width), and the `--ne-*` node_exporter group.
`-f json` / `-f csv` emit raw numbers (percentages as fields), no gauges.

## UI/UX: responsive by terminal width

The full comparison view is dense (~21 columns with 3 gauges); it is **designed
for ≥240 cols**. Narrower terminals get progressively curated column sets and
shorter bars, down to a readable **80-col default**. Width = `--width`, else
detected, else `$COLUMNS`, else 80.

Three column profiles, auto-selected (override with `--width`/`--profile`):

| Profile | Auto when width ≥ | Columns | Bar width |
|---|---|---|---|
| `full`    | 240 (or `--check-actual`) | name, aggregate, type, vms, vCPU u/t, OC, CPU alloc%, **CPU_phys**, RAM gauge, RAM u/t, **RAM_phys**, RAM_phys used, Disk gauge, Disk u/t, cpu_model, state | 20 |
| `wide`    | 160 | name, aggregate, type, vms, vCPU u/t, OC, RAM gauge, RAM u/t, Disk gauge, Disk u/t, cpu_model, state | 16 |
| `compact` | (default / <160, incl. 80) | name, vms, OC, RAM gauge, RAM%, Disk gauge, Disk% | 8 |

Bars auto-scale so the table never exceeds the width; when even `compact` would
overflow, low-priority columns (state, aggregate) drop first and a one-line hint
suggests `--width`/`-f json`. Colors: green `<warn`, yellow `≥warn`, red `≥crit`
(RAM/Disk); overcommit green `<1.0x` / yellow `1–4x` / red `≥4x`. Auto-off when
not a TTY or `NO_COLOR` set.

### Mockup — `full` (≥240 cols, `--gauge --check-actual`) — bars shown at 20

```
Name          Aggregate      Type  VMs  vCPU(u/t)   OC     CPUa%   CPU_phys                    RAM                         RAM(u/t GiB)  RAM_phys                    RAMp    Disk                        Disk(u/t GiB)  CPU Model     State
cdm-sl-pca33  rack1          QEMU   12    96/128   0.75x   75.0%  [██████░░░░░░░░░░░░░░]  31.4% [██████████░░░░░░░░░░]  52.1% 266/512   [████████░░░░░░░░░░░░]  40.2% 205 GiB [████░░░░░░░░░░░░░░░░]  20.3% 1200/6000  Cascadelake   up
cdm-sl-pca34  rack1          QEMU    8    64/128   0.50x   50.0%  [████░░░░░░░░░░░░░░░░]  22.0% [████████░░░░░░░░░░░░]  41.0% 210/512   [██████░░░░░░░░░░░░░░]  33.1% 168 GiB [███░░░░░░░░░░░░░░░░░]  15.0%  900/6000  Cascadelake   up
```
(RAM ≥70% renders yellow, ≥90% red; the physical gauges let you spot allocation
that is over- or under-committed vs real usage — the point of `--check-actual`.)

### Mockup — `compact` (80 cols, default) — bars at 8

```
Name          VMs  OC     RAM             Disk            St
cdm-sl-pca33   12  0.75x  [████░░░░] 52%  [██░░░░░░] 20%  up
cdm-sl-pca34    8  0.50x  [███░░░░░] 41%  [█░░░░░░░] 15%  up
```
~62 cols — fits 80 with margin; add `--width 200` for the `wide` profile, or
`--check-actual`/`--width 240` for `full`.

## Open decision — terminal-width detection

koc keeps a deliberately small dependency set (4 direct). Auto width needs a
`TIOCGWINSZ` ioctl, which stdlib doesn't expose. Options:

1. **`golang.org/x/term`** (recommended): `term.GetSize(fd)` — tiny, standard,
   reliable. Adds one vendored module (`make tidy`).
2. **Zero-dep**: `--width` flag + `$COLUMNS` env, default 80. No detection when
   neither is set (COLUMNS isn't always exported), so users on wide terminals
   must pass `--width 240`. Keeps the binary dependency-pure.

Recommendation: **option 1** for real "looks good out of the box" behavior; fall
back to `--width`/`$COLUMNS` when not a TTY. If the air-gap/minimal-deps rule
should win, option 2 with a good `--width` default is acceptable.

## Testing

Render is a pure function `render(hosts, opts, width) → string`; table tests feed
fixed hypervisor structs + widths (80/160/240) and assert column selection, bar
fill length, and (color-stripped) layout. node_exporter parsing tested against a
captured `/metrics` fixture via httptest. json/csv go through the `runXxx` seam +
`internal/output`.
