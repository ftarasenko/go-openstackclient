package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
)

// The loop is tested through its two seams — a scripted renderer and a fake
// clock — so none of this needs a terminal, an endpoint, or a real second of
// wall time.

// testClock advances only when the loop asks to wait, and then by exactly the
// amount asked for. The refresh loop is single-threaded, so this makes every
// test below deterministic: no sleeps, no goroutines, no tolerance windows.
type testClock struct {
	now time.Time
	// blocked makes after() return a channel that never fires, so only keys
	// (or cancellation) can move the loop. The key tests use it to keep the
	// select from racing a due tick against a keypress.
	blocked bool
	// onWait runs whenever the loop asks how long to sleep, which only wait()
	// does. It is the exact moment the loop is idle between refreshes, so a key
	// scripted there is a key "pressed while idle" — as opposed to one the
	// refresh itself picks up, which now takes a different path entirely.
	onWait func()
}

func newClock() *testClock {
	return &testClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) After(d time.Duration) <-chan time.Time {
	if c.onWait != nil {
		c.onWait()
	}
	if c.blocked {
		return make(chan time.Time)
	}
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// harness wires a Run with every seam under the test's control.
type harness struct {
	clock    *testClock
	out      strings.Builder
	errOut   strings.Builder
	frames   int
	restores int
	keys     chan keyCode

	// render is the scripted renderer; frame is its call count (1-based).
	render func(frame int, out, warn io.Writer) error
}

func newHarness() *harness {
	return &harness{clock: newClock(), keys: make(chan keyCode, 16)}
}

func (h *harness) options(o Options) Options {
	o.now = h.clock.Now
	o.after = h.clock.After
	o.size = func() (int, int) { return 100, 24 }
	o.winch = make(chan struct{}) // never fires; keeps the real SIGWINCH out
	o.keys = h.keys
	o.rawMode = func() (func(), error) {
		return func() { h.restores++ }, nil
	}
	return o
}

func (h *harness) run(ctx context.Context, o Options) error {
	return Run(ctx, h.options(o), &h.out, &h.errOut, func(_ context.Context, out, warn io.Writer) error {
		h.frames++
		return h.render(h.frames, out, warn)
	})
}

// staticFrames renders the given body on each call, cycling if the loop outruns
// the script.
func staticFrames(bodies ...string) func(int, io.Writer, io.Writer) error {
	return func(n int, out, _ io.Writer) error {
		body := bodies[(n-1)%len(bodies)]
		_, err := io.WriteString(out, body)
		return err
	}
}

func httpErr(code int) error {
	return gophercloud.ErrUnexpectedResponseCode{
		URL:    "http://example.com/v2.1/servers/detail",
		Method: "GET",
		Actual: code,
	}
}

func TestRunStopsAtCount(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")

	if err := h.run(context.Background(), Options{Interval: time.Second, Count: 3}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.frames != 3 {
		t.Fatalf("rendered %d frames, want 3", h.frames)
	}
	// Three refreshes at a one-second interval means two waits: the loop stops
	// on the third rather than sleeping again after it.
	if got, want := h.clock.now.Sub(newClock().now), 2*time.Second; got != want {
		t.Errorf("elapsed %s, want %s", got, want)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := newHarness()
	h.render = func(n int, out, _ io.Writer) error {
		if n == 2 {
			cancel()
		}
		_, err := io.WriteString(out, "row\n")
		return err
	}

	err := h.run(ctx, Options{Interval: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if h.frames != 2 {
		t.Errorf("rendered %d frames, want 2", h.frames)
	}
}

func TestRunUntilChange(t *testing.T) {
	h := newHarness()
	// Identical frames are not a change; the third one is.
	h.render = staticFrames("a\n", "a\n", "b\n", "b\n")

	if err := h.run(context.Background(), Options{Interval: time.Second, UntilChange: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.frames != 3 {
		t.Fatalf("rendered %d frames, want 3 (stop on the first frame that differs)", h.frames)
	}
}

func TestRunUntilChangeIgnoresTheFirstFrame(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("same\n")

	// Every frame is identical, so --watch-until-change must never fire; the
	// count is what ends this run. A first frame counted as "changed" (it has
	// nothing to be compared against) would stop it at two.
	if err := h.run(context.Background(), Options{Interval: time.Second, UntilChange: true, Count: 5}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.frames != 5 {
		t.Errorf("rendered %d frames, want 5", h.frames)
	}
}

func TestTransientErrorKeepsTheLastGoodFrame(t *testing.T) {
	h := newHarness()
	h.render = func(n int, out, _ io.Writer) error {
		if n >= 2 {
			return httpErr(http.StatusServiceUnavailable)
		}
		_, err := io.WriteString(out, "the good frame\n")
		return err
	}

	if err := h.run(context.Background(), Options{Interval: time.Second, Count: 4}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.frames != 4 {
		t.Fatalf("rendered %d frames, want 4 — a tolerated error must not stop the loop", h.frames)
	}
	painted := h.out.String()
	if strings.Count(painted, "the good frame") != 4 {
		t.Errorf("the last good frame was not held on screen:\n%q", painted)
	}
	if !strings.Contains(painted, "stale") || !strings.Contains(painted, "503 Service Unavailable") {
		t.Errorf("the status line does not report the failure:\n%q", lastStatus(painted))
	}
}

func TestErrorsExitStopsAtTheFirstFailure(t *testing.T) {
	h := newHarness()
	h.render = func(n int, out, _ io.Writer) error {
		if n == 2 {
			return httpErr(http.StatusServiceUnavailable)
		}
		_, err := io.WriteString(out, "row\n")
		return err
	}

	err := h.run(context.Background(), Options{Interval: time.Second, ErrorMode: ErrorsExit})
	if err == nil {
		t.Fatal("Run returned nil, want the refresh failure")
	}
	if h.frames != 2 {
		t.Errorf("rendered %d frames, want 2", h.frames)
	}
}

func TestAuthFailureIsFatalEvenWhenToleratingErrors(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			h := newHarness()
			h.render = func(n int, out, _ io.Writer) error {
				if n == 2 {
					return httpErr(code)
				}
				_, err := io.WriteString(out, "row\n")
				return err
			}

			err := h.run(context.Background(), Options{Interval: time.Second, ErrorMode: ErrorsTolerate})
			if err == nil {
				t.Fatal("Run returned nil; looping on a rejected credential is what trips account lockout")
			}
			if h.frames != 2 {
				t.Errorf("rendered %d frames, want 2", h.frames)
			}
		})
	}
}

func TestSettledFailureOnTheFirstFrameIsFatal(t *testing.T) {
	h := newHarness()
	h.render = func(int, io.Writer, io.Writer) error {
		return errors.New(`unknown column(s): Statuss`)
	}

	err := h.run(context.Background(), Options{Interval: time.Second, ErrorMode: ErrorsTolerate})
	if err == nil {
		t.Fatal("Run returned nil; a deterministic failure with no frame to hold must not loop")
	}
	if h.frames != 1 {
		t.Errorf("rendered %d frames, want 1", h.frames)
	}
}

func TestTransientFailureOnTheFirstFrameIsTolerated(t *testing.T) {
	h := newHarness()
	h.render = func(n int, out, _ io.Writer) error {
		if n == 1 {
			return httpErr(http.StatusServiceUnavailable)
		}
		_, err := io.WriteString(out, "row\n")
		return err
	}

	if err := h.run(context.Background(), Options{Interval: time.Second, Count: 3}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.frames != 3 {
		t.Errorf("rendered %d frames, want 3", h.frames)
	}
}

func TestSlowRefreshBacksOffRatherThanQueueing(t *testing.T) {
	h := newHarness()
	// Each refresh outlives its interval three times over. The loop must widen
	// the interval and say so, and must never work through the backlog it slept
	// past — the whole point is not to pile requests onto an endpoint that is
	// already struggling.
	h.render = func(_ int, out, _ io.Writer) error {
		h.clock.now = h.clock.now.Add(3 * time.Second)
		_, err := io.WriteString(out, "row\n")
		return err
	}

	start := h.clock.now
	if err := h.run(context.Background(), Options{Interval: time.Second, Count: 3}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	status := lastStatus(h.out.String())
	if !strings.Contains(status, "backoff from 1s") {
		t.Errorf("the status line does not report the backoff: %q", status)
	}
	if !strings.Contains(status, "every 6s") {
		t.Errorf("the interval did not widen to twice the latency: %q", status)
	}
	// Three refreshes of 3s each, with two 3s waits between them (the 6s
	// interval measured from each refresh's start). Anything less would mean the
	// loop ran the missed ticks back to back.
	if got, want := h.clock.now.Sub(start), 15*time.Second; got != want {
		t.Errorf("elapsed %s, want %s", got, want)
	}
}

func TestClockStepCountsSkippedRefreshes(t *testing.T) {
	// A slow endpoint never reaches this path — backoff keeps the next deadline
	// ahead of the clock. A clock that jumps forward does.
	r := &runner{o: Options{Interval: time.Second}, interval: time.Second}
	clock := newClock()
	r.o.now = clock.Now
	r.tickAt = clock.now
	clock.now = clock.now.Add(5 * time.Second)

	r.noteSkips()
	if r.skipped != 4 {
		t.Errorf("counted %d skipped refreshes, want 4", r.skipped)
	}
}

func TestBackoffWidensTheIntervalWhenRefreshesAreSlow(t *testing.T) {
	r := &runner{o: Options{Interval: time.Second}}
	r.backoff(100 * time.Millisecond)
	if r.interval != time.Second {
		t.Errorf("a fast refresh moved the interval to %s", r.interval)
	}
	r.backoff(2 * time.Second)
	if r.interval != 4*time.Second {
		t.Errorf("interval %s after a 2s refresh, want 4s (twice the latency)", r.interval)
	}
	r.backoff(2 * MaxInterval)
	if r.interval != MaxInterval {
		t.Errorf("interval %s, want it capped at %s", r.interval, MaxInterval)
	}
}

func TestRepaintEntersAndLeavesTheAlternateScreenExactlyOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := newHarness()
	h.render = func(n int, out, _ io.Writer) error {
		if n == 3 {
			cancel()
		}
		_, err := io.WriteString(out, "row\n")
		return err
	}

	_ = h.run(ctx, Options{Interval: time.Second})

	painted := h.out.String()
	for seq, name := range map[string]string{
		altScreenOn:  "alternate screen on",
		altScreenOff: "alternate screen off",
		cursorHide:   "cursor hide",
		cursorShow:   "cursor show",
	} {
		if n := strings.Count(painted, seq); n != 1 {
			t.Errorf("%s emitted %d times, want exactly 1 — even on cancel", name, n)
		}
	}
	if !strings.HasSuffix(painted, cursorShow+altScreenOff) {
		t.Error("the terminal is not handed back as the last thing written")
	}
}

func TestPlainModeEmitsNoEscapeSequences(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("+----+\n| id |\n+----+\n")

	if err := h.run(context.Background(), Options{Interval: time.Second, Count: 3, Plain: true, Diff: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	painted := h.out.String()
	if strings.ContainsRune(painted, 0x1b) {
		t.Fatalf("appended output carries an escape sequence:\n%q", painted)
	}
	if got, want := strings.Count(painted, "| id |"), 3; got != want {
		t.Errorf("wrote %d frames, want %d", got, want)
	}
}

func TestPlainModeEmitsTheCSVHeaderOnce(t *testing.T) {
	h := newHarness()
	h.render = staticFrames(
		"Name,Status\nweb-01,BUILD\n",
		"Name,Status\nweb-01,ACTIVE\n",
		"Name,Status\nweb-01,ACTIVE\n",
	)

	o := Options{Interval: time.Second, Count: 3, Plain: true, CSVHeaderOnce: true}
	if err := h.run(context.Background(), o); err != nil {
		t.Fatalf("Run: %v", err)
	}
	painted := h.out.String()
	if n := strings.Count(painted, "Name,Status"); n != 1 {
		t.Errorf("the CSV header appears %d times, want 1:\n%q", n, painted)
	}
	if n := strings.Count(painted, "web-01,"); n != 3 {
		t.Errorf("wrote %d rows, want 3:\n%q", n, painted)
	}
}

func TestPlainModeReportsToleratedErrorsOnStderr(t *testing.T) {
	h := newHarness()
	h.render = func(n int, out, _ io.Writer) error {
		if n == 2 {
			return httpErr(http.StatusServiceUnavailable)
		}
		_, err := io.WriteString(out, "row\n")
		return err
	}

	if err := h.run(context.Background(), Options{Interval: time.Second, Count: 3, Plain: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(h.errOut.String(), "503") {
		t.Errorf("the failure was not reported on stderr: %q", h.errOut.String())
	}
	if strings.Contains(h.out.String(), "503") {
		t.Error("the failure leaked into the frame stream, which has to stay parseable")
	}
}

func TestStatusLineSuppressed(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")

	if err := h.run(context.Background(), Options{Interval: time.Second, Count: 2, NoTitle: true, Title: "koc server list"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(h.out.String(), "koc server list") {
		t.Error("--watch-no-title still rendered the status line")
	}
}

func TestWarningsRenderUnderTheFrame(t *testing.T) {
	h := newHarness()
	h.render = func(_ int, out, warn io.Writer) error {
		io.WriteString(out, "the table\n")       //nolint:errcheck // a strings.Builder cannot fail
		io.WriteString(warn, "1 host skipped\n") //nolint:errcheck // idem
		return nil
	}

	if err := h.run(context.Background(), Options{Interval: time.Second, Count: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	painted := h.out.String()
	table, warn := strings.Index(painted, "the table"), strings.Index(painted, "1 host skipped")
	if warn < 0 {
		t.Fatalf("the captured warning never reached the frame:\n%q", painted)
	}
	if warn < table {
		t.Error("the warning rendered above the table instead of under it")
	}
}

// lastStatus returns the status line of the last frame painted.
func lastStatus(painted string) string {
	i := strings.LastIndex(painted, faint)
	if i < 0 {
		return ""
	}
	rest := painted[i+len(faint):]
	if j := strings.Index(rest, resetStyle); j >= 0 {
		return rest[:j]
	}
	return rest
}

// concurrency guard: the loop is single-threaded by design, and the tests above
// rely on it. This one fails under -race if a refresh ever runs off the loop's
// goroutine.
func TestRenderRunsOnOneGoroutine(t *testing.T) {
	var mu sync.Mutex
	inFlight := 0
	h := newHarness()
	h.render = func(_ int, out, _ io.Writer) error {
		mu.Lock()
		inFlight++
		if inFlight > 1 {
			mu.Unlock()
			t.Error("two refreshes overlapped")
			return nil
		}
		mu.Unlock()
		_, err := io.WriteString(out, "row\n")
		mu.Lock()
		inFlight--
		mu.Unlock()
		return err
	}
	if err := h.run(context.Background(), Options{Interval: time.Second, Count: 5}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// reauthErr is what gophercloud returns when a token expired, it asked Keystone
// for a new one, and that request failed. It deliberately does not Unwrap, so
// every rule that keys on a status code has to reach into it explicitly.
func reauthErr(reauth error) error {
	e := &gophercloud.ErrUnableToReauthenticate{}
	e.ErrOriginal = httpErr(http.StatusUnauthorized)
	e.ErrReauth = reauth
	return e
}

func TestRefusedReauthenticationIsFatal(t *testing.T) {
	h := newHarness()
	h.render = func(n int, out, _ io.Writer) error {
		if n >= 2 {
			// The credential is no longer accepted: password changed, account
			// disabled, application credential revoked.
			return reauthErr(httpErr(http.StatusUnauthorized))
		}
		_, err := io.WriteString(out, "row\n")
		return err
	}

	err := h.run(context.Background(), Options{Interval: time.Second, ErrorMode: ErrorsTolerate, Count: 20})
	if err == nil {
		t.Fatal("Run returned nil; retrying a refused credential once a second is how an account gets locked out")
	}
	if h.frames != 2 {
		t.Errorf("rendered %d frames, want 2 — it must stop at the refusal", h.frames)
	}
}

func TestReauthenticationThroughATransientKeystoneIsTolerated(t *testing.T) {
	h := newHarness()
	h.render = func(n int, out, _ io.Writer) error {
		if n == 2 {
			// Keystone itself was briefly unavailable. The credential may well
			// still be good, so this is the case to ride out rather than exit
			// on.
			return reauthErr(httpErr(http.StatusServiceUnavailable))
		}
		_, err := io.WriteString(out, "row\n")
		return err
	}

	if err := h.run(context.Background(), Options{Interval: time.Second, Count: 4}); err != nil {
		t.Fatalf("a Keystone blip ended the watch: %v", err)
	}
	if h.frames != 4 {
		t.Errorf("rendered %d frames, want 4", h.frames)
	}
}

func TestErrorAfterReauthenticationIsClassifiedByItsStatus(t *testing.T) {
	// Reauth succeeded and the retried request still failed. gophercloud wraps
	// that one, and it does unwrap, so the status inside decides.
	wrap := func(inner error) error {
		e := &gophercloud.ErrErrorAfterReauthentication{}
		e.ErrOriginal = inner
		return e
	}
	if !isAuthFailure(wrap(httpErr(http.StatusForbidden))) {
		t.Error("a 403 after re-authentication was not treated as an auth failure")
	}
	if !isTransient(wrap(httpErr(http.StatusServiceUnavailable))) {
		t.Error("a 503 after re-authentication was not treated as transient")
	}
}
