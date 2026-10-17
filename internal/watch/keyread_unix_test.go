//go:build !windows

package watch

import (
	"os"
	"testing"
	"time"
)

// TestStdinKeys_StopLeavesLaterInput: once the watch is over, the keyboard
// reader must be gone without having read what is typed next — inside koc
// shell that is the next command line.
func TestStdinKeys_StopLeavesLaterInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()

	keys, stop := stdinKeys(r)
	if _, err := w.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	select {
	case k := <-keys:
		if k != keyCode('q') {
			t.Errorf("key %v, want q", k)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no key read")
	}

	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return: the reader is still parked")
	}

	if _, err := w.Write([]byte("server list\r")); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := r.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case line := <-got:
		if line != "server list\r" {
			t.Errorf("after stop, the next reader got %q; want the whole line", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("after stop, the next reader got nothing: the watch's reader took the line")
	}
}
