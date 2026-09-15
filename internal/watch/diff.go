package watch

import (
	"fmt"
	"strings"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Differ reconciles one frame's tables against the previous frame's and reports
// what changed, as the per-cell styles internal/output paints.
//
// Rows are matched by *identity* — the ID column when the table has one, else
// Name, else the row's position — rather than by screen position, which is the
// one thing `watch -d` cannot do. watch(1) diffs by character offset, so the
// moment a server is created, deleted or comes back in a different order every
// row below it lights up; in a live fleet that is all of them. Matching on
// identity means `-c Name -c Status` shows the single cell that went
// BUILD → ACTIVE, which is the whole reason to watch a list.
//
// Differ also counts the rows in a frame whether or not highlighting is on, so
// the status line can report them; that is why the loop installs it even with
// --watch-diff off.
type Differ struct {
	enabled bool

	prev []tableState // the previous frame's tables, in the order they rendered
	cur  []tableState // the frame being rendered
	idx  int          // how many tables this frame has rendered so far

	rows    int
	tables  int
	changed bool
}

// tableState is one table as the previous frame left it: the row identities in
// render order and the rendered cells behind each one.
type tableState struct {
	signature string
	order     []string
	cells     map[string][]string
}

// NewDiffer returns a Differ. When enabled is false it still counts rows but
// returns no styles, which is what --watch-diff off and every non-terminal
// frame want.
func NewDiffer(enabled bool) *Differ { return &Differ{enabled: enabled} }

// Enabled reports whether highlighting is on; SetEnabled toggles it, which is
// what the 'd' key does mid-flight.
func (d *Differ) Enabled() bool { return d.enabled }

// SetEnabled turns highlighting on or off.
func (d *Differ) SetEnabled(on bool) { d.enabled = on }

// Rows reports how many rows the last committed frame held, across every table
// in it.
func (d *Differ) Rows() int { return d.rows }

// Changed reports whether the last committed frame differed from the one before
// it — a row added, removed, or any cell with a new value. It is what
// --watch-until-change exits on.
func (d *Differ) Changed() bool { return d.changed }

// BeginFrame starts a frame. Highlight is then called once per table the
// command renders, and the frame ends in exactly one of Commit or Rollback.
func (d *Differ) BeginFrame() {
	d.cur = nil
	d.idx = 0
	d.changed = false
}

// Commit makes the frame just rendered the baseline for the next one.
func (d *Differ) Commit() {
	d.prev = d.cur
	d.rows = 0
	d.tables = len(d.cur)
	for _, t := range d.cur {
		d.rows += len(t.order)
	}
}

// Tables reports how many tables the last committed frame held. Zero means the
// frame never reached the output layer's table path — -f json, -f yaml, or a
// command that writes its own text — so nothing here can speak for it.
func (d *Differ) Tables() int { return d.tables }

// Rollback discards a frame that failed to render, so the baseline stays the
// last frame the operator actually saw. Without it a transient error would make
// the next successful refresh light up entirely, reporting a change that never
// happened.
func (d *Differ) Rollback() { d.cur = nil }

// Highlight implements output.Highlighter. It is called by the output layer
// once per table, after --sort-column ordering and -c/--column selection and
// after the values have been canonicalised, but before anything is rendered.
func (d *Differ) Highlight(cols []string, rows [][]any) ([][]any, [][]output.CellStyle) {
	ordinal := d.idx
	d.idx++

	state := snapshot(cols, rows)
	d.cur = append(d.cur, state)

	prev, ok := d.previous(ordinal, state.signature)
	if !ok {
		// Either the first frame, or the command rendered a differently shaped
		// table than last time. Nothing to compare against, so nothing is
		// "changed" — a first frame that lit up every cell would be noise.
		return rows, nil
	}
	if !d.enabled {
		d.changed = d.changed || differs(prev, state)
		return rows, nil
	}
	return d.decorate(cols, rows, prev, state)
}

// previous returns the same table from the previous frame, if it is there and
// still has the same columns.
func (d *Differ) previous(ordinal int, signature string) (tableState, bool) {
	if ordinal >= len(d.prev) {
		return tableState{}, false
	}
	prev := d.prev[ordinal]
	if prev.signature != signature {
		return tableState{}, false
	}
	return prev, true
}

// decorate builds the style matrix and appends the rows that have just left the
// result, held on screen for one frame so a server disappearing is something
// the operator sees rather than something they have to have been looking at.
func (d *Differ) decorate(cols []string, rows [][]any, prev, state tableState) ([][]any, [][]output.CellStyle) {
	frame := rows
	// Sized for the rows that are actually here. The ghost rows appended below
	// grow it, which is the rare case and is what append is for — and the
	// arithmetic a guessed capacity needs is a size computation CodeQL is right
	// to object to, whatever the real bound on len() happens to be.
	styles := make([][]output.CellStyle, 0, len(rows))

	for _, key := range state.order {
		old, existed := prev.cells[key]
		if !existed {
			styles = append(styles, uniform(len(cols), output.CellAdded))
			d.changed = true
			continue
		}
		row, moved := changedCells(cols, state.cells[key], old)
		styles = append(styles, row)
		d.changed = d.changed || moved
	}

	for _, key := range prev.order {
		if _, still := state.cells[key]; still {
			continue
		}
		frame = append(frame, ghostRow(cols, prev.cells[key]))
		styles = append(styles, uniform(len(cols), output.CellRemoved))
		d.changed = true
	}
	return frame, styles
}

// changedCells marks the cells whose value differs from the previous frame.
func changedCells(cols []string, now, old []string) ([]output.CellStyle, bool) {
	styles := make([]output.CellStyle, len(cols))
	var changed bool
	for i := range cols {
		if at(now, i) != at(old, i) {
			styles[i] = output.CellChanged
			changed = true
		}
	}
	return styles, changed
}

// ghostRow turns a departed row's remembered cells back into a renderable row.
func ghostRow(cols []string, cells []string) []any {
	row := make([]any, len(cols))
	for i := range cols {
		row[i] = at(cells, i)
	}
	return row
}

func uniform(n int, s output.CellStyle) []output.CellStyle {
	styles := make([]output.CellStyle, n)
	for i := range styles {
		styles[i] = s
	}
	return styles
}

func at(cells []string, i int) string {
	if i < len(cells) {
		return cells[i]
	}
	return ""
}

// differs reports whether two snapshots of the same table hold different data.
// It is the cheap path taken when highlighting is off but --watch-until-change
// still has to know.
func differs(prev, cur tableState) bool {
	if len(prev.order) != len(cur.order) {
		return true
	}
	for key, cells := range cur.cells {
		old, ok := prev.cells[key]
		if !ok || !equalCells(cells, old) {
			return true
		}
	}
	return false
}

func equalCells(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// snapshot records a table the way the differ compares it: a stable identity per
// row and the rendered text of every cell.
func snapshot(cols []string, rows [][]any) tableState {
	key := keyColumn(cols)
	state := tableState{
		signature: strings.Join(cols, "\x00"),
		order:     make([]string, 0, len(rows)),
		cells:     make(map[string][]string, len(rows)),
	}
	for i, row := range rows {
		cells := make([]string, len(cols))
		for j := range cols {
			if j < len(row) {
				cells[j] = render(row[j])
			}
		}
		id := rowKey(cells, key, i)
		// A duplicate identity (two rows with the same name, or an ID column
		// the command left empty) would otherwise collapse into one entry and
		// mis-attribute both. Fall back to position for the duplicate.
		if _, clash := state.cells[id]; clash {
			id = positionKey(i)
		}
		state.order = append(state.order, id)
		state.cells[id] = cells
	}
	return state
}

// keyColumn picks the column that identifies a row: ID if the table has one,
// else Name, else none (rows are then matched by position, as watch(1) does).
func keyColumn(cols []string) int {
	for _, want := range []string{"ID", "Name"} {
		for i, c := range cols {
			if strings.EqualFold(c, want) {
				return i
			}
		}
	}
	// `koc <noun> show` renders a Field/Value table, where the field name is
	// the identity. It has no ID column, so this is the case that catches it.
	if len(cols) == 2 && strings.EqualFold(cols[0], "Field") {
		return 0
	}
	return -1
}

func rowKey(cells []string, key, pos int) string {
	if key < 0 || key >= len(cells) || cells[key] == "" {
		return positionKey(pos)
	}
	return "k:" + cells[key]
}

func positionKey(pos int) string { return "#" + fmt.Sprint(pos) }

// render is how the differ spells a cell for comparison. It does not have to
// match the table's own rendering byte for byte — only to be stable from one
// frame to the next, which %v over the already-canonicalised value is.
func render(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
