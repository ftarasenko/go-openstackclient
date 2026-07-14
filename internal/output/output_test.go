package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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

func TestWriteValue_CollapsesEmbeddedTabAndNewline(t *testing.T) {
	// A single value containing a tab and newline must not add columns or rows.
	o := &Options{Format: FormatValue}
	var buf bytes.Buffer
	tbl := Table{Columns: []string{"A", "B"}, Rows: [][]any{{"x\ty\nz", "end"}}}
	if err := o.WriteList(&buf, tbl); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("embedded newline leaked extra rows: %q", buf.String())
	}
	if got := lines[0]; got != "x y z\tend" {
		t.Errorf("value row = %q, want tab/newline collapsed to spaces", got)
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
