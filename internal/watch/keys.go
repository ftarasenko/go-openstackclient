package watch

import (
	"context"
	"os"
	"time"

	"golang.org/x/term"
)

// event is what woke the wait: the refresh deadline, a request to stop, or
// something that has already been dealt with (a repaint, an interval change).
type event int

const (
	evNone event = iota
	evTick
	evQuit
)

// Keys the loop understands. They are deliberately the ones viddy and less
// already use, so nothing here has to be learned: hence j/k alongside the
// arrows, and g/G alongside Home/End.
const (
	keyETX       = 0x03 // Ctrl-C
	keyEOT       = 0x04 // Ctrl-D
	keySpace     = ' '
	keyQuit      = 'q'
	keyRefresh   = 'r'
	keyPause     = 'p'
	keyFaster    = '-'
	keySlower    = '+'
	keySlowerAlt = '=' // the unshifted '+' on most layouts
	keyDiff      = 'd'
	keyHelp      = '?'

	// Scrolling. The single-byte spellings are here as well as the escape
	// sequences because they need no decoding and because anyone who reaches
	// for j/k will not thank us for arrows only.
	keyLineDown = 'j'
	keyLineUp   = 'k'
	keyTop      = 'g'
	keyBottom   = 'G'
	keyPageFwd  = 0x06 // Ctrl-F
	keyPageBack = 0x02 // Ctrl-B
)

// intervalStep is how far '+' moves the interval. Below a second the step is a
// quarter of one, so the keys stay useful either side of MinInterval.
func intervalStep(d time.Duration) time.Duration {
	if d < time.Second {
		return 250 * time.Millisecond
	}
	return time.Second
}

// stepDown is intervalStep for a decrease. At exactly one second the sub-second
// step is the one that applies, so '-' lands on 750ms instead of jumping
// straight to the floor.
func stepDown(d time.Duration) time.Duration {
	if d <= time.Second {
		return 250 * time.Millisecond
	}
	return time.Second
}

// keyClass says what may be done with a key that arrives while a refresh is
// still in flight.
//
// The loop runs the refresh on its own goroutine and watches the keyboard
// meanwhile, which is what stops a four-second fleet query from making the
// terminal look hung for four seconds. But that goroutine is inside the
// command, so what the loop may touch while it runs is not everything.
type keyClass int

const (
	// keyDeferred waits for the refresh to finish. Either it touches state the
	// refresh is using — 'd' flips the differ the output layer is calling into
	// right now — or acting early would lose it: 'r' asks for another refresh,
	// and the wait it would short-circuit has not started yet.
	keyDeferred keyClass = iota
	// keyImmediate changes only how the frame is presented, so it is safe to
	// act on and repaint while the refresh runs.
	keyImmediate
	// keyAbort stops the refresh rather than waiting it out. These are the keys
	// pressed *because* the screen looks stuck, so making them the slowest to
	// answer was exactly backwards.
	keyAbort
)

func classifyKey(k keyCode) keyClass {
	switch k {
	case keyETX, keyEOT, keyQuit, 'Q':
		return keyAbort
	case keyPause, 'P', keySlower, keySlowerAlt, keyFaster, keyHelp,
		keyLineDown, keyLineUp, keyTop, keyBottom, keyPageFwd, keyPageBack,
		keyDown, keyUp, keyPageDown, keyPageUp, keyHome, keyEnd, keyEscape:
		// Scrolling and the key map only change which lines are painted, never
		// what the refresh is doing.
		return keyImmediate
	default:
		return keyDeferred
	}
}

// key acts on one byte of input and reports what the loop should do next.
//
// In raw mode the terminal no longer turns Ctrl-C into SIGINT — it hands the
// byte over like any other — so the interrupt has to be re-created here by
// cancelling the context. It is *only* cancelled, never reported: cmd/koc's
// exit-130 path stays the single place an interrupt is announced, so a watched
// command and an interrupted `node deploy --wait` say the same thing.
func (r *runner) key(k keyCode, cancel context.CancelFunc) event {
	if r.scrollKey(k) {
		r.paint()
		return evNone
	}
	switch k {
	case keyETX, keyEOT:
		cancel()
		return evNone
	case keyQuit, 'Q':
		return evQuit
	case keySpace, keyRefresh, 'R':
		r.force = true
		return evTick
	case keyPause, 'P':
		return r.togglePause()
	case keySlower, keySlowerAlt:
		r.setInterval(r.interval + intervalStep(r.interval))
	case keyFaster:
		r.setInterval(r.interval - stepDown(r.interval))
	case keyDiff:
		r.differ.SetEnabled(!r.differ.Enabled())
		r.force = true
		return evTick
	case keyHelp:
		r.showHelp = !r.showHelp
		// The key map is short and always read from the top; the frame keeps
		// the place it was left at.
		r.helpView.reset()
		r.paint()
	case keyEscape:
		// Escape closes the key map and does nothing else — it is the other
		// thing everyone tries.
		if r.showHelp {
			r.showHelp = false
			r.paint()
		}
	}
	return evNone
}

// scrollKey moves the window if k is a scrolling key, and reports whether it
// was one. The clamping happens at the next paint, which is the only place the
// terminal's height is known.
func (r *runner) scrollKey(k keyCode) bool {
	v := r.scrollView()
	switch k {
	case keyLineDown, keyDown:
		v.scroll(1)
	case keyLineUp, keyUp:
		v.scroll(-1)
	case keyPageFwd, keyPageDown:
		v.scroll(v.page())
	case keyPageBack, keyPageUp:
		v.scroll(-v.page())
	case keyTop, keyHome:
		v.top()
	case keyBottom, keyEnd:
		v.bottom()
	default:
		return false
	}
	return true
}

// togglePause flips the pause state. Resuming refreshes at once rather than
// waiting out the remaining interval, which is what the operator who just
// pressed the key is asking for.
func (r *runner) togglePause() event {
	r.paused = !r.paused
	if r.paused {
		r.paint()
		return evNone
	}
	r.force = true
	return evTick
}

// setInterval applies a keyed interval change, clamped to the same bounds the
// flag is validated against. The backoff is reset with it: an explicit request
// outranks an inference the loop made about latency.
func (r *runner) setInterval(d time.Duration) {
	switch {
	case d < MinInterval:
		d = MinInterval
	case d > MaxInterval:
		d = MaxInterval
	}
	r.o.Interval = d
	r.interval = d
	r.paint()
}

// rawStdin puts the terminal's input side into raw mode and returns the
// function that puts it back. Raw mode is what makes single-key control
// possible: without it the tty buffers a whole line and nothing reaches the
// loop until Enter.
//
// It is entered only when standard input is a terminal, independently of
// standard output, so a watched command whose frames are piped elsewhere still
// runs — it just has no keys.
func rawStdin() (func(), error) {
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(fd, state) }, nil
}
