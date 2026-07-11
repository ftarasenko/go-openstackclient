package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
