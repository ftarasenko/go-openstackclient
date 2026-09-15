package output

import (
	"strings"
	"testing"
)

// stubHighlighter is a decorator scripted by the test: it returns the rows and
// styles it was built with, ignoring what it is handed.
type stubHighlighter struct {
	rows   [][]any
	styles [][]CellStyle
	seen   [][]string // the columns each call was made with
}

func (s *stubHighlighter) Highlight(cols []string, rows [][]any) ([][]any, [][]CellStyle) {
	s.seen = append(s.seen, cols)
	if s.rows != nil {
		rows = s.rows
	}
	return rows, s.styles
}

func TestSetDisplayWidthFitsTablesRenderedIntoABuffer(t *testing.T) {
	// The defect this closes: width is normally derived from the writer, so a
	// table rendered into a buffer — which is what every --watch frame is —
	// would come out unbounded and be clipped by whatever painted it. That is
	// exactly what `watch -n1 koc …` does today through a pipe.
	tbl := Table{
		Columns: []string{"ID", "Name", "Status", "Networks"},
		Rows: [][]any{
			{"11111111-1111-1111-1111-111111111111", "web-01", "ACTIVE", "private=192.0.2.10, public=203.0.113.7"},
		},
	}

	var unbounded strings.Builder
	o := &Options{Format: FormatTable}
	if err := o.WriteList(&unbounded, tbl); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	if got := maxLine(unbounded.String()); got <= 60 {
		t.Fatalf("the unbounded table is %d columns wide; the test needs a wider one", got)
	}

	var fitted strings.Builder
	o.SetDisplayWidth(60)
	if err := o.WriteList(&fitted, tbl); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	if got := maxLine(fitted.String()); got > 60 {
		t.Errorf("the table is %d columns wide, want at most 60", got)
	}

	// And an explicit --max-width still wins, as it does everywhere else: the
	// two renders below differ only in the measured width, and must come out
	// the same because --max-width outranks it.
	var cappedWithWidth, cappedWithout strings.Builder
	o.MaxWidth = 40
	if err := o.WriteList(&cappedWithWidth, tbl); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	o.SetDisplayWidth(0)
	if err := o.WriteList(&cappedWithout, tbl); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	if cappedWithWidth.String() != cappedWithout.String() {
		t.Error("the measured width overrode --max-width")
	}

	// Zero restores the derive-from-writer default.
	o.MaxWidth = 0
	var restored strings.Builder
	if err := o.WriteList(&restored, tbl); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	if restored.String() != unbounded.String() {
		t.Error("SetDisplayWidth(0) did not restore the default width behaviour")
	}
}

func TestHighlightDecoratesCellsAfterPadding(t *testing.T) {
	h := &stubHighlighter{styles: [][]CellStyle{{CellPlain, CellChanged}}}
	o := &Options{Format: FormatTable}
	o.SetHighlighter(h)

	var buf strings.Builder
	err := o.WriteList(&buf, Table{
		Columns: []string{"Name", "Status"},
		Rows:    [][]any{{"web-01", "ACTIVE"}},
	})
	if err != nil {
		t.Fatalf("WriteList: %v", err)
	}

	got := buf.String()
	// The escape wraps the *padded* cell. Decorating before padding would make
	// %-*s count the escape's runes as content and knock the column out of
	// alignment; decorating only the text would leave the highlight ragged.
	if !strings.Contains(got, "\x1b[7mACTIVE\x1b[0m") {
		t.Errorf("the changed cell is not reverse-video:\n%s", got)
	}
	if strings.Contains(got, "\x1b[7mweb-01") {
		t.Errorf("an unchanged cell was decorated:\n%s", got)
	}
	// Every line of the table still has the same width: the escapes must not
	// count towards it.
	if !uniformWidth(got) {
		t.Errorf("the escapes broke the table's alignment:\n%s", got)
	}
}

func TestHighlightStylesEveryStyleKind(t *testing.T) {
	for _, tc := range []struct {
		style CellStyle
		seq   string
	}{
		{CellChanged, "\x1b[7m"},
		{CellAdded, "\x1b[32m"},
		{CellRemoved, "\x1b[2m"},
	} {
		h := &stubHighlighter{styles: [][]CellStyle{{tc.style}}}
		o := &Options{Format: FormatTable}
		o.SetHighlighter(h)

		var buf strings.Builder
		if err := o.WriteList(&buf, Table{Columns: []string{"Name"}, Rows: [][]any{{"web-01"}}}); err != nil {
			t.Fatalf("WriteList: %v", err)
		}
		if !strings.Contains(buf.String(), tc.seq) {
			t.Errorf("style %d did not emit %q:\n%s", tc.style, tc.seq, buf.String())
		}
	}
}

func TestHighlightMayAddRows(t *testing.T) {
	h := &stubHighlighter{
		rows:   [][]any{{"web-01"}, {"web-02"}},
		styles: [][]CellStyle{{CellPlain}, {CellRemoved}},
	}
	o := &Options{Format: FormatTable}
	o.SetHighlighter(h)

	var buf strings.Builder
	if err := o.WriteList(&buf, Table{Columns: []string{"Name"}, Rows: [][]any{{"web-01"}}}); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	// A departed row is held on screen for one frame, so the decorator has to be
	// able to put back a row the command no longer returns.
	if !strings.Contains(buf.String(), "web-02") {
		t.Errorf("the decorator's extra row was dropped:\n%s", buf.String())
	}
}

func TestHighlightNeverAppliesToMachineFormats(t *testing.T) {
	for _, format := range []string{FormatJSON, FormatYAML, FormatCSV, FormatValue} {
		h := &stubHighlighter{styles: [][]CellStyle{{CellChanged}}}
		o := &Options{Format: format}
		o.SetHighlighter(h)

		var buf strings.Builder
		if err := o.WriteList(&buf, Table{Columns: []string{"Name"}, Rows: [][]any{{"web-01"}}}); err != nil {
			t.Fatalf("WriteList(%s): %v", format, err)
		}
		if strings.ContainsRune(buf.String(), 0x1b) {
			t.Errorf("-f %s emitted an escape sequence; the machine formats have to stay byte-exact:\n%q",
				format, buf.String())
		}
		if len(h.seen) != 0 {
			t.Errorf("-f %s consulted the decorator", format)
		}
	}
}

func TestHighlightAppliesToShowOutput(t *testing.T) {
	h := &stubHighlighter{styles: [][]CellStyle{{CellPlain, CellPlain}, {CellPlain, CellChanged}}}
	o := &Options{Format: FormatTable}
	o.SetHighlighter(h)

	var buf strings.Builder
	err := o.WriteSingle(&buf, []string{"name", "status"}, []any{"web-01", "ACTIVE"})
	if err != nil {
		t.Fatalf("WriteSingle: %v", err)
	}
	if !strings.Contains(buf.String(), "\x1b[7mACTIVE") {
		t.Errorf("a watched `show` does not highlight its changed field:\n%s", buf.String())
	}
	if len(h.seen) != 1 || h.seen[0][0] != "Field" {
		t.Errorf("the decorator was handed %v, want the Field/Value columns", h.seen)
	}
}

func TestHighlightIsHandedTheSelectedColumnsOnly(t *testing.T) {
	h := &stubHighlighter{}
	o := &Options{Format: FormatTable, Columns: []string{"Status"}}
	o.SetHighlighter(h)

	var buf strings.Builder
	err := o.WriteList(&buf, Table{
		Columns: []string{"Name", "Status"},
		Rows:    [][]any{{"web-01", "ACTIVE"}},
	})
	if err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	// -c has already narrowed the table by the time the decorator sees it, so
	// the style matrix it returns lines up with what is actually rendered.
	if len(h.seen) != 1 || len(h.seen[0]) != 1 || h.seen[0][0] != "Status" {
		t.Errorf("the decorator was handed %v, want just [Status]", h.seen)
	}
}

func TestHighlightCannotInjectEscapesThroughCellValues(t *testing.T) {
	// The escape-stripping invariant: a hostile resource name must never reach
	// the terminal as an escape. A decorator returns a CellStyle, not a
	// sequence, so the only escapes koc ever prints are the ones it composed
	// itself — and a value the decorator puts back is sanitised like any other.
	h := &stubHighlighter{
		rows:   [][]any{{"\x1b[31mred\x1b[0m"}},
		styles: [][]CellStyle{{CellRemoved}},
	}
	o := &Options{Format: FormatTable}
	o.SetHighlighter(h)

	var buf strings.Builder
	if err := o.WriteList(&buf, Table{Columns: []string{"Name"}, Rows: [][]any{{"x"}}}); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b[31m") {
		t.Errorf("a server-supplied escape survived the decorator path:\n%q", buf.String())
	}
	if !strings.Contains(buf.String(), "\x1b[2m") {
		t.Errorf("koc's own style sequence was lost:\n%q", buf.String())
	}
}

// maxLine reports the widest line in s, in runes.
func maxLine(s string) int {
	var widest int
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if n := len([]rune(ln)); n > widest {
			widest = n
		}
	}
	return widest
}

// uniformWidth reports whether every line of a rendered table has the same
// printed width once the escapes are discounted.
func uniformWidth(s string) bool {
	var want int
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		n := len([]rune(ansiRe.ReplaceAllString(ln, "")))
		if want == 0 {
			want = n
			continue
		}
		if n != want {
			return false
		}
	}
	return true
}

func TestCompactRowsPutEachRowOnOneLine(t *testing.T) {
	tbl := Table{
		Columns: []string{"Name", "Networks"},
		Rows: [][]any{
			{"web-01", "tenant-net-019=198.51.100.20, 203.0.113.20, 192.0.2.44"},
			{"web-02", "short"},
		},
	}

	var wrapped, compact strings.Builder
	o := &Options{Format: FormatTable, MaxWidth: 40}
	if err := o.WriteList(&wrapped, tbl); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	o.SetCompactRows(true)
	if err := o.WriteList(&compact, tbl); err != nil {
		t.Fatalf("WriteList: %v", err)
	}

	// Measured on a 250-server fleet, this is the difference between 1.96 and
	// 1.00 physical lines per row at 120 columns.
	wn, cn := lineCount(wrapped.String()), lineCount(compact.String())
	if cn >= wn {
		t.Errorf("compact rendered %d lines and wrapped %d; compaction did nothing", cn, wn)
	}
	// Two borders, a header, a rule and one line per row.
	if want := 2 + 1 + 1 + 2; cn != want {
		t.Errorf("compact rendered %d lines, want %d:\n%s", cn, want, compact.String())
	}
	if !strings.Contains(compact.String(), "…") {
		t.Errorf("the over-long cell was not cut short:\n%s", compact.String())
	}
	if maxLine(compact.String()) > 40 {
		t.Errorf("compact output is %d columns, over --max-width:\n%s", maxLine(compact.String()), compact.String())
	}
}

func TestCompactRowsFoldMultiLineCells(t *testing.T) {
	// A cell that already contains newlines — an address map, a property list —
	// becomes one line rather than several.
	var buf strings.Builder
	o := &Options{Format: FormatTable}
	o.SetCompactRows(true)
	if err := o.WriteList(&buf, Table{
		Columns: []string{"Name", "Addresses"},
		Rows:    [][]any{{"web-01", "private=192.0.2.10\npublic=203.0.113.7"}},
	}); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	if n := lineCount(buf.String()); n != 5 {
		t.Errorf("rendered %d lines, want 5:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "private=192.0.2.10 public=203.0.113.7") {
		t.Errorf("the folded cell lost its separator:\n%s", buf.String())
	}
}

func TestCompactRowsAreOffByDefault(t *testing.T) {
	// An unwatched run, and a watched one that has not asked, render exactly as
	// they always have.
	tbl := Table{
		Columns: []string{"Name", "Networks"},
		Rows:    [][]any{{"web-01", "tenant-net-019=198.51.100.20, 203.0.113.20, 192.0.2.44"}},
	}
	var before, after strings.Builder
	o := &Options{Format: FormatTable, MaxWidth: 40}
	if err := o.WriteList(&before, tbl); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	o.SetCompactRows(true)
	o.SetCompactRows(false)
	if err := o.WriteList(&after, tbl); err != nil {
		t.Fatalf("WriteList: %v", err)
	}
	if before.String() != after.String() {
		t.Error("turning compaction off did not restore the default rendering")
	}
}

func TestTruncateCell(t *testing.T) {
	for _, tc := range []struct {
		in    string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"truncate-me", 10, "truncate-…"},
		{"multi\nline", 20, "multi line"},
		{"x", 1, "x"},
		{"xyz", 1, "…"},
		{"anything", 0, "anything"}, // no width to fit to; leave it alone
	} {
		got := truncateCell(tc.in, tc.width)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("truncateCell(%q, %d) = %q, want [%q]", tc.in, tc.width, got, tc.want)
		}
	}
}

// lineCount reports how many physical lines s holds.
func lineCount(s string) int {
	return len(strings.Split(strings.TrimRight(s, "\n"), "\n"))
}
