package watch

import "strings"

// A watched `server list --all` does not fit on a screen and never will: a
// 250-server fleet measures 495 physical lines at 120 columns, and the painter
// used to keep the first screenful and say "… +407 more line(s)" — which named
// the problem and offered no way out of it.
//
// So the frame is a window onto the lines, not the first of them.

// viewport is the window: how far down the frame it starts, and what the last
// paint resolved that to once the terminal's height was known.
type viewport struct {
	// offset is the first body line shown, in lines from the top of the body.
	offset int

	// total and shown are the last paint's measurements, kept so the status
	// line can report the position and the page keys know how far a page is.
	total int
	shown int
}

// frame is one paint's worth of lines, already windowed.
type frame struct {
	// head is pinned above the window: a table's top border, its header row and
	// the rule under it. Column names that scroll off the top make the rest of
	// the table unreadable, which is most of what is wrong with paging a table
	// through less(1).
	head []string
	// body is the visible slice of the scrollable remainder.
	body []string
	// more is how many body lines are below the window.
	more int
	// first and last are the 1-based body lines on screen, for the status line.
	first, last int
}

// stickyHead returns the leading lines to pin, and the rest.
//
// The shape is koc's own — writeTable emits a border, the header row, and a
// second border — so matching on it is reading back something this process
// wrote, not guessing at someone else's output. A frame that is not a table
// (-f value, a console log, the key map) simply has no head.
func stickyHead(lines []string) (head, body []string) {
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "+-") || !strings.HasPrefix(lines[2], "+-") {
		return nil, lines
	}
	return lines[:3], lines[3:]
}

// apply windows lines to a terminal of the given height, clamping the offset to
// what there is to show. reserve is the number of lines the caller keeps for
// itself (the status line and its blank).
//
// The offset is clamped rather than rejected: a refresh that returns fewer rows
// than the last one would otherwise leave the window past the end of the frame,
// showing nothing at all.
func (v *viewport) apply(lines []string, rows, reserve int) frame {
	head, body := stickyHead(lines)
	if rows <= 0 {
		// No measurable terminal, so nothing can be windowed sensibly. Show it
		// all and let whatever is painting decide.
		v.offset, v.total, v.shown = 0, len(body), len(body)
		return frame{head: head, body: body, first: 1, last: len(body)}
	}

	avail := rows - reserve - len(head)
	if avail < 1 {
		avail = 1
	}
	// One line goes to the "more below" marker whenever there is one.
	if len(body) > avail {
		avail--
		if avail < 1 {
			avail = 1
		}
	}

	v.total, v.shown = len(body), avail
	if last := len(body) - avail; v.offset > last {
		v.offset = last
	}
	if v.offset < 0 {
		v.offset = 0
	}

	end := min(v.offset+avail, len(body))
	f := frame{head: head, body: body[v.offset:end], more: len(body) - end}
	if len(body) > 0 {
		f.first, f.last = v.offset+1, end
	}
	return f
}

// fits reports whether the whole frame was on screen at the last paint, which
// is when the position is not worth saying.
func (v *viewport) fits() bool { return v.total <= v.shown }

// scroll moves the window by n lines, positive for down. The clamp happens at
// the next paint, which is the only place the terminal's height is known.
func (v *viewport) scroll(n int) {
	v.offset += n
	if v.offset < 0 {
		v.offset = 0
	}
}

// page is the distance the page keys move: a screenful less a line of overlap,
// so the line you were reading is still there afterwards.
func (v *viewport) page() int {
	if v.shown <= 1 {
		return 1
	}
	return v.shown - 1
}

// top and bottom jump to either end. bottom overshoots deliberately; apply
// clamps it to the last screenful, which is what "end" means on a frame whose
// length changes under you.
func (v *viewport) top()    { v.offset = 0 }
func (v *viewport) bottom() { v.offset = v.total }

// reset returns to the top, for when the content changes out from under the
// window entirely — opening or closing the key map.
func (v *viewport) reset() { v.offset = 0 }
