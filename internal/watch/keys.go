package watch

import (
	"context"
	"io"
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

// Key bytes the loop understands. They are deliberately the ones viddy and
// less already use, so nothing here has to be learned.
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

// key acts on one byte of input and reports what the loop should do next.
//
// In raw mode the terminal no longer turns Ctrl-C into SIGINT — it hands the
// byte over like any other — so the interrupt has to be re-created here by
// cancelling the context. It is *only* cancelled, never reported: cmd/koc's
// exit-130 path stays the single place an interrupt is announced, so a watched
// command and an interrupted `node deploy --wait` say the same thing.
func (r *runner) key(b byte, cancel context.CancelFunc) event {
	switch b {
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
		r.paint()
	}
	return evNone
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

// readKeys pumps single bytes from src into a channel the loop can select on.
//
// The read runs on its own goroutine because it blocks: doing it inline would
// make a refresh wait on a keypress, which is precisely backwards. The channel
// is buffered so a burst of held keys is not lost between ticks, and the
// goroutine ends when the reader returns an error — which, for a terminal, is
// when the process is on its way out. A read still parked in the kernel at that
// point is not worth chasing: the terminal's mode has already been restored by
// the deferred restore in Run, so the parked read holds nothing.
func readKeys(src io.Reader) <-chan byte {
	ch := make(chan byte, 16)
	go func() {
		defer close(ch)
		buf := make([]byte, 1)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				ch <- buf[0]
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}
