package watch

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// The control sequences the painter uses. They are spelled out rather than
// pulled from a terminfo library so the binary stays dependency-free, and they
// are the same handful every full-screen tool emits.
const (
	// altScreenOn switches to the alternate screen buffer, so the operator's
	// scrollback survives the watch and quitting leaves the terminal exactly as
	// it was found. altScreenOff switches back.
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
	cursorHide   = "\x1b[?25l"
	cursorShow   = "\x1b[?25h"
	// homeErase moves the cursor to the top-left and erases from there to the
	// end of the screen. Repainting this way rather than clearing first is what
	// keeps the frame from flickering: the old pixels are overwritten by the new
	// ones in the same write.
	homeErase = "\x1b[H\x1b[0J"
	// faint renders the status line without competing with the table.
	faint      = "\x1b[2m"
	resetStyle = "\x1b[0m"
)

// screen owns everything about putting frames in front of the operator: which
// of the two painting modes is in force, the last frame that rendered
// successfully, and the terminal state that has to be given back on the way
// out.
type screen struct {
	out   io.Writer
	plain bool

	size   func() (cols, rows int)
	keys   bool
	rawFn  func() (func(), error)
	csvOne bool

	body    []byte
	warn    []byte
	latency time.Duration
	rows    int
	pending bool // plain mode: a frame is accepted but not yet written

	csvHeader []byte
	wroteAny  bool

	stale error
}

func newScreen(out io.Writer, o Options) *screen {
	s := &screen{
		out:   out,
		plain: o.Plain,

		size:   o.size,
		keys:   o.Keys,
		rawFn:  o.rawMode,
		csvOne: o.CSVHeaderOnce,
	}
	if s.size == nil {
		s.size = func() (int, int) { return writerSize(out) }
	}
	if s.rawFn == nil {
		s.rawFn = rawStdin
	}
	return s
}

// enter claims the terminal and returns the function that gives it back. The
// returned function is safe to call once and must be deferred: it is the only
// thing standing between a mid-frame failure and a terminal left in the
// alternate buffer with its cursor hidden and its line discipline off.
//
// Order matters on the way out. The input mode is restored *first* and the
// alternate screen left *second*, so that if anything goes wrong in between the
// operator is left with a working shell rather than a cooked-looking screen
// they cannot type into.
func (s *screen) enter() (func(), error) {
	if s.plain {
		return func() {}, nil
	}

	var restoreRaw func()
	if s.keys {
		fn, err := s.rawFn()
		if err != nil {
			// Keys are a convenience; losing them is not a reason to refuse to
			// watch. The loop simply runs without them.
			s.keys = false
		} else {
			restoreRaw = fn
		}
	}

	io.WriteString(s.out, altScreenOn+cursorHide) //nolint:errcheck,gosec // a failed write here resurfaces on the first paint
	return func() {
		if restoreRaw != nil {
			restoreRaw()
		}
		io.WriteString(s.out, cursorShow+altScreenOff) //nolint:errcheck,gosec // on the way out of a dying terminal there is nothing useful left to do
	}, nil
}

// accept records a frame that rendered successfully.
func (s *screen) accept(body, warn []byte, latency time.Duration, rows int) {
	s.body = bytes.Clone(body)
	s.warn = bytes.Clone(warn)
	s.latency = latency
	s.rows = rows
	s.stale = nil
	s.pending = true
}

// reject records a failed refresh. The frame already on screen is deliberately
// left alone: a transient 503 should not blank an operator's view of a fleet.
func (s *screen) reject(err error) { s.stale = err }

// paint puts the current state on the display. overlay, when non-nil, is shown
// in place of the frame — today only the '?' key map, which appending frames
// has no use for, so it is ignored in plain mode.
func (s *screen) paint(status string, overlay []string) {
	if s.plain {
		s.appendFrame()
		return
	}
	s.repaint(status, overlay)
}

// appendFrame writes one whole frame to the stream, with no escape sequences at
// all, so the output stays composable: `--watch -f json | jq .` sees a snapshot
// per tick and nothing else.
func (s *screen) appendFrame() {
	if !s.pending {
		return
	}
	s.pending = false
	body := s.body
	if s.csvOne {
		body = s.stripRepeatHeader(body)
	}
	s.out.Write(body)   //nolint:errcheck,gosec // a closed stdout surfaces as the next refresh's write error
	s.out.Write(s.warn) //nolint:errcheck,gosec // idem
	s.wroteAny = true
}

// stripRepeatHeader drops the CSV header from every frame after the first. A
// header wedged between each pair of snapshots is not a CSV any tool can read
// back, and re-emitting it is the one thing appending frames would otherwise
// get wrong.
func (s *screen) stripRepeatHeader(body []byte) []byte {
	line, rest, found := bytes.Cut(body, []byte("\n"))
	if !found {
		return body
	}
	if !s.wroteAny {
		s.csvHeader = bytes.Clone(line)
		return body
	}
	if bytes.Equal(line, s.csvHeader) {
		return rest
	}
	return body
}

// repaint draws one full frame in a single write: cursor home, erase to the end
// of the screen, then the frame and the status line. One write means the
// terminal never shows a half-drawn table.
func (s *screen) repaint(status string, overlay []string) {
	cols, rows := s.size()
	var b strings.Builder
	b.WriteString(homeErase)

	lines := overlay
	if lines == nil {
		lines = frameLines(s.body, s.warn)
	}
	reserve := 0
	if status != "" {
		reserve = 2
	}
	lines = clipLines(lines, rows, reserve)
	for _, ln := range lines {
		b.WriteString(ln)
		b.WriteString("\r\n")
	}
	if status != "" {
		b.WriteString("\r\n")
		b.WriteString(faint)
		b.WriteString(truncate(status, cols))
		b.WriteString(resetStyle)
	}
	io.WriteString(s.out, b.String()) //nolint:errcheck,gosec // a failed paint resurfaces on the next one; blanking the screen to report it would be worse
	// The frame is only consumed when it was the frame that was drawn: with the
	// key map up, the refresh underneath has still not been seen.
	s.pending = overlay != nil && s.pending
}

// frameLines splits the rendered body and any captured warnings into physical
// lines. Warnings are captured rather than left on stderr because a command
// that warns mid-list — the partial-failure path in `server migration list`,
// say — would otherwise write straight onto the alternate screen, over the
// frame being drawn.
func frameLines(body, warn []byte) []string {
	lines := splitLines(body)
	if w := splitLines(warn); len(w) > 0 {
		lines = append(lines, "")
		lines = append(lines, w...)
	}
	return lines
}

func splitLines(b []byte) []string {
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// clipLines trims a frame to the height the terminal actually has, saying how
// much was dropped. Letting it overflow instead would scroll the alternate
// screen and push the status line out of sight.
func clipLines(lines []string, rows, reserve int) []string {
	if rows <= 0 || len(lines) <= rows-reserve {
		return lines
	}
	keep := rows - reserve - 1
	if keep < 0 {
		keep = 0
	}
	dropped := len(lines) - keep
	out := make([]string, 0, keep+1)
	out = append(out, lines[:keep]...)
	return append(out, "… +"+strconv.Itoa(dropped)+" more line(s); the terminal is "+strconv.Itoa(rows)+" rows")
}

// truncate cuts s to at most cols runes. The status line is the one string koc
// itself composes that can outgrow the display.
func truncate(s string, cols int) string {
	if cols <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= cols {
		return s
	}
	if cols <= 1 {
		return string(r[:cols])
	}
	return string(r[:cols-1]) + "…"
}

// writerSize reports the size of the terminal behind w, or 0, 0 when w is not
// one. It is measured per frame rather than once at startup so a resize is
// picked up even where SIGWINCH does not exist (Windows).
func writerSize(w io.Writer) (cols, rows int) {
	f, ok := w.(*os.File)
	if !ok {
		return 0, 0
	}
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return 0, 0
	}
	c, r, err := term.GetSize(fd)
	if err != nil {
		return 0, 0
	}
	return c, r
}

// Size reports the size of the terminal behind w, or 0, 0 when w is not one.
// The cli layer calls it once per frame and hands the width to
// output.Options.SetDisplayWidth, so tables are fitted to the display even
// though they are rendered into a buffer.
func Size(w io.Writer) (cols, rows int) { return writerSize(w) }

// IsTerminal reports whether w is a terminal koc can repaint on. The cli layer
// uses it to decide between repaint and append mode.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// StdinIsTerminal reports whether the interactive keys can be offered. It is
// deliberately independent of the output side: `koc … --watch | tee` keeps
// working, it just has no keys.
func StdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
