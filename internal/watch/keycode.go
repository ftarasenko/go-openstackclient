package watch

import (
	"io"
	"unicode/utf8"
)

// A terminal in raw mode hands over an arrow key as three bytes — ESC, '[', 'A'
// — and PgDn as four. Reading one byte at a time, as the loop used to, turns
// Down into "escape, then bracket, then a capital A", which is not a keypress
// anyone made: the 'A' would be swallowed as an unknown key and the ESC could
// close the help panel on its way past.
//
// So the reader decodes instead of forwarding. Ordinary keys keep their byte
// value, and the sequences become the synthetic codes below, well clear of the
// byte range.

// keyCode is one decoded keypress: an ordinary key's byte value, or one of the
// synthetic codes for a key that arrives as an escape sequence.
type keyCode int

// The synthetic codes. They start past 0xFF so an ordinary byte can never
// collide with one.
const (
	keyUp keyCode = 0x100 + iota
	keyDown
	keyRight
	keyLeft
	keyPageUp
	keyPageDown
	keyHome
	keyEnd
	// keyEscape is a lone ESC — the operator pressed Escape, rather than a
	// terminal beginning a sequence.
	keyEscape
	// keyUnknown is a sequence that decoded cleanly but means nothing here. It
	// is emitted rather than dropped so the loop can ignore it deliberately, and
	// so its bytes can never be mistaken for the keys they are made of.
	keyUnknown
)

// csiKeys maps the final byte of a CSI sequence with no parameters — ESC [ X.
var csiKeys = map[byte]keyCode{
	'A': keyUp,
	'B': keyDown,
	'C': keyRight,
	'D': keyLeft,
	'H': keyHome,
	'F': keyEnd,
}

// tildeKeys maps the numeric parameter of a CSI sequence ending in '~', which is
// how the xterm family spells the navigation block. Both spellings of Home and
// End are accepted because terminals disagree: xterm sends ESC [ H, the linux
// console and PuTTY send ESC [ 1 ~ and ESC [ 4 ~.
var tildeKeys = map[int]keyCode{
	1: keyHome,
	4: keyEnd,
	5: keyPageUp,
	6: keyPageDown,
	7: keyHome,
	8: keyEnd,
}

// decodeKeys turns a buffer of raw terminal input into keypresses. It returns
// the keys it decoded and the bytes of a sequence that is still incomplete, for
// the caller to prepend to the next read.
//
// Splitting matters: a terminal usually delivers a sequence in one read, but
// nothing guarantees it, and a decoder that gave up on a partial sequence would
// emit its bytes as separate keypresses — the failure this exists to avoid.
func decodeKeys(buf []byte) (keys []keyCode, rest []byte) {
	for i := 0; i < len(buf); {
		b := buf[i]
		if b != 0x1b {
			// Multi-byte UTF-8 is not bound to anything, but it must be
			// consumed as one rune rather than as its bytes.
			if b < utf8.RuneSelf {
				keys = append(keys, keyCode(b))
				i++
				continue
			}
			_, size := utf8.DecodeRune(buf[i:])
			if size == 1 && buf[i] >= utf8.RuneSelf {
				return keys, buf[i:] // an incomplete rune; wait for the rest
			}
			keys = append(keys, keyUnknown)
			i += size
			continue
		}
		key, n, ok := decodeEscape(buf[i:])
		if !ok {
			return keys, buf[i:] // incomplete; the caller reads more
		}
		keys = append(keys, key)
		i += n
	}
	return keys, nil
}

// decodeEscape reads one ESC-introduced sequence from the front of buf. It
// reports the key, how many bytes it consumed, and whether the sequence was
// complete.
func decodeEscape(buf []byte) (keyCode, int, bool) {
	if len(buf) == 1 {
		// Just an ESC so far: either the Escape key, or a sequence whose rest
		// has not arrived. The caller holds it for the next read, and flushes
		// it as Escape if the input ends there.
		return 0, 0, false
	}
	switch buf[1] {
	case '[':
		return decodeCSI(buf)
	case 'O':
		// SS3, which is what an application-mode keypad sends: ESC O A for Up.
		if len(buf) < 3 {
			return 0, 0, false
		}
		if k, ok := csiKeys[buf[2]]; ok {
			return k, 3, true
		}
		return keyUnknown, 3, true
	default:
		// ESC followed by an ordinary key is ambiguous: a terminal spells Alt-j
		// that way, and so does a human pressing Escape and then j. Without a
		// timer there is nothing to tell them apart, so it is read as the two
		// presses — only the ESC is consumed here and the rest decodes on its
		// own. Nothing binds Alt, so the reading that loses is the one that
		// costs nothing.
		return keyEscape, 1, true
	}
}

// decodeCSI reads a CSI sequence: ESC [ <parameters> <final byte>.
func decodeCSI(buf []byte) (keyCode, int, bool) {
	var param int
	var haveParam, sawSemi bool
	for i := 2; i < len(buf); i++ {
		c := buf[i]
		if c >= '0' && c <= '9' {
			// Only the *first* parameter identifies the key; what follows a
			// semicolon is the modifier (Shift-PgDn is ESC [ 6 ; 2 ~), and
			// folding its digits into the same number turns 6 into 62.
			if !sawSemi {
				param = param*10 + int(c-'0')
				haveParam = true
			}
			continue
		}
		if c == ';' {
			sawSemi = true
			continue
		}
		switch {
		case c == '~':
			if k, ok := tildeKeys[param]; ok && haveParam {
				return k, i + 1, true
			}
			return keyUnknown, i + 1, true
		case c >= '@' && c <= '~':
			if k, ok := csiKeys[c]; ok {
				return k, i + 1, true
			}
			return keyUnknown, i + 1, true
		}
	}
	return 0, 0, false // still reading
}

// readKeys pumps decoded keypresses from src into a channel the loop can select
// on.
//
// The read runs on its own goroutine because it blocks: doing it inline would
// make a refresh wait on a keypress, which is precisely backwards. The channel
// is buffered so a burst of held keys is not lost between ticks, and the
// goroutine ends when the reader returns an error — which, for a terminal, is
// when the process is on its way out. A read still parked in the kernel at that
// point is not worth chasing: the terminal's mode has already been restored by
// the deferred restore in Run, so the parked read holds nothing.
//
// A lone ESC is held back, because at that instant it is indistinguishable from
// the start of an arrow key. It is released as Escape once the input ends, or
// decoded as part of whatever sequence the next bytes complete.
func readKeys(src io.Reader) <-chan keyCode {
	ch := make(chan keyCode, 32)
	go func() {
		defer close(ch)
		var held []byte
		buf := make([]byte, 64)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				keys, rest := decodeKeys(append(held, buf[:n]...))
				held = append(held[:0], rest...)
				for _, k := range keys {
					ch <- k
				}
			}
			if err != nil {
				// Nothing more is coming, so a held ESC was the Escape key.
				if len(held) == 1 && held[0] == 0x1b {
					ch <- keyEscape
				}
				return
			}
		}
	}()
	return ch
}
