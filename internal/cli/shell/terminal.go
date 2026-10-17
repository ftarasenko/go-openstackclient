package shell

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/ftarasenko/go-openstackclient/internal/kochome"
)

// console is where the shell reads lines from: an x/term line editor on a
// terminal, or plain lines from a pipe.
type console interface {
	// readLine shows prompt and returns the next line; io.EOF ends the shell.
	readLine(prompt string) (string, error)
	// writeAbovePrompt prints text while a line is being edited (completion
	// candidates), keeping the prompt and the half-typed line intact.
	writeAbovePrompt(s string)
	// width is the terminal's width, re-read before every line.
	width() int
}

// keyInterrupt stands in for Ctrl-C at the prompt. x/term's ReadLine treats
// Ctrl-C exactly like Ctrl-D on an empty line — it returns io.EOF — which would
// end the shell on the key every operator presses to abandon a line. The input
// filter rewrites the byte to this private-use rune, which x/term hands to the
// AutoCompleteCallback like any other key, and the callback clears the line.
const keyInterrupt = ''

const ctrlC = 0x03

// interruptFilter rewrites Ctrl-C in the terminal's input to keyInterrupt.
type interruptFilter struct {
	r       io.Reader
	pending []byte
}

func (f *interruptFilter) Read(p []byte) (int, error) {
	if len(f.pending) == 0 {
		buf := make([]byte, len(p))
		n, err := f.r.Read(buf)
		if n == 0 {
			return 0, err
		}
		for _, b := range buf[:n] {
			if b == ctrlC {
				f.pending = utf8.AppendRune(f.pending, keyInterrupt)
				continue
			}
			f.pending = append(f.pending, b)
		}
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

// termConsole is the interactive console: x/term's line editor, with the
// terminal in raw mode only while a line is being read, so a command runs on
// a normal terminal — `--watch` and the gauges then take raw mode themselves.
type termConsole struct {
	t  *term.Terminal
	fd int // the terminal's descriptor; -1 when there is none (tests)
}

func newTermConsole(rw io.ReadWriter, fd int) *termConsole {
	in := &interruptFilter{r: rw}
	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{in, rw}, "")
	return &termConsole{t: t, fd: fd}
}

func (c *termConsole) readLine(prompt string) (string, error) {
	if c.fd >= 0 {
		// The terminal may have been resized while the last command ran, and
		// x/term wraps the line it edits by the width it was last told.
		// A PTY nobody sized reports 0x0, which x/term would wrap at every
		// character; keep its default then.
		if w, h, err := term.GetSize(c.fd); err == nil && w > 0 && h > 0 {
			_ = c.t.SetSize(w, h)
		}
		old, err := term.MakeRaw(c.fd)
		if err != nil {
			return "", err
		}
		defer func() { _ = term.Restore(c.fd, old) }()
	}
	c.t.SetPrompt(prompt)
	line, err := c.t.ReadLine()
	if errors.Is(err, term.ErrPasteIndicator) {
		// A pasted line is still a line; bracketed paste is not enabled, but
		// be safe if a terminal sends the markers anyway.
		err = nil
	}
	return line, err
}

func (c *termConsole) writeAbovePrompt(s string) { _, _ = c.t.Write([]byte(s)) }

func (c *termConsole) width() int {
	if c.fd >= 0 {
		if w, _, err := term.GetSize(c.fd); err == nil && w > 0 {
			return w
		}
	}
	return defaultWidth
}

// defaultWidth is assumed when the width cannot be measured.
const defaultWidth = 80

// plainConsole reads lines from a pipe: no prompt, no editing — `echo 'server
// list' | koc shell` runs like a script.
type plainConsole struct{ sc *bufio.Scanner }

func newPlainConsole(r io.Reader) *plainConsole {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return &plainConsole{sc: sc}
}

func (c *plainConsole) readLine(string) (string, error) {
	if c.sc.Scan() {
		return c.sc.Text(), nil
	}
	if err := c.sc.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}

func (c *plainConsole) writeAbovePrompt(string) { /* no prompt line to redraw around */ }
func (c *plainConsole) width() int              { return defaultWidth }

// History is kept in memory only, unless KOC_HISTORY_FILE names a file: then
// earlier sessions' lines are loaded from it and every new line is appended.
// Secret values are redacted before a line reaches the file, so a password
// typed on a line is recalled in this session but never written to disk.

// historyLimit bounds the in-memory history.
const historyLimit = 1000

// EnvHistoryFile names the file history persists to.
const EnvHistoryFile = "KOC_HISTORY_FILE"

// history implements term.History.
type history struct {
	mu      sync.Mutex
	entries []string // oldest first
	file    *os.File
	redact  func(string) string
}

func (h *history) Add(entry string) {
	if strings.TrimSpace(entry) == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.entries); n > 0 && h.entries[n-1] == entry {
		return
	}
	h.entries = append(h.entries, entry)
	if len(h.entries) > historyLimit {
		h.entries = h.entries[len(h.entries)-historyLimit:]
	}
	if h.file != nil {
		line := entry
		if h.redact != nil {
			line = h.redact(line)
		}
		if _, err := h.file.WriteString(strings.ReplaceAll(line, "\n", " ") + "\n"); err != nil {
			_ = h.file.Close()
			h.file = nil
		}
	}
}

func (h *history) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

// At returns the idx-th most recent entry, as term.History requires.
func (h *history) At(idx int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.entries[len(h.entries)-1-idx]
}

func (h *history) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		_ = h.file.Close()
		h.file = nil
	}
}

// openHistory loads and opens KOC_HISTORY_FILE when it is set. A relative path
// is resolved inside KOC_HOME.
func openHistory(redact func(string) string) (*history, error) {
	h := &history{redact: redact}
	path := os.Getenv(EnvHistoryFile)
	if path == "" {
		return h, nil
	}
	path = kochome.Resolve(path)
	if raw, err := kochome.ReadFile(path); err == nil {
		for l := range strings.SplitSeq(string(raw), "\n") {
			if strings.TrimSpace(l) != "" {
				h.entries = append(h.entries, l)
			}
		}
		if len(h.entries) > historyLimit {
			h.entries = h.entries[len(h.entries)-historyLimit:]
		}
	}
	f, err := kochome.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return h, err
	}
	h.file = f
	return h, nil
}
