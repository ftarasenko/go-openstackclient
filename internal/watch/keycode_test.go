package watch

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []keyCode
		rest string
	}{
		{"ordinary keys", "qp?", []keyCode{'q', 'p', '?'}, ""},
		{"control bytes", "\x03\x06\x02", []keyCode{keyETX, keyPageFwd, keyPageBack}, ""},
		{"arrows", "\x1b[A\x1b[B\x1b[C\x1b[D", []keyCode{keyUp, keyDown, keyRight, keyLeft}, ""},
		{"page keys", "\x1b[5~\x1b[6~", []keyCode{keyPageUp, keyPageDown}, ""},
		// Terminals disagree on Home and End; both spellings have to work.
		{"home and end, xterm", "\x1b[H\x1b[F", []keyCode{keyHome, keyEnd}, ""},
		{"home and end, tilde", "\x1b[1~\x1b[4~", []keyCode{keyHome, keyEnd}, ""},
		{"application keypad", "\x1bOA\x1bOB", []keyCode{keyUp, keyDown}, ""},
		{"modified page key", "\x1b[6;2~", []keyCode{keyPageDown}, ""},
		{"a key among sequences", "\x1b[Bq\x1b[A", []keyCode{keyDown, 'q', keyUp}, ""},
		// Alt-x and "Escape, then x" are the same bytes; with no timer to tell
		// them apart, the reading that costs nothing is the one that wins.
		{"escape then a key", "\x1bx", []keyCode{keyEscape, 'x'}, ""},
		{"unknown sequence", "\x1b[Z", []keyCode{keyUnknown}, ""},
		// A sequence split across two reads must not decay into its bytes: that
		// is how Down becomes "escape, bracket, capital B" and closes the help
		// panel on the way past.
		{"incomplete csi", "\x1b[", nil, "\x1b["},
		{"incomplete tilde", "\x1b[6", nil, "\x1b[6"},
		{"lone escape", "\x1b", nil, "\x1b"},
		{"key then incomplete", "j\x1b[", []keyCode{'j'}, "\x1b["},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys, rest := decodeKeys([]byte(tc.in))
			if !reflect.DeepEqual(keys, tc.want) {
				t.Errorf("keys %v, want %v", keys, tc.want)
			}
			if string(rest) != tc.rest {
				t.Errorf("rest %q, want %q", rest, tc.rest)
			}
		})
	}
}

func TestDecodeKeysAcrossReads(t *testing.T) {
	// Feed a sequence one byte at a time, which is what a slow link or an
	// unlucky scheduler produces.
	var got []keyCode
	var held []byte
	for _, b := range []byte("\x1b[6~j") {
		keys, rest := decodeKeys(append(held, b))
		got = append(got, keys...)
		held = append(held[:0], rest...)
	}
	want := []keyCode{keyPageDown, 'j'}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keys %v, want %v", got, want)
	}
	if len(held) != 0 {
		t.Errorf("%q was left over", held)
	}
}

func TestReadKeysDecodes(t *testing.T) {
	ch := readKeys(strings.NewReader("j\x1b[Bk\x1b[6~q"))
	var got []keyCode
	for k := range ch {
		got = append(got, k)
	}
	want := []keyCode{'j', keyDown, 'k', keyPageDown, 'q'}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keys %v, want %v", got, want)
	}
}

func TestReadKeysReleasesAHeldEscapeAtTheEnd(t *testing.T) {
	// ESC is held on arrival because it may be the start of an arrow key. When
	// the input ends there, it was the Escape key.
	var got []keyCode
	for k := range readKeys(strings.NewReader("j\x1b")) {
		got = append(got, k)
	}
	want := []keyCode{'j', keyEscape}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keys %v, want %v", got, want)
	}
}

func TestReadKeysDoesNotSplitASequenceAcrossReads(t *testing.T) {
	// The failure this guards: a Down arrow delivered in two reads decaying
	// into ESC, '[', 'B' — three presses nobody made, one of which closes the
	// key map on its way past.
	pr, pw := io.Pipe()
	ch := readKeys(pr)
	go func() {
		_, _ = pw.Write([]byte("\x1b["))
		_, _ = pw.Write([]byte("B"))
		_ = pw.Close()
	}()
	var got []keyCode
	for k := range ch {
		got = append(got, k)
	}
	if !reflect.DeepEqual(got, []keyCode{keyDown}) {
		t.Errorf("keys %v, want one keyDown", got)
	}
}

func TestDecodeKeysHandlesMultibyteRunes(t *testing.T) {
	// Nothing binds a non-ASCII key, but it must be consumed as one rune: a
	// two-byte character forwarded as two keys is two unrelated presses.
	keys, rest := decodeKeys([]byte("é"))
	if len(keys) != 1 || keys[0] != keyUnknown {
		t.Errorf("keys %v, want one keyUnknown", keys)
	}
	if len(rest) != 0 {
		t.Errorf("rest %q, want none", rest)
	}
	// And a rune split across reads waits for its tail.
	if keys, rest := decodeKeys([]byte("é")[:1]); len(keys) != 0 || len(rest) != 1 {
		t.Errorf("a partial rune decoded as %v / %q", keys, rest)
	}
}
