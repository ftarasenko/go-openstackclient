//go:build !windows

package watch

import (
	"os"
	"os/signal"
	"syscall"
)

// winchSignals delivers a token whenever the terminal is resized, so the frame
// is redrawn at the new width instead of staying wrong until the next tick.
//
// Windows has no SIGWINCH, hence the build tag and the no-op sibling. Nothing
// is lost there: the display size is measured once per frame rather than once
// at startup precisely so a resize is picked up either way — the signal only
// makes it immediate.
func winchSignals() (<-chan struct{}, func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case <-done:
				return
			case <-sig:
				select {
				case out <- struct{}{}:
				default: // a repaint is already pending; one is enough
				}
			}
		}
	}()
	return out, func() {
		signal.Stop(sig)
		close(done)
	}
}
