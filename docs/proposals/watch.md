# Proposal: `--watch` — live-refreshing read commands

Status: IMPLEMENTED — all three phases of §6. `--watch` is registered on every
read verb (219 `list`/`show` leaves) by a tree pass in `internal/cli/watch.go`,
which also wraps each command's `RunE` into the refresh loop; the loop itself is
`internal/watch`. §7 (token cache on disk, `changes-since` incremental polling)
remains separately decidable and is **not** implemented.

Validated end to end against a mock Keystone + nova: four refreshes cost **one**
token where four cold starts cost four; the fitted table at 60 columns is
byte-identical to an unwatched TTY render; a forced 503 leaves the last good
frame up and marks the status line stale; 401/403 exits at once; `-f json` pipes
cleanly through `jq` and `-f csv` emits its header once; every key (`q`,
`space`/`r`, `p`, `+`/`-`, `d`, Ctrl-C) behaves, and the alternate screen is
entered and left exactly once on every exit path.

Files: `internal/watch/` (loop, painter, status line, differ, keys, error
classification), `internal/cli/watch.go` (wiring, flags, denials),
`internal/output/output.go` (`SetDisplayWidth`, `Highlighter`/`CellStyle`),
`internal/auth/services.go` (memoized `authenticated`),
`internal/cli/resolve/cache.go` (name→ID memo), `cmd/koc/main.go` (interrupt
message). Tests: `internal/watch/*_test.go`, `internal/cli/watch_test.go`,
`internal/output/highlight_test.go`, `internal/cli/resolve/cache_test.go`.

## Changed during implementation

- **`--watch-count` counts attempts, not successes.** Counting only successful
  refreshes meant an endpoint that was down made a bounded watch unbounded.
- **Tick skipping is a clock-step guard, not the slow-endpoint path.** Backoff
  widens the interval to twice the last latency *before* the next deadline is
  computed, so a slow endpoint never reaches an overdue deadline. What does is
  the clock moving under the loop (an NTP step); the counter stays for that.
- **`--debug`/`--timing` are refused only in repaint mode.** Appending frames
  overwrites nothing, so `--watch-plain` (and a piped run, which is implicitly
  plain) allows both, as §7 of the original allowed for.
- **The status line carries only the command's own flags.** §4's example showed
  the invocation; taking it from `os.Args` would put `--os-password` on screen.
- **`--watch-no-title`** is the spelling of §4's "suppressible (`watch -t`)".
- **Ctrl-C reports "koc: interrupted" without the `--wait` caveat.**
  `cmd/koc/main.go` stays the single place an interrupt is reported (§6 phase 3),
  but it now recognises `cli.ErrWatchStopped` — a read-only loop leaves nothing
  running server-side, and Ctrl-C is how a watch is *meant* to end.
- **The name→ID memo is opt-in** (`resolve.Enable`, called by the watch wiring).
  A one-shot invocation resolves each name once, so a process-global cache buys
  it nothing and would silently change what a second lookup returns.
- **Two `show` verbs are denied**, which §3 anticipated without naming:
  `console url show` POSTs to nova's remote-consoles API and mints a session per
  call, and `server password show` reads a key passphrase from the terminal.
- **`?` opens a key map**, which §6 phase 3 did not call for. The status line
  originally listed the letters (`keys: q p r + - d`), which says that six keys
  exist and nothing about what they do — and `+` reads backwards, since it
  lengthens the interval. The hint now points at the panel, and the `--watch`
  flag's own usage string names the key, so `koc <noun> list --help` answers it
  too.
- **A departed row is held at the bottom of the table**, not in its old
  position: the rows above it have already been reconciled by identity, so
  re-inserting it would imply an ordering the API did not send.

---

*The rest of this document is the proposal as agreed, unchanged.*

Replaces the `watch -n1 koc …` idiom with an in-process refresh loop on every
read-only verb.

Motivating invocation (real operator usage):

```sh
watch -n1 koc server list --all --host <host> -f table -c Name -c Status
```

## 1. What `watch(1)` actually costs here

`watch` re-executes the binary every tick. For `koc` that is not a redraw, it is
a full cold start. Per second, against the control plane:

| Per tick, today | Why |
|---|---|
| TCP connect + TLS handshake to Keystone, then to Nova | new process, new `http.Client`, no keep-alive survives |
| `POST /v3/auth/tokens` | `auth.Options.Authenticate` runs once per process (`internal/auth/services.go` → `authenticated`); nothing is cached across processes. Keystone mints and persists a Fernet token **every second** |
| catalog parse + endpoint resolution | same |
| one Keystone lookup per named `--project`/`--user` | `resolveServerOwner` re-resolves the name each tick |
| `GET /servers/detail?all_tenants=1&host=…` (paged) | the actual payload — the only request the operator wanted |

So roughly **half the requests and all the handshakes are pure restart tax**, and
`--all` makes the one useful request a cross-cell query. At `-n1` a single
operator terminal is a sustained ~2 req/s write-ish load on Keystone.

Display is degraded too, and this part is not obvious:

- `watch` runs the command with stdout on a **pipe**, so `output.terminalSize`
  reports "not a TTY", `fitWidth` returns 0, and the table renders **unbounded**
  — `watch` then hard-clips each line at the right edge, silently cutting
  columns. (`--max-width $(tput cols)` is the workaround today.)
- For the same reason `colorEnabled` (`server/hypervisor_gauge.go`) turns color
  off, so `hypervisor list --gauge` under `watch` loses its thresholds unless
  both `watch -c` and a forcing flag are given.
- Full-screen clear each tick → flicker, lost scrollback, and a transient API
  5xx blanks the screen instead of showing the last good state.
- `watch -d` diffs by **character position**, which is noise the moment a row is
  added, removed, or re-ordered — exactly what happens in a live fleet.

## 2. Prior art

| Tool | Mechanism | What to take |
|---|---|---|
| `watch(1)` (procps) | re-exec + full clear | flag vocabulary: `-n` interval, `-d` diff, `-e` exit-on-error, `-g` exit-on-change, `-t` no title |
| `viddy` | in-process, diff highlight, scrollback | per-cell diff, keep history, pause/resume keys |
| `kubectl get -w` | **server-side** streaming watch | nothing to copy: OpenStack has no watch/long-poll API on any service koc talks to. Client polling is the only option |
| `k9s`, `htop`, `docker stats` | full TUI, alternate screen | alt-screen + clean restore; but a TUI framework is a dependency koc will not take |
| `aws`/`gcloud`/`kubectl` | on-disk credential cache | the token cache (§7, separate workstream) |
| clig.dev | — | be composable when piped; never leave the terminal wedged; `NO_COLOR` |

Nova's `changes-since` is the closest thing to an incremental watch and koc
already exposes it; see §7.

## 3. Shape of the feature

**Client-side polling, in one process, at the cobra layer** — so it lands on all
`list` + `show` leaves at once rather than per command.

```sh
koc server list --all --host <host> -c Name -c Status --watch=1s
koc server list --watch                 # default 2s, matching watch(1)
koc baremetal node list --watch --watch-diff
koc server list --watch -f json | jq .  # not a TTY → one snapshot per tick, NDJSON-style
```

Flags, registered by the watch layer on watchable commands only (**not** as root
persistent flags — see §5, flag-prefix ambiguity):

| Flag | Default | Notes |
|---|---|---|
| `-w`, `--watch[=DURATION]` | off / `2s` | **decided**: one flag, pflag `NoOptDefVal="2s"`, so bare `--watch` and `--watch=1s` both work. `-w` is unused as a shorthand anywhere in the tree |
| `--watch-diff` | on when TTY | highlight changed cells, new rows, departing rows |
| `--watch-count N` | 0 | stop after N refreshes (`0` = forever) |
| `--watch-errors tolerate\|exit` | `tolerate` | `exit` is `watch -e` |
| `--watch-until-change` | off | `watch -g`: exit 0 on the first change |
| `--watch-plain` | off | append frames instead of repainting, even on a TTY (for `tee`/logging) |

A bare duration positional right after `--watch` (`--watch 1s`, the form pflag
cannot take) is detected and answered with "did you mean `--watch=1s`?" rather
than cobra's `accepts 0 arg(s)`.

**Watchable = read-only.** Commands opt in by annotation, applied in the same
final tree pass as `requireSubcommands(root)` (`internal/cli/root.go`) for verbs
`list`/`show`, with an explicit deny for the interactive and streaming ones
(`s3 download`, `image save`, anything that prompts). `koc server delete --watch`
must be an unknown flag, not a loop.

## 4. Behaviour

**Frame.** Each tick renders into a buffer, then one `Write` to the terminal —
no partial frames, no flicker. Repaint is cursor-home + erase-to-end
(`ESC[H ESC[0J`), inside the **alternate screen buffer** (`ESC[?1049h/l`) so
scrollback survives and quitting leaves the terminal exactly as found. Cursor
hidden while running. Restore is deferred **and** hooked to the existing
`signal.NotifyContext` in `cmd/koc/main.go`, so Ctrl-C never leaves a wedged
terminal.

**Status line** (suppressible, `watch -t`):

```
koc server list --all --host <host> · every 1s · 12:03:45 · 42 rows · 218ms · ok
```

On failure it becomes `stale 4s · 2 errors · last: 503 Service Unavailable` and
**the last good frame stays on screen**. Transient errors (5xx, timeout, connection
reset) never blank the display; authentication and authorization failures are
fatal immediately, because looping on bad credentials hammers Keystone and can
trip account lockout.

**Diff.** Rows are reconciled by identity — `ID` column if present, else `Name`,
else ordinal — so highlighting survives re-ordering, unlike `watch -d`. Changed
cells reverse-video, new rows green, rows that vanished held dimmed for one tick
before dropping. This is the single biggest win for `-c Name -c Status`: you see
`BUILD → ACTIVE` land, instead of spotting it.

**Load discipline.** Ticks never overlap: if a refresh outlives its interval the
next tick is skipped and the status line says so. If refresh latency exceeds
~50% of the interval the loop backs off automatically (and visibly) rather than
quietly saturating the control plane. Minimum interval is floored (`250ms`) with
an error below it.

**Piped output stays composable.** No TTY (or `--watch-plain`) → no escape
sequences at all, one rendered snapshot per tick appended to the stream; CSV
emits its header once. `NO_COLOR` is honoured, matching `colorEnabled`.

## 5. Implementation

New package `internal/watch`, with one testable seam and no dependencies beyond
`golang.org/x/term` (already vendored):

```go
// Run drives render on a ticker until ctx ends, the count is reached, or a
// fatal error occurs. render writes one complete frame into w.
func Run(ctx context.Context, o Options, out io.Writer, render func(context.Context, io.Writer) error) error
```

Wiring is a `RunE` wrapper: swap `cmd.SetOut`/`cmd.SetErr` to the frame buffer,
call the original `RunE` per tick, hand the bytes to the painter. Nothing in the
command files changes.

Touch points and the gotchas found while reading the tree:

1. **`internal/auth`: memoize the authenticated `*Client`.** `authenticated()`
   currently calls `Authenticate` on every call. Cache it on `Options`.
   `ao.AllowReauth = true` is already set (`provider.go`), so gophercloud
   silently re-authenticates on token expiry — a multi-hour watch keeps working.
   This is the change that removes the per-tick token, and it also benefits any
   command that touches two services.
2. **`internal/output`: render width must be explicit.** `terminalSize` derives
   the width from the writer being an `*os.File`; rendering into a buffer would
   make every table unbounded — the same defect `watch` has today. Add
   `Options.SetDisplayWidth(int)` so the watch layer measures the real terminal
   once per tick and passes it down. Height-clipping (`+N more rows`) lives in
   the watch layer.
3. **Highlighting must not break the escape-stripping invariant.**
   `output.stripControl` deliberately removes ANSI from every server-supplied
   string so a hostile resource name cannot rewrite the operator's terminal.
   Diff colors therefore cannot be injected into cell values. Add an optional
   `Table.Highlight [][]bool` (or a `CellStyle` hook) applied **after**
   sanitising and after width measurement, so escapes still only ever originate
   in koc.
4. **`internal/cli/resolve`: memoize name→ID for the process.** Otherwise a
   watched `--project foo` re-queries Keystone every tick. Short TTL (~5 min).
5. **Flags are local, not persistent.** `--wait` exists on 15 write commands
   (`server create`, `node deploy`, `volume …`). A root-persistent `--watch`
   would add candidates to every `--w…` prefix on all of them, and
   `ExpandFlagPrefixes` leaves ambiguous prefixes alone. Registering `--watch`
   only on read verbs keeps the two flag sets disjoint. `-w` is unused as a
   shorthand anywhere in the tree.
6. **stderr goes into the frame.** Commands that warn mid-list (e.g. the
   partial-failure path in `server/migration.go`) must not scribble over the
   alternate screen; captured warnings render under the table.
7. **`--debug` and `--timing` are refused with `--watch`** (or force
   `--watch-plain`): a request-dump per tick destroys the frame, and
   `auth.ReportTiming`'s totals are meaningless over an unbounded loop — the
   status line reports per-tick latency instead.

## 6. Phasing

All three phases are in scope. They stay ordered because each one's risk is
different and phase 1 is independently shippable if the later ones stall.

- **Phase 1 — the loop.** `internal/watch.Run`, the `RunE` wrapper, the
  annotation pass, auth memoization, explicit render width, status line, TTY
  repaint + non-TTY append, error tolerance and backoff. This alone removes the
  restart tax and fixes the clipped-table and no-color defects.
- **Phase 2 — the diff.** Cell-level highlighting via the `Table.Highlight` hook
  (§5.3 — it touches `internal/output`'s sanitising path, so it is reviewed on
  its own), row reconciliation by identity, `--watch-count`,
  `--watch-until-change`, resolve memoization.
- **Phase 3 — the keys.** Interactive control via `term.MakeRaw`: `q` quit,
  `space`/`r` refresh now, `p` pause/resume, `+`/`-` adjust interval, `d` toggle
  diff. Raw mode means koc owns the terminal state and Ctrl-C on **every** exit
  path, so it carries the most risk of leaving a wedged terminal:
  - `term.MakeRaw` on entry, `term.Restore` in a `defer` **and** from the
    signal path, ordered so the alternate screen is left after the mode is
    restored.
  - In raw mode the tty no longer generates SIGINT, so `0x03` is read as a byte
    and must cancel the context by hand — the exit-130 path in `cmd/koc/main.go`
    stays the single place that reports it.
  - Key reads run on their own goroutine feeding a channel; a `select` over
    {tick, key, ctx.Done} keeps the loop single-threaded. Never block a refresh
    on a key read.
  - Raw mode is entered only when **stdin** is a terminal, independently of
    stdout: `koc … --watch | tee` keeps working, it just has no keys.
  - `SIGWINCH` forces a repaint at the new width (Linux/macOS); on Windows the
    size is re-measured each tick anyway, which is why width is measured per
    frame rather than once at startup.

## 7. Adjacent, separately decidable

- **Token cache on disk** (`$XDG_CACHE_HOME/koc/tokens`, `0600`, keyed by
  cloud+user+scope, expiry-aware, **opt-in**). This is what `aws`/`gcloud`/
  `kubectl` do, and it speeds up *every* scripted loop — including the existing
  `watch -n1 koc …` muscle memory, with no flag change. It is deliberately not
  part of `--watch`: a cached Keystone token is a bearer credential at rest and
  needs its own security review before it is ever on by default.
- **Incremental polling via `changes-since`.** Nova can return only servers
  updated since the last tick, which would make a watched `server list` nearly
  free on a large host. It needs local merge state and `--deleted` handling for
  tombstones, so it is an optimisation to layer on once Phase 1 is proven — not
  a v1 requirement.

## 8. Rejected

- **A TUI framework** (bubbletea/tview): a large dependency tree against a
  vendored, air-gapped, size-gated single binary (`make size`, `make crossbuild`).
  The whole feature is ~400 lines of ANSI and a ticker.
- **`koc watch <command …>`** as a subcommand shelling out to itself: re-exec
  per tick, i.e. exactly the cost we are removing, plus a `docs/coverage.md`
  entry for a command that is not an OpenStack noun.
- **Server-side watch**: does not exist in Nova, Ironic, Cinder, Neutron,
  Keystone, Glance, Designate or Octavia.
- **Overloading `--wait`**: different contract (run until a terminal state, then
  exit) and it is already implemented per-command against specific state
  machines.

## 9. Testing

`Run`'s seam takes a `render` func and a writer, so the loop is tested with a
fake clock and a scripted renderer — no auth, no HTTP, no terminal:

- N ticks then context cancel; `--watch-count` termination; `--watch-until-change`
  exit code.
- transient error → last good frame retained, status line marked stale, loop
  continues; `--watch-errors exit` → non-zero.
- fatal (401) → immediate exit regardless of the errors mode.
- overlapping slow refresh → tick skipped, not queued.
- non-TTY → byte-for-byte free of escape sequences; CSV header emitted once.
- alternate screen entered and left exactly once, including on cancel.
- diff reconciliation across re-ordered, added and removed rows.
- golden frames alongside the existing `internal/output/golden_test.go`.
- keys (phase 3): a scripted key channel drives pause → no refresh issued,
  `space` → exactly one extra refresh, `+`/`-` → interval changed, `0x03` →
  context cancelled; and terminal restore runs exactly once on every exit path
  (normal, count reached, fatal error, cancel, panic).

Per-command cost stays at the usual httptest + gophercloud fixtures for one
watched `list`, asserting the second tick issues **no** second token request.
