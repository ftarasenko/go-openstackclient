package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// newTestRunner builds a runner for the key handlers that do not need the whole
// loop: the seams are filled in as Run would, and frames go nowhere.
func newTestRunner(o Options) *runner {
	o.Plain = true
	o.applyDefaults()
	return &runner{
		o:        o,
		out:      io.Discard,
		errOut:   io.Discard,
		differ:   NewDiffer(false),
		screen:   newScreen(io.Discard, o),
		interval: o.Interval,
	}
}

// The key tests block the clock, so the refresh deadline never comes due and
// nothing but a keypress can move the loop. Without that, a ready timer and a
// ready key channel would both be selectable and Go would pick between them at
// random.

// keyed scripts keys pressed while the loop is *idle*: one is delivered each
// time the loop asks how long to sleep, which only the wait does. Keys pressed
// while a refresh is in flight take a different path now (see
// TestStopKeyAbortsTheRefreshInFlight), so the two cases are driven apart
// rather than left to whichever branch the select happens to pick.
func (h *harness) keyed(t *testing.T, o Options, keys ...keyCode) error {
	t.Helper()
	h.clock.blocked = true
	pending := keys
	h.clock.onWait = func() {
		if len(pending) == 0 {
			return
		}
		h.keys <- pending[0]
		pending = pending[1:]
	}
	o.Keys = true
	return h.run(context.Background(), o)
}

func TestKeyQuitStopsCleanly(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")

	if err := h.keyed(t, Options{Interval: time.Second}, keyQuit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.frames != 1 {
		t.Errorf("rendered %d frames, want 1 (the first, then quit)", h.frames)
	}
	if h.restores != 1 {
		t.Errorf("the terminal mode was restored %d times, want exactly 1", h.restores)
	}
}

func TestKeyRefreshRendersExactlyOneExtraFrame(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")

	if err := h.keyed(t, Options{Interval: time.Second}, keySpace, keyRefresh, keyQuit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The first frame, then one per refresh key.
	if h.frames != 3 {
		t.Errorf("rendered %d frames, want 3", h.frames)
	}
}

func TestKeyPauseStopsRefreshing(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")

	// Pause, then a key that is not bound to anything, then quit. Nothing
	// between the pause and the quit may refresh.
	if err := h.keyed(t, Options{Interval: time.Second}, keyPause, 'z', keyQuit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.frames != 1 {
		t.Errorf("rendered %d frames while paused, want 1 (the frame before the pause)", h.frames)
	}
}

func TestKeyPauseResumeRefreshesAtOnce(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")

	if err := h.keyed(t, Options{Interval: time.Second}, keyPause, keyPause, keyQuit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Resuming refreshes immediately rather than waiting out the interval,
	// which is what the operator who just pressed the key is asking for.
	if h.frames != 2 {
		t.Errorf("rendered %d frames, want 2", h.frames)
	}
}

func TestKeyIntervalAdjustment(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start time.Duration
		key   keyCode
		want  time.Duration
	}{
		{"slower by a second", 2 * time.Second, keySlower, 3 * time.Second},
		{"slower on the unshifted key", 2 * time.Second, keySlowerAlt, 3 * time.Second},
		{"faster by a second", 3 * time.Second, keyFaster, 2 * time.Second},
		{"sub-second steps", 500 * time.Millisecond, keySlower, 750 * time.Millisecond},
		{"one second steps down to 750ms", time.Second, keyFaster, 750 * time.Millisecond},
		{"floored at the minimum", MinInterval, keyFaster, MinInterval},
		{"capped at the maximum", MaxInterval, keySlower, MaxInterval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRunner(Options{Interval: tc.start})
			r.key(tc.key, func() {})
			if r.interval != tc.want {
				t.Errorf("interval %s, want %s", r.interval, tc.want)
			}
			if r.o.Interval != tc.want {
				t.Errorf("an explicit change must also reset the backoff baseline: %s", r.o.Interval)
			}
		})
	}
}

func TestKeyDiffToggle(t *testing.T) {
	r := newTestRunner(Options{Interval: time.Second})
	r.differ = NewDiffer(true)

	if ev := r.key(keyDiff, func() {}); ev != evTick {
		t.Errorf("the diff key returned %v, want an immediate refresh so the change is visible", ev)
	}
	if r.differ.Enabled() {
		t.Error("the diff key did not turn highlighting off")
	}
	r.key(keyDiff, func() {})
	if !r.differ.Enabled() {
		t.Error("the diff key did not turn highlighting back on")
	}
}

func TestKeyCtrlCCancelsTheContext(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")

	// In raw mode the tty no longer turns Ctrl-C into SIGINT, so the loop has to
	// re-create the interrupt itself. The error has to come back as
	// context.Canceled, because cmd/koc's exit-130 path is the single place an
	// interrupt is reported.
	err := h.keyed(t, Options{Interval: time.Second}, keyETX)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if h.restores != 1 {
		t.Errorf("the terminal mode was restored %d times, want exactly 1", h.restores)
	}
	painted := h.out.String()
	if n := strings.Count(painted, altScreenOff); n != 1 {
		t.Errorf("left the alternate screen %d times, want exactly 1", n)
	}
}

func TestTerminalRestoredOnEveryExitPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    Options
		keys []keyCode
		run  func(*harness)
	}{
		{name: "count reached", o: Options{Interval: time.Second, Count: 1}},
		{name: "quit key", o: Options{Interval: time.Second}, keys: []keyCode{keyQuit}},
		{name: "interrupt", o: Options{Interval: time.Second}, keys: []keyCode{keyETX}},
		{name: "fatal error", o: Options{Interval: time.Second}, run: func(h *harness) {
			h.render = func(int, io.Writer, io.Writer) error { return errors.New("settled") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			h.render = staticFrames("row\n")
			if tc.run != nil {
				tc.run(h)
			}
			if len(tc.keys) > 0 {
				_ = h.keyed(t, tc.o, tc.keys...)
			} else {
				o := tc.o
				o.Keys = true
				_ = h.run(context.Background(), o)
			}
			if h.restores != 1 {
				t.Errorf("the terminal mode was restored %d times, want exactly 1", h.restores)
			}
			painted := h.out.String()
			if n := strings.Count(painted, cursorShow); n != 1 {
				t.Errorf("the cursor was restored %d times, want exactly 1", n)
			}
			if n := strings.Count(painted, altScreenOff); n != 1 {
				t.Errorf("left the alternate screen %d times, want exactly 1", n)
			}
		})
	}
}

func TestRawModeFailureStillWatches(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")
	o := h.options(Options{Interval: time.Second, Count: 2, Keys: true})
	o.rawMode = func() (func(), error) { return nil, errors.New("not a terminal") }

	// Keys are a convenience. A terminal that refuses raw mode loses them and
	// keeps the watch, rather than the other way round.
	err := Run(context.Background(), o, &h.out, &h.errOut, func(_ context.Context, out, warn io.Writer) error {
		h.frames++
		return h.render(h.frames, out, warn)
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.frames != 2 {
		t.Errorf("rendered %d frames, want 2", h.frames)
	}
}

func TestPlainModeHasNoKeys(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")
	o := h.options(Options{Interval: time.Second, Count: 1, Keys: true, Plain: true})

	err := Run(context.Background(), o, &h.out, &h.errOut, func(_ context.Context, out, warn io.Writer) error {
		h.frames++
		return h.render(h.frames, out, warn)
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.restores != 0 {
		t.Error("appending frames to a stream must not put the terminal into raw mode")
	}
}

func TestWindowResizeRepaints(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")
	h.clock.blocked = true
	winch := make(chan struct{}, 1)
	winch <- struct{}{}

	o := h.options(Options{Interval: time.Second, Keys: true})
	o.winch = winch
	go func() {
		// The resize repaints without refreshing; the quit key then ends the run.
		time.Sleep(10 * time.Millisecond)
		h.keys <- keyQuit
	}()
	if err := Run(context.Background(), o, &h.out, &h.errOut, func(_ context.Context, out, warn io.Writer) error {
		h.frames++
		return h.render(h.frames, out, warn)
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.frames != 1 {
		t.Errorf("rendered %d frames, want 1 — a resize repaints, it does not refresh", h.frames)
	}
	// The first frame's paint plus the resize repaint.
	if n := strings.Count(h.out.String(), homeErase); n < 2 {
		t.Errorf("painted %d times, want at least 2 (the frame and the resize)", n)
	}
}

// midRefreshKeys delivers keys from inside the render itself, on an unbuffered
// channel: the send completes only when the loop's refresh watcher takes it, so
// "pressed while the round trip is in flight" is exact rather than timed.
func (h *harness) midRefreshKeys(o Options, atFrame int, keys ...keyCode) (Options, func(int, io.Writer, io.Writer) error) {
	ch := make(chan keyCode)
	o = h.options(o)
	o.keys = ch
	o.Keys = true
	// Blocking the clock keeps a later tick from racing the keys — but only
	// once the loop has reached the frame being scripted, so a test that needs
	// an earlier frame on screen first leaves the clock running.
	h.clock.blocked = atFrame == 1
	inner := h.render
	return o, func(n int, out, warn io.Writer) error {
		if n == atFrame {
			h.clock.blocked = true
			for _, k := range keys {
				ch <- k
			}
		}
		return inner(n, out, warn)
	}
}

func TestStopKeyAbortsTheRefreshInFlight(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     keyCode
		wantErr error
	}{
		{"quit", keyQuit, nil},
		{"interrupt", keyETX, context.Canceled},
		{"eof", keyEOT, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			var renderCtx context.Context
			h.render = func(_ int, out, _ io.Writer) error {
				_, err := io.WriteString(out, "row\n")
				return err
			}
			o, render := h.midRefreshKeys(Options{Interval: time.Second}, 1, tc.key)

			err := Run(context.Background(), o, &h.out, &h.errOut,
				func(ctx context.Context, out, warn io.Writer) error {
					renderCtx = ctx
					h.frames++
					return render(h.frames, out, warn)
				})

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Run returned %v, want %v", err, tc.wantErr)
			}
			// The refresh was cut short rather than waited out: a fleet query
			// that takes seconds must not make q the slowest key to answer.
			if renderCtx == nil || renderCtx.Err() == nil {
				t.Error("the in-flight refresh was not cancelled")
			}
			if h.restores != 1 {
				t.Errorf("the terminal mode was restored %d times, want exactly 1", h.restores)
			}
		})
	}
}

func TestAbortedRefreshIsNotReportedAsAFailure(t *testing.T) {
	h := newHarness()
	h.render = func(_ int, out, _ io.Writer) error {
		_, _ = io.WriteString(out, "row\n")
		// What a cancelled round trip actually returns.
		return fmt.Errorf("listing servers: %w", context.Canceled)
	}
	o, render := h.midRefreshKeys(Options{Interval: time.Second}, 1, keyQuit)

	err := Run(context.Background(), o, &h.out, &h.errOut,
		func(_ context.Context, out, warn io.Writer) error {
			h.frames++
			return render(h.frames, out, warn)
		})
	// koc cancelled it, so it is not an endpoint failure and 'q' still means a
	// clean exit.
	if err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
}

func TestKeysHeldBackDuringARefreshAreAppliedInOrder(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")
	// 'd' touches the differ the render is calling into, so it queues rather
	// than being acted on mid-flight; 'q' then ends the run.
	o, render := h.midRefreshKeys(Options{Interval: time.Second, Diff: true}, 1, keyDiff, keyQuit)

	err := Run(context.Background(), o, &h.out, &h.errOut,
		func(_ context.Context, out, warn io.Writer) error {
			h.frames++
			return render(h.frames, out, warn)
		})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 'q' aborts the first refresh, so 'd' is still queued and never runs —
	// which is the point: it was not silently dropped into the differ while the
	// output layer was inside it.
	if h.frames != 1 {
		t.Errorf("rendered %d frames, want 1", h.frames)
	}
}

func TestPresentationKeyActsDuringARefresh(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")
	// '?' only changes what is painted, so it is safe to answer while the round
	// trip is still out — which is the difference between a terminal that feels
	// alive and one that does not.
	o, render := h.midRefreshKeys(Options{Interval: time.Second}, 1, keyHelp, keyQuit)

	err := Run(context.Background(), o, &h.out, &h.errOut,
		func(_ context.Context, out, warn io.Writer) error {
			h.frames++
			return render(h.frames, out, warn)
		})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(h.out.String(), "koc --watch — keys") {
		t.Errorf("the key map was not painted mid-refresh:\n%q", h.out.String())
	}
}
