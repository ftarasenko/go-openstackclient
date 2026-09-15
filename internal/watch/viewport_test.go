package watch

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// table builds a frame shaped like one koc renders: a border, a header, a rule,
// n data rows and a closing border.
func table(n int) []string {
	lines := []string{"+----+", "| ID |", "+----+"}
	for i := 1; i <= n; i++ {
		lines = append(lines, fmt.Sprintf("| r%02d |", i))
	}
	return append(lines, "+----+")
}

func TestViewportPinsTheTableHeader(t *testing.T) {
	v := &viewport{}
	f := v.apply(table(100), 24, 2)

	// Column names that scroll off the top make the rest of the table
	// unreadable, which is most of what is wrong with paging a table through
	// less(1).
	if len(f.head) != 3 || f.head[1] != "| ID |" {
		t.Fatalf("head %q, want the border, header and rule", f.head)
	}
	v.scroll(40)
	f = v.apply(table(100), 24, 2)
	if len(f.head) != 3 || f.head[1] != "| ID |" {
		t.Errorf("the header did not survive scrolling: %q", f.head)
	}
	if strings.Contains(strings.Join(f.body, "\n"), "| r01 |") {
		t.Error("the body did not scroll")
	}
}

func TestViewportLeavesNonTablesAlone(t *testing.T) {
	// -f value, a console log, the key map: nothing to pin.
	f := (&viewport{}).apply([]string{"a", "b", "c", "d"}, 24, 2)
	if len(f.head) != 0 {
		t.Errorf("head %q, want none", f.head)
	}
	if len(f.body) != 4 {
		t.Errorf("body %q, want all four lines", f.body)
	}
}

func TestViewportWindowsAndReportsPosition(t *testing.T) {
	v := &viewport{}
	lines := table(100) // 3 head + 101 body
	f := v.apply(lines, 24, 2)

	if f.first != 1 {
		t.Errorf("first line %d, want 1", f.first)
	}
	if f.more != 101-len(f.body) {
		t.Errorf("more=%d does not account for the body", f.more)
	}
	if v.fits() {
		t.Error("a 101-line body on a 24-row terminal was reported as fitting")
	}
	total := len(f.body) + f.more
	if total != 101 {
		t.Errorf("the window accounts for %d lines, want 101", total)
	}
}

func TestViewportBottomAndTop(t *testing.T) {
	v := &viewport{}
	lines := table(100)
	v.apply(lines, 24, 2) // measure first

	v.bottom()
	f := v.apply(lines, 24, 2)
	if f.more != 0 {
		t.Errorf("%d lines still below after End", f.more)
	}
	if f.last != 101 {
		t.Errorf("last line %d, want 101", f.last)
	}

	v.top()
	f = v.apply(lines, 24, 2)
	if f.first != 1 {
		t.Errorf("first line %d after Home, want 1", f.first)
	}
}

func TestViewportClampsWhenTheFrameShrinks(t *testing.T) {
	v := &viewport{}
	v.apply(table(100), 24, 2)
	v.bottom()
	v.apply(table(100), 24, 2)

	// The next refresh returns a far shorter list. Left alone, the window would
	// sit past the end of it and show nothing at all.
	f := v.apply(table(5), 24, 2)
	if len(f.body) == 0 {
		t.Fatal("the window fell off the end of a shrunken frame")
	}
	if f.first != 1 {
		t.Errorf("first line %d, want 1 — the whole frame now fits", f.first)
	}
	if !v.fits() {
		t.Error("a 6-line body on a 24-row terminal was reported as not fitting")
	}
}

func TestViewportPageOverlapsByALine(t *testing.T) {
	v := &viewport{}
	v.apply(table(100), 24, 2)
	// A page that moved by exactly a screenful would leave no line in common,
	// so the line being read vanishes.
	if v.page() != v.shown-1 {
		t.Errorf("page is %d of %d shown", v.page(), v.shown)
	}
}

func TestViewportWithoutAMeasurableTerminal(t *testing.T) {
	// No size to window to — show everything rather than guessing.
	f := (&viewport{}).apply(table(100), 0, 2)
	if len(f.body) != 101 || f.more != 0 {
		t.Errorf("body %d lines, %d more; want all 101", len(f.body), f.more)
	}
}

func TestScrollKeysMoveTheWindow(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []keyCode
		want int
	}{
		{"line down", []keyCode{keyLineDown}, 1},
		{"arrow down", []keyCode{keyDown}, 1},
		{"down then up", []keyCode{keyDown, keyDown, keyUp}, 1},
		{"never above the top", []keyCode{keyUp, keyUp}, 0},
		{"home after down", []keyCode{keyDown, keyDown, keyHome}, 0},
		{"g after down", []keyCode{keyDown, keyTop}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRunner(Options{Interval: time.Second})
			for _, k := range tc.keys {
				if !r.scrollKey(k) {
					t.Fatalf("%v was not taken as a scroll key", k)
				}
			}
			if r.view.offset != tc.want {
				t.Errorf("offset %d, want %d", r.view.offset, tc.want)
			}
		})
	}
}

func TestScrollKeysAreAnsweredDuringARefresh(t *testing.T) {
	// Scrolling only changes which lines are painted, so it must not wait for
	// the round trip — the frame you are trying to read is already on screen.
	h := newHarness()
	h.render = func(_ int, out, _ io.Writer) error {
		_, err := io.WriteString(out, strings.Join(table(100), "\n")+"\n")
		return err
	}
	// Scripted for the *second* refresh, so a frame is already on screen: there
	// is nothing to scroll during the first one.
	o, render := h.midRefreshKeys(Options{Interval: time.Second}, 2, keyPageDown, keyQuit)

	err := Run(context.Background(), o, &h.out, &h.errOut,
		func(_ context.Context, out, warn io.Writer) error {
			h.frames++
			return render(h.frames, out, warn)
		})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Asserted through what the operator sees rather than through a seam into
	// the loop: the status line names the window.
	if status := lastStatus(h.out.String()); !strings.Contains(status, "of 101") ||
		strings.Contains(status, "lines 1–") {
		t.Errorf("the page key did not scroll during the refresh: %q", status)
	}
}

func TestTheKeyMapKeepsItsOwnPlace(t *testing.T) {
	r := newTestRunner(Options{Interval: time.Second})
	r.scrollKey(keyDown)
	r.scrollKey(keyDown)

	r.showHelp = true
	r.helpView.reset()
	r.scrollKey(keyDown)
	if r.view.offset != 2 {
		t.Errorf("scrolling the key map moved the frame to %d", r.view.offset)
	}
	r.showHelp = false
	// Closing the key map puts the operator back where they were in the list,
	// not at the top of it.
	if r.view.offset != 2 {
		t.Errorf("the frame's place was lost: offset %d, want 2", r.view.offset)
	}
}

func TestMoreMarkerNamesTheWayOut(t *testing.T) {
	// The version that only said how much was left described a dead end.
	m := moreMarker(407)
	if !strings.Contains(m, "407") {
		t.Errorf("marker %q does not say how much is left", m)
	}
	for _, want := range []string{"j", "PgDn", "G", "?"} {
		if !strings.Contains(m, want) {
			t.Errorf("marker %q does not mention %q", m, want)
		}
	}
}

func TestStatusLineReportsThePosition(t *testing.T) {
	h := newHarness()
	h.render = func(_ int, out, _ io.Writer) error {
		_, err := io.WriteString(out, strings.Join(table(100), "\n")+"\n")
		return err
	}
	if err := h.keyed(t, Options{Interval: time.Second}, keyPageDown, keyQuit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	status := lastStatus(h.out.String())
	if !strings.Contains(status, "of 101") {
		t.Errorf("the status line does not report the frame's length: %q", status)
	}
	if strings.Contains(status, "lines 1–") {
		t.Errorf("the status line still reports the top after a page down: %q", status)
	}
}

func TestStatusLineOmitsThePositionWhenItAllFits(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")
	if err := h.keyed(t, Options{Interval: time.Second}, keyQuit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// A range covering the whole frame is noise.
	if strings.Contains(lastStatus(h.out.String()), "lines ") {
		t.Errorf("a frame that fits still reported a range: %q", lastStatus(h.out.String()))
	}
}
