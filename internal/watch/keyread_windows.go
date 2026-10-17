//go:build windows

package watch

import "os"

// stdinKeys is the blocking reader on Windows, which has no poll(2) for a
// console handle. The reader may outlive the watch; `koc shell` is not shipped
// for Windows consoles in the embedded build, and a one-shot watch ends the
// process anyway.
func stdinKeys(f *os.File) (<-chan keyCode, func()) {
	return readKeys(f), func() { /* nothing to stop */ }
}
