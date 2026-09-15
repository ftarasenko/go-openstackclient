package watch

import (
	"reflect"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// frame runs one frame's worth of table through the differ.
func (d *Differ) frame(cols []string, rows [][]any) ([][]any, [][]output.CellStyle) {
	d.BeginFrame()
	out, styles := d.Highlight(cols, rows)
	d.Commit()
	return out, styles
}

var listCols = []string{"ID", "Name", "Status"}

func row(id, name, status string) []any { return []any{id, name, status} }

func TestDifferFirstFrameHighlightsNothing(t *testing.T) {
	d := NewDiffer(true)
	_, styles := d.frame(listCols, [][]any{row("1", "web-01", "BUILD")})
	if styles != nil {
		t.Errorf("the first frame returned styles %v; there is nothing to compare it against", styles)
	}
	if d.Changed() {
		t.Error("the first frame reported a change")
	}
	if d.Rows() != 1 {
		t.Errorf("counted %d rows, want 1", d.Rows())
	}
}

func TestDifferHighlightsOnlyTheChangedCell(t *testing.T) {
	d := NewDiffer(true)
	d.frame(listCols, [][]any{row("1", "web-01", "BUILD")})
	_, styles := d.frame(listCols, [][]any{row("1", "web-01", "ACTIVE")})

	want := [][]output.CellStyle{{output.CellPlain, output.CellPlain, output.CellChanged}}
	if !reflect.DeepEqual(styles, want) {
		t.Errorf("styles %v, want %v — only the cell that moved may light up", styles, want)
	}
	if !d.Changed() {
		t.Error("the frame was not reported as changed")
	}
}

func TestDifferSurvivesReordering(t *testing.T) {
	d := NewDiffer(true)
	d.frame(listCols, [][]any{
		row("1", "web-01", "ACTIVE"),
		row("2", "web-02", "ACTIVE"),
		row("3", "web-03", "ACTIVE"),
	})
	// The same three servers, in a different order, with one status moved. This
	// is the case `watch -d` cannot handle: diffing by character position lights
	// up every row below the first that moved.
	_, styles := d.frame(listCols, [][]any{
		row("3", "web-03", "ACTIVE"),
		row("1", "web-01", "ERROR"),
		row("2", "web-02", "ACTIVE"),
	})

	want := [][]output.CellStyle{
		{output.CellPlain, output.CellPlain, output.CellPlain},
		{output.CellPlain, output.CellPlain, output.CellChanged},
		{output.CellPlain, output.CellPlain, output.CellPlain},
	}
	if !reflect.DeepEqual(styles, want) {
		t.Errorf("styles %v, want %v — rows are matched by ID, not by position", styles, want)
	}
}

func TestDifferMarksAddedRows(t *testing.T) {
	d := NewDiffer(true)
	d.frame(listCols, [][]any{row("1", "web-01", "ACTIVE")})
	frame, styles := d.frame(listCols, [][]any{
		row("1", "web-01", "ACTIVE"),
		row("2", "web-02", "BUILD"),
	})

	if len(frame) != 2 {
		t.Fatalf("rendered %d rows, want 2", len(frame))
	}
	if got := styles[1]; !allStyle(got, output.CellAdded) {
		t.Errorf("the new row is styled %v, want every cell marked as added", got)
	}
	if allStyle(styles[0], output.CellAdded) {
		t.Error("the unchanged row was marked as added")
	}
}

func TestDifferHoldsDepartedRowsForOneFrame(t *testing.T) {
	d := NewDiffer(true)
	d.frame(listCols, [][]any{
		row("1", "web-01", "ACTIVE"),
		row("2", "web-02", "ACTIVE"),
	})

	// web-01 is gone. It is held on screen, dimmed, so a server disappearing is
	// something the operator sees rather than something they must have been
	// looking at.
	frame, styles := d.frame(listCols, [][]any{row("2", "web-02", "ACTIVE")})
	if len(frame) != 2 {
		t.Fatalf("rendered %d rows, want 2 (the survivor plus the ghost)", len(frame))
	}
	if frame[1][1] != "web-01" {
		t.Errorf("the held row is %v, want web-01", frame[1])
	}
	if !allStyle(styles[1], output.CellRemoved) {
		t.Errorf("the held row is styled %v, want every cell dimmed", styles[1])
	}

	// And on the next frame it is gone for good — the ghost must not become
	// part of the baseline, or it would be held forever.
	frame, _ = d.frame(listCols, [][]any{row("2", "web-02", "ACTIVE")})
	if len(frame) != 1 {
		t.Errorf("rendered %d rows on the following frame, want 1", len(frame))
	}
}

func TestDifferRollbackKeepsTheBaseline(t *testing.T) {
	d := NewDiffer(true)
	d.frame(listCols, [][]any{row("1", "web-01", "ACTIVE")})

	// A refresh that fails must not become the baseline: the next successful
	// one would otherwise light up entirely, reporting a change that never
	// happened.
	d.BeginFrame()
	d.Rollback()

	d.BeginFrame()
	_, styles := d.Highlight(listCols, [][]any{row("1", "web-01", "ACTIVE")})
	d.Commit()
	if styles != nil && !allStyle(styles[0], output.CellPlain) {
		t.Errorf("styles %v after a rolled-back frame, want nothing highlighted", styles)
	}
	if d.Changed() {
		t.Error("a rolled-back frame was counted as a change")
	}
}

func TestDifferCountsRowsWithHighlightingOff(t *testing.T) {
	d := NewDiffer(false)
	_, styles := d.frame(listCols, [][]any{
		row("1", "web-01", "ACTIVE"),
		row("2", "web-02", "ACTIVE"),
	})
	if styles != nil {
		t.Errorf("styles %v with highlighting off, want none", styles)
	}
	if d.Rows() != 2 {
		t.Errorf("counted %d rows, want 2 — the status line needs the count either way", d.Rows())
	}

	// --watch-until-change still has to work with --watch-diff off.
	d.frame(listCols, [][]any{row("1", "web-01", "ERROR")})
	if !d.Changed() {
		t.Error("a change went unnoticed with highlighting off")
	}
}

func TestDifferFallsBackFromIDToName(t *testing.T) {
	cols := []string{"Name", "Status"}
	d := NewDiffer(true)
	d.frame(cols, [][]any{{"web-01", "BUILD"}, {"web-02", "ACTIVE"}})
	_, styles := d.frame(cols, [][]any{{"web-02", "ACTIVE"}, {"web-01", "ACTIVE"}})

	// Reordered, and web-01's status moved. Matching on Name is what keeps the
	// second row from lighting up entirely.
	want := [][]output.CellStyle{
		{output.CellPlain, output.CellPlain},
		{output.CellPlain, output.CellChanged},
	}
	if !reflect.DeepEqual(styles, want) {
		t.Errorf("styles %v, want %v", styles, want)
	}
}

func TestDifferUsesTheFieldColumnForShowOutput(t *testing.T) {
	cols := []string{"Field", "Value"}
	d := NewDiffer(true)
	d.frame(cols, [][]any{{"status", "BUILD"}, {"name", "web-01"}})
	_, styles := d.frame(cols, [][]any{{"name", "web-01"}, {"status", "ACTIVE"}})

	want := [][]output.CellStyle{
		{output.CellPlain, output.CellPlain},
		{output.CellPlain, output.CellChanged},
	}
	if !reflect.DeepEqual(styles, want) {
		t.Errorf("styles %v, want %v — a Field/Value view is keyed on the field name", styles, want)
	}
}

func TestDifferFallsBackToPositionWithoutAnIdentity(t *testing.T) {
	cols := []string{"Metric", "Value"}
	d := NewDiffer(true)
	d.frame(cols, [][]any{{"cpu", "10"}, {"ram", "20"}})
	_, styles := d.frame(cols, [][]any{{"cpu", "11"}, {"ram", "20"}})

	want := [][]output.CellStyle{
		{output.CellPlain, output.CellChanged},
		{output.CellPlain, output.CellPlain},
	}
	if !reflect.DeepEqual(styles, want) {
		t.Errorf("styles %v, want %v", styles, want)
	}
}

func TestDifferResetsWhenTheColumnsChange(t *testing.T) {
	d := NewDiffer(true)
	d.frame(listCols, [][]any{row("1", "web-01", "ACTIVE")})
	// A different table shape (the 'd' key does not do this, but a command whose
	// optional columns depend on the data can). There is nothing to compare, so
	// nothing is highlighted and nothing is reported as changed.
	_, styles := d.frame([]string{"ID", "Name"}, [][]any{{"1", "web-01"}})
	if styles != nil {
		t.Errorf("styles %v across a column change, want none", styles)
	}
	if d.Changed() {
		t.Error("a column change was reported as a data change")
	}
}

func TestDifferKeepsSeveralTablesApart(t *testing.T) {
	d := NewDiffer(true)
	d.BeginFrame()
	d.Highlight(listCols, [][]any{row("1", "a", "ACTIVE")})
	d.Highlight(listCols, [][]any{row("2", "b", "ACTIVE")})
	d.Commit()
	if d.Tables() != 2 || d.Rows() != 2 {
		t.Fatalf("tables=%d rows=%d, want 2 and 2", d.Tables(), d.Rows())
	}

	d.BeginFrame()
	_, first := d.Highlight(listCols, [][]any{row("1", "a", "ACTIVE")})
	_, second := d.Highlight(listCols, [][]any{row("2", "b", "ERROR")})
	d.Commit()

	// Each table is compared against the one that rendered in the same position
	// last frame, so the second table's change cannot be attributed to the first.
	if first != nil && !allStyle(first[0], output.CellPlain) {
		t.Errorf("the first table lit up: %v", first)
	}
	if second[0][2] != output.CellChanged {
		t.Errorf("the second table's change was missed: %v", second)
	}
}

func TestDifferDisambiguatesDuplicateIdentities(t *testing.T) {
	cols := []string{"Name", "Status"}
	d := NewDiffer(true)
	// Two rows with the same name: without a fallback they would collapse into
	// one entry and mis-attribute both.
	d.frame(cols, [][]any{{"dup", "A"}, {"dup", "B"}})
	_, styles := d.frame(cols, [][]any{{"dup", "A"}, {"dup", "C"}})

	if styles[0][1] != output.CellPlain {
		t.Errorf("the unchanged duplicate lit up: %v", styles[0])
	}
	if styles[1][1] != output.CellChanged {
		t.Errorf("the changed duplicate did not light up: %v", styles[1])
	}
}

func allStyle(styles []output.CellStyle, want output.CellStyle) bool {
	if len(styles) == 0 {
		return false
	}
	for _, s := range styles {
		if s != want {
			return false
		}
	}
	return true
}
