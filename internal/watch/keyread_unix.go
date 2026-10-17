//go:build !windows

package watch

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// stdinKeys reads keys from f until the returned stop function is called, and
// never blocks in a read to do it: it polls f together with a stop pipe and
// reads only once f has input. A reader parked in Read would outlive the watch
// and take the next keystrokes typed — harmless when the watch ends the
// process, but inside `koc shell` those keystrokes are the next line.
//
// stop returns once the reader has exited, so nothing reads f after it.
func stdinKeys(f *os.File) (<-chan keyCode, func()) {
	stopR, stopW, err := os.Pipe()
	if err != nil {
		return readKeys(f), func() { /* no stop pipe: the old blocking reader */ }
	}
	ch := readKeys(&pollReader{f: f, stop: stopR})
	return ch, func() {
		_ = stopW.Close()
		// Drain until the reader has returned and closed the channel.
		for range ch {
			// discard: only the close matters
		}
		_ = stopR.Close()
	}
}

// pollReader reads f only when poll(2) says it has input, and reports io.EOF
// as soon as its stop pipe is closed.
type pollReader struct {
	f    *os.File
	stop *os.File
}

func (p *pollReader) Read(b []byte) (int, error) {
	fds := []unix.PollFd{
		{Fd: int32(p.f.Fd()), Events: unix.POLLIN},    //nolint:gosec // G115: a file descriptor fits in an int32
		{Fd: int32(p.stop.Fd()), Events: unix.POLLIN}, //nolint:gosec // G115: a file descriptor fits in an int32
	}
	for {
		if _, err := unix.Poll(fds, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return 0, err
		}
		if fds[1].Revents != 0 {
			return 0, io.EOF
		}
		if fds[0].Revents != 0 {
			return p.f.Read(b)
		}
	}
}
