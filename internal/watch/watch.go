// Package watch turns any read-only koc command into a live-refreshing view:
// one process, one authenticated session, a ticker, and a frame repainted in
// place.
//
// It replaces the `watch -n1 koc …` idiom, which re-executes the binary every
// tick. For koc that is not a redraw but a full cold start — a TLS handshake to
// Keystone, a fresh Fernet token, a catalog parse and every name→ID lookup
// again — so at -n1 a single operator terminal is a sustained ~2 req/s load on
// the control plane, of which only half is the request the operator wanted.
// Re-execution also degrades the display: the command's stdout is a pipe, so the
// table renders unbounded and watch(1) hard-clips it, colour is switched off,
// scrollback is lost to the per-tick clear, and one transient 5xx blanks the
// screen instead of holding the last good state.
//
// The loop here fixes all of those: it authenticates once (see
// internal/auth's memoized authenticated), measures the real terminal and hands
// the width down explicitly (output.Options.SetDisplayWidth), renders each frame
// into a buffer and writes it in one call inside the alternate screen buffer,
// and keeps the last good frame on screen when a refresh fails.
//
// Nothing about a specific command lives here. Run takes a Renderer — a
// closure over the command's own RunE — so all 200-odd read verbs are watchable
// without touching a single command file; internal/cli/watch.go does that
// wiring.
package watch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Renderer produces exactly one frame. Everything the command would have
// written to stdout goes to out and everything it would have written to stderr
// goes to warn, so a mid-list warning is rendered under the table rather than
// scribbled over the alternate screen.
//
// A Renderer is called once per tick and must be safe to call repeatedly; the
// loop never calls it concurrently with itself.
type Renderer func(ctx context.Context, out, warn io.Writer) error

// Error modes for Options.ErrorMode, mirroring watch(1)'s -e.
const (
	// ErrorsTolerate keeps the last good frame on screen and carries on after a
	// failed refresh. It is the default: a fleet-wide `server list --all` hits a
	// transient 5xx often enough that exiting on one would make the feature
	// useless.
	ErrorsTolerate = "tolerate"
	// ErrorsExit stops at the first failed refresh, like `watch -e`.
	ErrorsExit = "exit"
)

// MinInterval is the shortest refresh interval accepted. watch(1) allows 0.1s;
// koc does not, because a koc tick is an authenticated API call against a
// shared control plane rather than a local command, and four of them a second
// per terminal is not a load an operator should be able to ask for by typo.
const MinInterval = 250 * time.Millisecond

// MaxInterval caps the interval the +/- keys can reach.
const MaxInterval = time.Hour

// DefaultInterval is what a bare --watch means. It matches watch(1)'s own
// default so the muscle memory carries over.
const DefaultInterval = 2 * time.Second

// Options is the resolved --watch configuration for one command. The cli layer
// builds it from the flags (see internal/cli/watch.go); nothing here parses a
// command line.
type Options struct {
	// Interval is the target time between the *starts* of two refreshes.
	Interval time.Duration

	// Diff highlights what changed since the previous frame. Ignored in Plain
	// mode, which emits no escape sequences at all.
	Diff bool

	// Count stops the loop after this many refreshes; 0 means forever.
	Count int

	// ErrorMode is ErrorsTolerate or ErrorsExit.
	ErrorMode string

	// UntilChange exits 0 as soon as a refresh differs from the one before it,
	// like `watch -g`.
	UntilChange bool

	// Plain appends whole frames to the stream instead of repainting one in
	// place, and emits no escape sequences. It is forced on when the output is
	// not a terminal, so `koc server list --watch -f json | jq .` stays a clean
	// stream of snapshots.
	Plain bool

	// NoTitle suppresses the status line, like `watch -t`.
	NoTitle bool

	// Keys enables the interactive controls (q, space/r, p, +/-, d). It is set
	// only when standard input is a terminal — independently of standard
	// output, so `koc … --watch | tee` still works, it just has no keys.
	Keys bool

	// Title is the status line's left-hand side: the command line being
	// refreshed.
	Title string

	// Compact renders one physical line per table row. See
	// output.Options.SetCompactRows, which the cli layer calls when this is set.
	Compact bool

	// CSVHeaderOnce suppresses the repeated header line in appended frames. Set
	// by the cli layer when -f csv is in effect; a stream of snapshots with a
	// header wedged between each pair is not CSV anyone can read back.
	CSVHeaderOnce bool

	// Differ reconciles consecutive frames. The cli layer supplies the one it
	// has also installed as the output layer's Highlighter, so the loop and the
	// renderer agree on what changed; Run makes its own when this is nil.
	Differ *Differ

	// Seams. All nil in a normal build, meaning the real clock, the real
	// terminal and the real keyboard. Tests live in this package and set them
	// directly; see watch_test.go.
	now     func() time.Time
	after   func(time.Duration) <-chan time.Time
	size    func() (cols, rows int)
	rawMode func() (restore func(), err error)
	keys    <-chan keyCode
	winch   <-chan struct{}
}

// runner is one Run in progress. It exists so the loop's steps can be small
// methods over shared state rather than one long function.
type runner struct {
	o      Options
	out    io.Writer
	errOut io.Writer
	render Renderer
	differ *Differ
	screen *screen

	// interval is the interval actually in force, which the +/- keys and the
	// latency backoff move away from o.Interval.
	interval time.Duration

	frames   int       // refreshes that actually rendered a frame
	attempts int       // refreshes tried, failures included
	errs     int       // consecutive failed refreshes
	lastErr  error     // the most recent refresh failure
	tickAt   time.Time // when the most recent refresh started
	lastOK   time.Time // when the frame currently on screen was rendered
	// view windows the frame, and helpView does the same for the key map, so
	// closing the map puts the operator back where they were in the list rather
	// than at the top of it.
	view     viewport
	helpView viewport
	// lastFrame is the window the current paint resolved to, so the status line
	// can say where it sits. It is filled before the status line is composed.
	lastFrame frame

	paused   bool
	showHelp bool // the '?' key map is open, in place of the frame
	force    bool // refresh on the next pass even if paused
	skipped  int  // refreshes dropped because the previous one overran

	// deferred holds keys that arrived while a refresh was in flight and could
	// not be acted on until it finished. They are drained, in order, before the
	// loop waits on anything else.
	deferred []keyCode

	// stopWinch unsubscribes from window-resize notifications.
	stopWinch func()
}

// Run drives render on a ticker until ctx ends, the refresh count is reached, a
// fatal error occurs, or the operator quits.
//
// out receives the frames; errOut receives loop-level failures in Plain mode
// (in repaint mode they are reported on the status line instead, so they cannot
// scribble over the frame). Run restores the terminal on every exit path —
// normal end, count reached, fatal error, cancellation, panic — because the
// restore is deferred immediately after the terminal is claimed and nothing
// between the two can return.
func Run(ctx context.Context, o Options, out, errOut io.Writer, render Renderer) error {
	o.applyDefaults()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	r := &runner{
		o:        o,
		out:      out,
		errOut:   errOut,
		render:   render,
		differ:   o.Differ,
		interval: o.Interval,
		force:    true,
	}
	if r.differ == nil {
		r.differ = NewDiffer(o.Diff && !o.Plain)
	}
	r.screen = newScreen(out, o)

	restore, err := r.screen.enter()
	if err != nil {
		return err
	}
	defer restore()
	// enter() may have found the terminal refuses raw mode, in which case the
	// loop runs without keys rather than not at all.
	r.o.Keys = r.screen.keys

	r.attachInputs()
	defer r.detachInputs()

	return r.loop(ctx, cancel)
}

// attachInputs subscribes to the keyboard and to window resizes, unless a test
// has already supplied either.
func (r *runner) attachInputs() {
	if r.o.Keys && r.o.keys == nil {
		r.o.keys = readKeys(os.Stdin)
	}
	if !r.o.Plain && r.o.winch == nil {
		r.o.winch, r.stopWinch = winchSignals()
	}
}

func (r *runner) detachInputs() {
	if r.stopWinch != nil {
		r.stopWinch()
	}
}

func (o *Options) applyDefaults() {
	if o.Plain {
		// Appending frames to a stream and reading single keys off the terminal
		// are two different tools; only the repainting one has a screen for a
		// keypress to affect.
		o.Keys = false
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.ErrorMode == "" {
		o.ErrorMode = ErrorsTolerate
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.after == nil {
		o.after = time.After
	}
}

// loop is the single-threaded heart of the feature: refresh, paint, wait,
// repeat. Every event — the tick, a keypress, a window resize, cancellation —
// is funnelled through one select in wait, so nothing here needs a lock and a
// refresh never races a repaint.
func (r *runner) loop(ctx context.Context, cancel context.CancelFunc) error {
	for {
		// Checked before each refresh, not only in the wait below: once the
		// deadline is due, its channel and ctx.Done() are both ready and a
		// select would choose between them at random, so a cancelled watch
		// could render another frame or three on the way out.
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.force || !r.paused {
			r.force = false
			stop, err := r.tick(ctx, cancel)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
		}
		ev, err := r.wait(ctx, cancel)
		if err != nil {
			return err
		}
		if ev == evQuit {
			return nil
		}
	}
}

// tick performs one refresh and paints the result. It reports stop when the
// loop has reached a normal end (--watch-count exhausted, --watch-until-change
// satisfied).
func (r *runner) tick(ctx context.Context, cancel context.CancelFunc) (bool, error) {
	started := r.o.now()
	r.tickAt = started
	r.attempts++
	var body, warn bytes.Buffer

	r.differ.BeginFrame()
	aborted, err := r.refresh(ctx, &body, &warn)
	latency := r.o.now().Sub(started)

	if aborted != 0 {
		// The refresh was cut short on purpose, so whatever it returned is
		// koc's own cancellation rather than anything the endpoint did. The key
		// that stopped it is acted on here instead.
		r.differ.Rollback()
		return r.key(aborted, cancel) == evQuit, nil
	}
	if err != nil {
		r.differ.Rollback()
		if fatal := r.recordError(err); fatal != nil {
			return false, fatal
		}
		r.paint()
		// A failed refresh is still a refresh: --watch-count counts attempts,
		// so an endpoint that is down cannot turn a bounded watch into an
		// unbounded one.
		return r.countReached(), nil
	}

	r.differ.Commit()
	// A frame rendered as JSON or YAML never reaches the output layer's table
	// path, so the differ sees nothing and cannot say whether anything moved.
	// Comparing the frame's bytes against the one before it is exact for those
	// formats, and --watch-until-change has to work in all of them.
	byteChange := r.differ.Tables() == 0 && r.frames > 0 && !bytes.Equal(body.Bytes(), r.screen.body)

	r.errs, r.lastErr = 0, nil
	r.frames++
	r.lastOK = started
	r.screen.accept(body.Bytes(), warn.Bytes(), latency, r.differ.Rows())
	r.backoff(latency)
	r.paint()

	if r.countReached() {
		return true, nil
	}
	// The first frame is "all new" by construction, so a change is only
	// meaningful from the second one on.
	if r.o.UntilChange && r.frames > 1 && (r.differ.Changed() || byteChange) {
		return true, nil
	}
	return false, nil
}

// countReached reports whether --watch-count has been satisfied.
func (r *runner) countReached() bool { return r.o.Count > 0 && r.attempts >= r.o.Count }

// refresh runs one render, watching the keyboard while it is in flight.
//
// The render goes on its own goroutine so this one stays free to answer keys:
// without that, a refresh is a hole in the loop as long as the round trip, and
// `q` on a four-second fleet query took four seconds to be noticed — measured,
// not supposed. It returns the render's error, and the key that aborted it if
// one did.
//
// The two goroutines share nothing that is written on both sides. The render
// writes only into out and warn, which this goroutine does not read until the
// channel receive below has already ordered the two; and the keys acted on
// mid-flight are the ones classifyKey calls immediate, which touch the
// interval, the pause state and the help panel — never the differ the render is
// calling into. Everything else queues.
func (r *runner) refresh(ctx context.Context, out, warn io.Writer) (keyCode, error) {
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()

	done := make(chan error, 1)
	go func() { done <- r.render(rctx, out, warn) }()

	var aborted keyCode
	for {
		select {
		case err := <-done:
			return aborted, err
		case b, ok := <-r.o.keys:
			if !ok {
				r.o.keys = nil
				continue
			}
			switch classifyKey(b) {
			case keyAbort:
				if aborted == 0 {
					aborted = b
					rcancel() // and keep waiting, so the goroutine is joined
				}
			case keyImmediate:
				r.key(b, rcancel)
			case keyDeferred:
				r.deferred = append(r.deferred, b)
			}
		}
	}
}

// recordError classifies a failed refresh. It returns non-nil when the loop
// must stop: cancellation, a credential the endpoint has already rejected, a
// deterministic failure before any frame ever succeeded, or --watch-errors exit.
func (r *runner) recordError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	r.errs++
	r.lastErr = err

	switch {
	case isAuthFailure(err):
		// Looping on a rejected credential hammers Keystone and, where lockout
		// is configured, is how an operator locks their own account out. Fatal
		// whatever --watch-errors says.
		return err
	case r.frames == 0 && !isTransient(err):
		// Nothing has ever rendered, and the failure is not the kind that comes
		// and goes — an unknown column, a bad filter, a service missing from the
		// catalog. There is no last good frame to hold and no reason to expect a
		// different answer next second.
		return err
	case r.o.ErrorMode == ErrorsExit:
		return err
	}

	r.screen.reject(err)
	if r.o.Plain {
		fmt.Fprintf(r.errOut, "koc: %v\n", err) //nolint:errcheck // the stream is already failing; there is nowhere better to report it
	}
	return nil
}

// backoff widens the interval when refreshes are slow enough that the loop
// would otherwise spend most of its time in flight. A refresh taking more than
// half the interval means the control plane is the bottleneck, and asking it
// for more is how a monitoring loop turns into an outage; the status line says
// when this is in force, so it is never a silent slowdown.
func (r *runner) backoff(latency time.Duration) {
	want := r.o.Interval
	if latency*2 > want {
		want = latency * 2
	}
	if want > MaxInterval {
		want = MaxInterval
	}
	r.interval = want
}

// paint renders the current state onto the display: the frame, or the key map
// when '?' is open.
//
// The window is resolved here rather than in the painter because the status
// line has to report where in the frame it lands, and the status line is
// composed before anything is written.
func (r *runner) paint() {
	if r.o.Plain {
		r.screen.paint("", frame{}, false)
		return
	}
	lines, vp := r.frameLines()
	_, rows := r.screen.size()
	reserve := 0
	if !r.o.NoTitle {
		reserve = 2
	}
	r.lastFrame = vp.apply(lines, rows, reserve)
	r.screen.paint(r.status(), r.lastFrame, r.showHelp)
}

// frameLines is what should be on screen, with the viewport that windows it.
func (r *runner) frameLines() ([]string, *viewport) {
	if r.showHelp {
		return r.helpLines(), &r.helpView
	}
	return frameLines(r.screen.body, r.screen.warn), &r.view
}

// scrollView is the window the scroll keys move: whichever of the two is on
// screen.
func (r *runner) scrollView() *viewport {
	if r.showHelp {
		return &r.helpView
	}
	return &r.view
}

// wait blocks until the next refresh is due, returning early for a keypress
// that asks for one. Because the refresh itself runs on this goroutine, ticks
// can never overlap: a refresh that outlives its interval simply makes the next
// deadline already past, which is counted as a skip rather than queued behind
// it.
func (r *runner) wait(ctx context.Context, cancel context.CancelFunc) (event, error) {
	for {
		// Keys held back during the refresh come first, in the order they were
		// pressed, so nothing is lost and nothing arrives out of turn.
		if len(r.deferred) > 0 {
			b := r.deferred[0]
			r.deferred = r.deferred[1:]
			if ev := r.key(b, cancel); ev != evNone {
				return ev, nil
			}
			continue
		}
		d := r.untilNext()
		if d <= 0 {
			r.noteSkips()
			return evTick, nil
		}
		select {
		case <-ctx.Done():
			return evQuit, ctx.Err()
		case <-r.o.after(d):
			return evTick, nil
		case <-r.o.winch:
			// A resize changes the width every table was fitted to, so the
			// frame on screen is now wrong. Repainting is not enough on its own
			// — the body was rendered at the old width — but it is what can be
			// done without going back to the API, and the next refresh fixes
			// the rest.
			r.paint()
		case b, ok := <-r.o.keys:
			if !ok {
				r.o.keys = nil
				continue
			}
			switch r.key(b, cancel) {
			case evQuit:
				return evQuit, nil
			case evTick:
				return evTick, nil
			case evNone:
			}
		}
	}
}

// untilNext reports how long is left before the next refresh is due. A paused
// loop is never due: it waits for a key.
func (r *runner) untilNext() time.Duration {
	if r.paused {
		return MaxInterval
	}
	return r.tickAt.Add(r.interval).Sub(r.o.now())
}

// noteSkips counts refreshes whose deadline had already passed by the time the
// loop reached it. They are dropped, not queued — a loop that works through its
// own backlog against a struggling control plane is the failure mode this
// feature exists to avoid.
//
// A slow endpoint does not normally get here: backoff has already widened the
// interval to twice the last refresh's latency, so the next deadline is still
// ahead. What does get here is the clock moving under the loop — an NTP step on
// a host that has been up a while — which would otherwise look like nothing at
// all.
func (r *runner) noteSkips() {
	overdue := r.o.now().Sub(r.tickAt.Add(r.interval))
	if missed := int(overdue / r.interval); missed > 0 {
		r.skipped += missed
	}
}
