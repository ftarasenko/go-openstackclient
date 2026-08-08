package output

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func sampleTable() Table {
	return Table{
		Columns: []string{"UUID", "Name", "Maintenance"},
		Rows: [][]any{
			{"u1", "node-a", false},
			{"u2", "node-b", true},
		},
	}
}

func TestWriteList_Table(t *testing.T) {
	o := &Options{Format: FormatTable}
	var buf bytes.Buffer
	if err := o.WriteList(&buf, sampleTable()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "+--") || !strings.Contains(out, "| UUID") {
		t.Errorf("table missing borders/headers:\n%s", out)
	}
	for _, w := range []string{"node-a", "node-b", "false", "true"} {
		if !strings.Contains(out, w) {
			t.Errorf("table missing %q:\n%s", w, out)
		}
	}
}

func TestWriteList_JSON(t *testing.T) {
	o := &Options{Format: FormatJSON}
	var buf bytes.Buffer
	if err := o.WriteList(&buf, sampleTable()); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(got) != 2 || got[0]["Name"] != "node-a" || got[1]["Maintenance"] != true {
		t.Errorf("unexpected JSON: %#v", got)
	}
}

func TestWriteList_ValueTabSeparated(t *testing.T) {
	o := &Options{Format: FormatValue}
	var buf bytes.Buffer
	if err := o.WriteList(&buf, sampleTable()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	if lines[0] != "u1\tnode-a\tfalse" {
		t.Errorf("value row = %q", lines[0])
	}
	if strings.Contains(buf.String(), "UUID") {
		t.Errorf("value output must have no header")
	}
}

func TestWriteList_CSV(t *testing.T) {
	o := &Options{Format: FormatCSV}
	var buf bytes.Buffer
	if err := o.WriteList(&buf, sampleTable()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if lines[0] != "UUID,Name,Maintenance" {
		t.Errorf("csv header = %q", lines[0])
	}
	if lines[1] != "u1,node-a,false" {
		t.Errorf("csv row = %q", lines[1])
	}
}

func TestColumnSelection_OrderAndCaseInsensitive(t *testing.T) {
	o := &Options{Format: FormatCSV, Columns: []string{"name", "uuid"}}
	var buf bytes.Buffer
	if err := o.WriteList(&buf, sampleTable()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if lines[0] != "Name,UUID" {
		t.Errorf("selected header = %q, want reordered case-insensitive match", lines[0])
	}
	if lines[1] != "node-a,u1" {
		t.Errorf("selected row = %q", lines[1])
	}
}

func TestWriteSingle_Table(t *testing.T) {
	o := &Options{Format: FormatTable}
	var buf bytes.Buffer
	err := o.WriteSingle(&buf, []string{"UUID", "Name"}, []any{"u1", "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Field") || !strings.Contains(out, "Value") {
		t.Errorf("single table should have Field/Value headers:\n%s", out)
	}
	if !strings.Contains(out, "node-a") {
		t.Errorf("single table missing value:\n%s", out)
	}
}

func TestValidate(t *testing.T) {
	if err := (&Options{Format: "bogus"}).Validate(); err == nil {
		t.Error("expected error for invalid format")
	}
	for _, f := range allFormats {
		if err := (&Options{Format: f}).Validate(); err != nil {
			t.Errorf("format %q should be valid: %v", f, err)
		}
	}
}

func TestCell_StructuredValues(t *testing.T) {
	if got := cell(map[string]string{"b": "2", "a": "1"}); got != "a='1', b='2'" {
		t.Errorf("map cell = %q, want sorted key=val", got)
	}
	if got := cell([]string{"x", "y"}); got != "x, y" {
		t.Errorf("slice cell = %q", got)
	}
	if got := cell(nil); got != "" {
		t.Errorf("nil cell = %q, want empty", got)
	}
}

func TestStripControl_ANSIAndControlChars(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain passthrough", "node-a", "node-a"},
		{"cyrillic passthrough", "проект", "проект"},
		{"strip CSI color", "\x1b[31mred\x1b[0m", "red"},
		{"strip screen clear + cursor home", "a\x1b[2J\x1b[1;1Hadmin", "aadmin"},
		{"strip OSC title set", "x\x1b]0;pwned\x07y", "xy"},
		{"strip carriage return", "real\rfake", "realfake"},
		{"strip BEL and NUL", "a\x07b\x00c", "abc"},
		{"keep tab and newline", "a\tb\nc", "a\tb\nc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripControl(tt.in); got != tt.want {
				t.Errorf("stripControl(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCell_SanitizesServerSuppliedString(t *testing.T) {
	if got := cell("vm\x1b[2Kadmin"); got != "vmadmin" {
		t.Errorf("cell did not strip ANSI: %q", got)
	}
}

// `-f value` is the format scripts redirect into a file, so it must emit the
// value byte-for-byte. Collapsing embedded newlines made `zone export showfile
// -f value > zone.txt` produce an unusable zonefile.
func TestWriteValue_EmitsEmbeddedNewlinesVerbatim(t *testing.T) {
	o := &Options{Format: FormatValue}
	var buf bytes.Buffer
	tbl := Table{Columns: []string{"A", "B"}, Rows: [][]any{{"x\ty\nz", "end"}}}
	if err := o.WriteList(&buf, tbl); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "x\ty\nz\tend\n"; got != want {
		t.Errorf("value output = %q, want %q", got, want)
	}
}

// The showfile workflow end to end: a multi-line zonefile written through
// WriteSingle with -f value must round-trip unchanged.
func TestWriteSingle_ValueRoundTripsAMultiLineZonefile(t *testing.T) {
	zonefile := "$ORIGIN example.com.\n$TTL 3600\nexample.com. IN SOA ns1.example.com. admin.example.com. 1 3600 600 86400 3600\nexample.com. IN NS ns1.example.com.\n"
	o := &Options{Format: FormatValue}
	var buf bytes.Buffer
	if err := o.WriteSingle(&buf, []string{"data"}, []any{zonefile}); err != nil {
		t.Fatal(err)
	}
	// WriteSingle terminates the record with its own newline; the zonefile
	// already ends in one, so trim exactly the added record separator.
	if got := strings.TrimSuffix(buf.String(), "\n"); got != zonefile {
		t.Errorf("zonefile did not round-trip\n got: %q\nwant: %q", got, zonefile)
	}
}

// A multi-line cell renders across several physical lines of its table row,
// the way cliff's PrettyTable does, instead of collapsing onto one.
func TestWriteTable_RendersMultiLineCellAcrossLines(t *testing.T) {
	o := &Options{Format: FormatTable}
	var buf bytes.Buffer
	tbl := Table{
		Columns: []string{"ID", "NS Records"},
		Rows:    [][]any{{"p1", "1:ns1.example.com.\n2:ns2.example.com."}, {"p2", "1:ns3.example.com."}},
	}
	if err := o.WriteList(&buf, tbl); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"| p1 | 1:ns1.example.com. |",
		"|    | 2:ns2.example.com. |",
		"| p2 | 1:ns3.example.com. |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing line %q\n---\n%s", want, out)
		}
	}
	// The column is as wide as the widest LINE, not the whole cell.
	if strings.Contains(out, "1:ns1.example.com. 2:ns2.example.com.") {
		t.Errorf("multi-line cell was flattened\n---\n%s", out)
	}
}

// Tabs still collapse to spaces in a table cell — they would knock the columns
// out of alignment — while newlines survive.
func TestWriteTable_CollapsesTabsButKeepsNewlines(t *testing.T) {
	o := &Options{Format: FormatTable}
	var buf bytes.Buffer
	tbl := Table{Columns: []string{"A"}, Rows: [][]any{{"x\ty\nz"}}}
	if err := o.WriteList(&buf, tbl); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "| x y |") || !strings.Contains(out, "| z   |") {
		t.Errorf("want a tab collapsed to a space and the newline kept\n---\n%s", out)
	}
}

// CSV must quote a field containing a newline (RFC 4180) rather than collapse it.
func TestWriteCSV_QuotesMultiLineCell(t *testing.T) {
	o := &Options{Format: FormatCSV}
	var buf bytes.Buffer
	tbl := Table{Columns: []string{"ID", "NS"}, Rows: [][]any{{"p1", "a\nb"}}}
	if err := o.WriteList(&buf, tbl); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "ID,NS\np1,\"a\nb\"\n"; got != want {
		t.Errorf("csv = %q, want %q", got, want)
	}
	// And it must parse back to the original value.
	recs, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv did not parse back: %v", err)
	}
	if recs[1][1] != "a\nb" {
		t.Errorf("round-tripped cell = %q, want %q", recs[1][1], "a\nb")
	}
}

// json and yaml were already correct; lock that in alongside the others.
func TestWriteSingle_JSONAndYAMLKeepNewlines(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   string
	}{
		{FormatJSON, `"a\nb"`},
		{FormatYAML, "|-"}, // yaml emits a literal block scalar
	} {
		o := &Options{Format: tc.format}
		var buf bytes.Buffer
		if err := o.WriteSingle(&buf, []string{"data"}, []any{"a\nb"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), tc.want) {
			t.Errorf("-f %s output %q does not contain %q", tc.format, buf.String(), tc.want)
		}
	}
}

func TestWriteTable_RuneWidthAlignment(t *testing.T) {
	// Cyrillic (multi-byte, single-width) content must align with the border.
	o := &Options{Format: FormatTable}
	var buf bytes.Buffer
	tbl := Table{Columns: []string{"Name"}, Rows: [][]any{{"проект"}, {"abcdefgh"}}}
	if err := o.WriteList(&buf, tbl); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	// All border/content lines must be the same display (rune) width.
	want := utf8.RuneCountInString(lines[0])
	for i, ln := range lines {
		if n := utf8.RuneCountInString(ln); n != want {
			t.Errorf("line %d width = %d runes, want %d:\n%s", i, n, want, buf.String())
		}
	}
}

func TestWriteSingle_ElidesOversizedCell(t *testing.T) {
	blob := strings.Repeat("a", maxTableCell+500)
	o := &Options{Format: FormatTable}
	var buf bytes.Buffer
	if err := o.WriteSingle(&buf, []string{"user_data"}, []any{blob}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, blob) {
		t.Errorf("oversized cell was not elided:\n%s", out)
	}
	if !strings.Contains(out, "bytes; use -f yaml") {
		t.Errorf("elision placeholder missing:\n%s", out)
	}
}

func TestWriteSingle_NoElisionWhenColumnSelected(t *testing.T) {
	blob := strings.Repeat("a", maxTableCell+500)
	// An explicit -c selection means the user asked for this field: show it in full.
	o := &Options{Format: FormatTable, Columns: []string{"user_data"}}
	var buf bytes.Buffer
	if err := o.WriteSingle(&buf, []string{"user_data"}, []any{blob}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), blob) {
		t.Error("column-selected oversized cell should be shown in full")
	}
}

func TestWriteSingle_NoElisionInYAML(t *testing.T) {
	blob := strings.Repeat("a", maxTableCell+500)
	o := &Options{Format: FormatYAML}
	var buf bytes.Buffer
	if err := o.WriteSingle(&buf, []string{"user_data"}, []any{blob}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), blob) {
		t.Error("machine formats must never elide")
	}
}

func TestWriteTable_MaxWidthWrapsAndBounds(t *testing.T) {
	const maxW = 40
	val := strings.Repeat("word ", 30) // long, space-separated → wraps
	o := &Options{Format: FormatTable, MaxWidth: maxW}
	var buf bytes.Buffer
	if err := o.WriteSingle(&buf, []string{"Field"}, []any{strings.TrimSpace(val)}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if n := utf8.RuneCountInString(ln); n > maxW {
			t.Errorf("line exceeds --max-width %d (got %d):\n%s", maxW, n, out)
		}
	}
	// The content survives, just spread across multiple physical rows.
	if !strings.Contains(out, "word") {
		t.Errorf("wrapped content lost:\n%s", out)
	}
}

func TestWrapText(t *testing.T) {
	if got := wrapText("short", 0); len(got) != 1 || got[0] != "short" {
		t.Errorf("width 0 should not wrap: %q", got)
	}
	// Hard-break a token longer than the width.
	got := wrapText("aaaaaaaaaa", 4)
	if len(got) != 3 || got[0] != "aaaa" || got[2] != "aa" {
		t.Errorf("hard-break wrong: %q", got)
	}
	// Prefer wrapping at spaces.
	got = wrapText("aa bb cc dd", 5)
	for _, ln := range got {
		if utf8.RuneCountInString(ln) > 5 {
			t.Errorf("line over width: %q", got)
		}
	}
}

func TestWriteSingle_MismatchedFieldsValues(t *testing.T) {
	o := &Options{Format: FormatTable}
	var buf bytes.Buffer
	if err := o.WriteSingle(&buf, []string{"A", "B"}, []any{"only-one"}); err == nil {
		t.Error("expected error when values shorter than fields, got nil (panic risk)")
	}
}

// --sort-column orders rows in the output layer, so it works on every list in
// every format and needs no API support.
func TestWriteList_SortColumn(t *testing.T) {
	table := Table{
		Columns: []string{"Name", "Size", "Zone"},
		Rows: [][]any{
			{"beta", 10, "az2"},
			{"alpha", 9, "az1"},
			{"gamma", 100, "az1"},
		},
	}

	t.Run("string column", func(t *testing.T) {
		o := &Options{Format: FormatValue, SortColumns: []string{"Name"}}
		var buf bytes.Buffer
		if err := o.WriteList(&buf, cloneTable(table)); err != nil {
			t.Fatalf("WriteList: %v", err)
		}
		want := "alpha\t9\taz1\nbeta\t10\taz2\ngamma\t100\taz1\n"
		if buf.String() != want {
			t.Errorf("got:\n%swant:\n%s", buf.String(), want)
		}
	})

	// Numbers must compare numerically: 9 before 10 before 100, not "10" < "100" < "9".
	t.Run("numeric column", func(t *testing.T) {
		o := &Options{Format: FormatValue, SortColumns: []string{"Size"}}
		var buf bytes.Buffer
		if err := o.WriteList(&buf, cloneTable(table)); err != nil {
			t.Fatalf("WriteList: %v", err)
		}
		want := "alpha\t9\taz1\nbeta\t10\taz2\ngamma\t100\taz1\n"
		if buf.String() != want {
			t.Errorf("numeric sort went lexicographic:\ngot:\n%swant:\n%s", buf.String(), want)
		}
	})

	// Repeated keys break ties, in the order given.
	t.Run("multiple columns", func(t *testing.T) {
		o := &Options{Format: FormatValue, SortColumns: []string{"Zone", "Size"}}
		var buf bytes.Buffer
		if err := o.WriteList(&buf, cloneTable(table)); err != nil {
			t.Fatalf("WriteList: %v", err)
		}
		want := "alpha\t9\taz1\ngamma\t100\taz1\nbeta\t10\taz2\n"
		if buf.String() != want {
			t.Errorf("got:\n%swant:\n%s", buf.String(), want)
		}
	})

	// Column names are prose; operators type lower case.
	t.Run("case insensitive", func(t *testing.T) {
		o := &Options{Format: FormatValue, SortColumns: []string{"name"}}
		var buf bytes.Buffer
		if err := o.WriteList(&buf, cloneTable(table)); err != nil {
			t.Fatalf("WriteList: %v", err)
		}
		if !strings.HasPrefix(buf.String(), "alpha") {
			t.Errorf("lower-case column name did not match:\n%s", buf.String())
		}
	})

	// Sorting runs before -c narrows the columns, so a sort key need not be shown.
	t.Run("sort by a column that is not displayed", func(t *testing.T) {
		o := &Options{Format: FormatValue, Columns: []string{"Name"}, SortColumns: []string{"Size"}}
		var buf bytes.Buffer
		if err := o.WriteList(&buf, cloneTable(table)); err != nil {
			t.Fatalf("WriteList: %v", err)
		}
		want := "alpha\nbeta\ngamma\n"
		if buf.String() != want {
			t.Errorf("got:\n%swant:\n%s", buf.String(), want)
		}
	})

	t.Run("unknown column errors", func(t *testing.T) {
		o := &Options{Format: FormatValue, SortColumns: []string{"Nonesuch"}}
		var buf bytes.Buffer
		err := o.WriteList(&buf, cloneTable(table))
		if err == nil || !strings.Contains(err.Error(), "unknown sort column") {
			t.Fatalf("err = %v, want an unknown-sort-column error", err)
		}
	})

	// Rows that compare equal keep the order the API returned them in.
	t.Run("stable", func(t *testing.T) {
		dup := Table{
			Columns: []string{"Name", "Zone"},
			Rows:    [][]any{{"b", "az1"}, {"a", "az1"}, {"c", "az1"}},
		}
		o := &Options{Format: FormatValue, SortColumns: []string{"Zone"}}
		var buf bytes.Buffer
		if err := o.WriteList(&buf, dup); err != nil {
			t.Fatalf("WriteList: %v", err)
		}
		want := "b\taz1\na\taz1\nc\taz1\n"
		if buf.String() != want {
			t.Errorf("sort was not stable:\ngot:\n%swant:\n%s", buf.String(), want)
		}
	})
}

// cloneTable gives each subtest its own rows, since sortRows orders them in place.
func cloneTable(t Table) Table {
	rows := make([][]any, len(t.Rows))
	for i, r := range t.Rows {
		rows[i] = append([]any(nil), r...)
	}
	return Table{Columns: t.Columns, Rows: rows}
}

func TestCompareCells_NumericAndString(t *testing.T) {
	tests := []struct {
		name string
		a, b any
		want int
	}{
		{"ints", 9, 10, -1},
		{"equal ints", 5, 5, 0},
		{"int descending", 10, 9, 1},
		// Sizes and counts arrive as strings from several APIs.
		{"numeric strings", "9", "10", -1},
		{"floats", 1.5, 1.25, 1},
		{"mixed numeric types", int64(3), 4.0, -1},
		{"strings", "alpha", "beta", -1},
		// A number against a non-number falls back to string comparison, where
		// "10" precedes "ACTIVE" because '1' < 'A' in byte order.
		{"number vs word", 10, "ACTIVE", -1},
		{"nil sorts first", nil, "a", -1},
		{"empty string is not a number", "", "1", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareCells(tc.a, tc.b); got != tc.want {
				t.Errorf("compareCells(%v, %v) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// A time.Time must render one way in every format, and an absent one must not
// surface Go's zero time. Before this, "koc loadbalancer show" printed
// updated_at as "0001-01-01 00:00:00 +0000 UTC" in a table and
// "0001-01-01T00:00:00Z" in JSON, and a real timestamp printed Go's native
// format in the table but RFC 3339 in JSON.
func TestTimestampsRenderOneWay(t *testing.T) {
	created := time.Date(2023, 6, 15, 14, 14, 55, 0, time.UTC)
	fields := []string{"id", "created_at", "updated_at"}
	values := []any{"lb-1", created, time.Time{}}

	t.Run("table", func(t *testing.T) {
		var b bytes.Buffer
		o := &Options{Format: FormatTable}
		if err := o.WriteSingle(&b, fields, values); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if !strings.Contains(out, "2023-06-15T14:14:55Z") {
			t.Errorf("table lacks the RFC 3339 timestamp:\n%s", out)
		}
		if strings.Contains(out, "0001-01-01") {
			t.Errorf("table leaked Go's zero time:\n%s", out)
		}
		if strings.Contains(out, "+0000 UTC") {
			t.Errorf("table leaked Go's native time format:\n%s", out)
		}
	})

	t.Run("json", func(t *testing.T) {
		var b bytes.Buffer
		o := &Options{Format: FormatJSON}
		if err := o.WriteSingle(&b, fields, values); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b.Bytes(), &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["created_at"] != "2023-06-15T14:14:55Z" {
			t.Errorf("created_at = %v, want 2023-06-15T14:14:55Z", m["created_at"])
		}
		if m["updated_at"] != nil {
			t.Errorf("updated_at = %v, want null", m["updated_at"])
		}
	})

	t.Run("value", func(t *testing.T) {
		var b bytes.Buffer
		o := &Options{Format: FormatValue}
		if err := o.WriteSingle(&b, fields, values); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), "0001-01-01") {
			t.Errorf("value output leaked Go's zero time:\n%s", b.String())
		}
	})
}

// The same convention applies to list rows, and to *time.Time fields.
func TestTimestampsInListRows(t *testing.T) {
	created := time.Date(2026, 8, 7, 15, 36, 15, 0, time.UTC)
	tbl := Table{
		Columns: []string{"ID", "Created At", "Updated At", "Deleted At"},
		Rows:    [][]any{{"lb-1", created, time.Time{}, (*time.Time)(nil)}},
	}
	var b bytes.Buffer
	o := &Options{Format: FormatJSON}
	if err := o.WriteList(&b, tbl); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rows[0]["Created At"] != "2026-08-07T15:36:15Z" {
		t.Errorf("Created At = %v", rows[0]["Created At"])
	}
	if rows[0]["Updated At"] != nil {
		t.Errorf("Updated At = %v, want null", rows[0]["Updated At"])
	}
	if rows[0]["Deleted At"] != nil {
		t.Errorf("Deleted At = %v, want null", rows[0]["Deleted At"])
	}
}

// ColumnsWithin lets a command skip fetching what nobody selected, so it must
// answer "no" whenever anything outside the narrow set is still needed.
func TestColumnsWithin(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    Options
		want bool
	}{
		{"no selection means every column", Options{}, false},
		{"exact", Options{Columns: []string{"ID"}}, true},
		{"case and space insensitive", Options{Columns: []string{" id ", "NAME"}}, true},
		{"one column outside the set", Options{Columns: []string{"ID", "Status"}}, false},
		{"sort key outside the set", Options{Columns: []string{"ID"}, SortColumns: []string{"Status"}}, false},
		{"sort key inside the set", Options{Columns: []string{"ID"}, SortColumns: []string{"Name"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.o.ColumnsWithin("ID", "Name"); got != tc.want {
				t.Errorf("ColumnsWithin(ID, Name) = %v, want %v", got, tc.want)
			}
		})
	}
}
