package watch

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// statusSep joins the status line's segments. A middle dot reads as a separator
// at a glance without looking like table punctuation.
const statusSep = " · "

// statusClock is the wall-clock format on the status line: time only, because
// the operator watching a fleet knows what day it is and the line has to fit.
const statusClock = "15:04:05"

// status composes the one line of chrome a watched command carries:
//
//	koc server list --all --host node-14 · every 1s · 12:03:45 · 42 rows · 218ms · ok
//
// and, when a refresh fails, says so without taking the frame away:
//
//	koc server list --all --host node-14 · every 1s · 12:03:49 · stale 4s · 2 errors · last: 503 Service Unavailable
//
// It returns "" when the status line is suppressed (--watch-no-title, watch(1)'s
// -t) or when frames are appended rather than repainted, where a line of chrome
// per snapshot would corrupt the stream the operator is piping somewhere.
func (r *runner) status() string {
	if r.o.NoTitle || r.o.Plain {
		return ""
	}
	segs := []string{r.o.Title, r.intervalSegment(), r.o.now().Format(statusClock)}
	if r.screen.rows > 0 {
		segs = append(segs, plural(r.screen.rows, "row"))
	}
	// The latency belongs to the frame on screen, so while that frame is stale
	// it describes a refresh that is no longer the news. Dropping it here also
	// buys the width the error message needs, which is what an operator is
	// actually reading at that moment.
	if r.frames > 0 && r.lastErr == nil {
		segs = append(segs, compactDuration(r.screen.latency))
	}
	segs = append(segs, r.stateSegments()...)
	if r.skipped > 0 {
		segs = append(segs, plural(r.skipped, "skipped refresh"))
	}
	// The keys hint is the first thing to go: it is the same every frame, and
	// the line is truncated to the display.
	if r.o.Keys && r.lastErr == nil {
		segs = append(segs, "keys: q p r + - d")
	}
	return strings.Join(segs, statusSep)
}

// intervalSegment reports the interval actually in force, and says so plainly
// when the latency backoff has moved it off what was asked for — a loop that
// quietly slows down is worse than one that never sped up.
func (r *runner) intervalSegment() string {
	s := "every " + compactDuration(r.interval)
	if r.interval != r.o.Interval {
		s += " (backoff from " + compactDuration(r.o.Interval) + ")"
	}
	return s
}

// stateSegments render the loop's health: ok, paused, or stale with the error
// that made it so.
func (r *runner) stateSegments() []string {
	switch {
	case r.lastErr != nil:
		var segs []string
		if !r.lastOK.IsZero() {
			segs = append(segs, "stale "+compactDuration(r.o.now().Sub(r.lastOK)))
		}
		segs = append(segs, plural(r.errs, "error"), "last: "+errSummary(r.lastErr))
		return segs
	case r.paused:
		return []string{"paused"}
	default:
		return []string{"ok"}
	}
}

// errSummary reduces an error to something that fits on one line. API errors
// arrive with the whole response body attached (gophercloud's
// ErrUnexpectedResponseCode prints it), which is exactly what must not land on
// the status line.
func errSummary(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if code, ok := httpStatus(err); ok {
		return fmt.Sprintf("%d %s", code, http.StatusText(code))
	}
	const maxLen = 80
	if r := []rune(s); len(r) > maxLen {
		return string(r[:maxLen-1]) + "…"
	}
	return s
}

// compactDuration renders a duration the way an operator reads one: whole
// milliseconds below a second, one decimal below a minute, and Go's own
// spelling above that. time.Duration's own String would put "218.394122ms" on
// the status line.
func compactDuration(d time.Duration) string {
	// Rounded before the branch, not inside it, so 999.6ms reads as "1s" rather
	// than "1000ms".
	d = d.Round(time.Millisecond)
	switch {
	case d < 0:
		return "0ms"
	case d < time.Second:
		return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
	case d < time.Minute:
		return strconv.FormatFloat(d.Round(100*time.Millisecond).Seconds(), 'g', -1, 64) + "s"
	default:
		return d.Round(time.Second).String()
	}
}

// plural renders "1 row" / "42 rows".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
