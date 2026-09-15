package watch

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestHelpKeyTogglesThePanel(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("+------+\n| web  |\n+------+\n")

	// Open the key map, then close it again, then quit. The frame has to come
	// back: a help panel you cannot get out of is worse than none.
	if err := h.keyed(t, Options{Interval: time.Second}, keyHelp, keyHelp, keyQuit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.frames != 1 {
		t.Errorf("rendered %d frames, want 1 — opening the help must not refresh", h.frames)
	}

	painted := h.out.String()
	open := strings.Index(painted, "koc --watch — keys")
	if open < 0 {
		t.Fatalf("the key map was never painted:\n%q", painted)
	}
	// The last paint before the quit is the frame again, not the panel.
	if last := strings.LastIndex(painted, "| web  |"); last < open {
		t.Error("closing the help did not bring the frame back")
	}
}

func TestHelpPanelReplacesTheFrame(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("THE-TABLE\n")

	if err := h.keyed(t, Options{Interval: time.Second}, keyHelp, keyQuit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The panel is drawn in place of the frame rather than over it: a table at
	// the terminal's width leaves no rectangle to float in.
	frame := lastPaint(h.out.String())
	if !strings.Contains(frame, "koc --watch — keys") {
		t.Fatalf("the last paint is not the key map:\n%q", frame)
	}
	if strings.Contains(frame, "THE-TABLE") {
		t.Errorf("the frame was still drawn under the key map:\n%q", frame)
	}
}

func TestHelpPanelNamesEveryBinding(t *testing.T) {
	// helpLines is hand-written, so this is the check that a binding added to
	// keys.go does not stay undocumented.
	panel := strings.Join(helpLines(), "\n")
	for _, key := range []string{"q", "space, r", "p", "+, =", "-", "d", "?", "Ctrl-C"} {
		if !helpMentions(panel, key) {
			t.Errorf("the key map does not name %q:\n%s", key, panel)
		}
	}
	// The two things the bare letters got wrong.
	if !strings.Contains(panel, "slower") || !strings.Contains(panel, "faster") {
		t.Error("the key map does not say which direction +/- move the interval")
	}
	if !strings.Contains(panel, MinInterval.String()) {
		t.Error("the key map does not name the interval floor")
	}
}

func TestHelpHintReplacesTheLetterList(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")
	if err := h.keyed(t, Options{Interval: time.Second}, keyQuit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	status := lastStatus(h.out.String())
	if !strings.Contains(status, "? for help") {
		t.Errorf("the status line does not point at the key map: %q", status)
	}
	if strings.Contains(status, "q p r + - d") {
		t.Errorf("the status line still lists the bare letters: %q", status)
	}
}

func TestHelpHintSaysHowToClose(t *testing.T) {
	r := newTestRunner(Options{Interval: time.Second})
	r.o.Keys = true
	r.showHelp = true
	if got := r.helpHint(); !strings.Contains(got, "closes") {
		t.Errorf("hint %q does not say how to close the help", got)
	}
}

func TestHelpIsAbsentWhereThereAreNoKeys(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")
	// Appending frames to a stream has no screen for a panel to occupy, and no
	// keys to open it with.
	o := h.options(Options{Interval: time.Second, Count: 1, Plain: true})
	err := Run(context.Background(), o, &h.out, &h.errOut, func(_ context.Context, out, warn io.Writer) error {
		h.frames++
		return h.render(h.frames, out, warn)
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(h.out.String(), "? for help") {
		t.Error("appended output carries the key-map hint")
	}
}

// lastPaint returns the last frame the painter wrote.
func lastPaint(painted string) string {
	i := strings.LastIndex(painted, homeErase)
	if i < 0 {
		return painted
	}
	return painted[i:]
}

func TestHelpPanelFitsAConventionalTerminal(t *testing.T) {
	// The painter clips an overlong frame and says how much it dropped. A help
	// panel that gets clipped is the one frame that must not, so this pins the
	// shape rather than trusting it: 20 rows is the smallest terminal anyone
	// watches a fleet in, and two of them go to the status line.
	lines := helpLines()
	if len(lines) > 18 {
		t.Errorf("the key map is %d lines; it will clip on a 20-row terminal", len(lines))
	}
	for i, ln := range lines {
		if n := len([]rune(ln)); n > 70 {
			t.Errorf("line %d is %d columns:\n%s", i, n, ln)
		}
	}
}

func TestHelpPanelIsNotClippedAtTwentyRows(t *testing.T) {
	h := newHarness()
	h.render = staticFrames("row\n")
	o := h.options(Options{Interval: time.Second, Keys: true})
	o.size = func() (int, int) { return 80, 20 }
	h.clock.blocked = true
	h.keys <- keyHelp
	h.keys <- keyQuit

	if err := Run(context.Background(), o, &h.out, &h.errOut, func(_ context.Context, out, warn io.Writer) error {
		h.frames++
		return h.render(h.frames, out, warn)
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	painted := h.out.String()
	if strings.Contains(painted, "more line(s)") {
		t.Errorf("the key map was clipped at 80x20:\n%q", painted)
	}
	if !strings.Contains(painted, "Ctrl-C") {
		t.Error("the last binding did not survive to the screen")
	}
}
