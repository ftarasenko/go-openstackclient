// Package output provides the shared result-formatting layer used by every koc
// command. A single formatter renders both list results (a header row plus data
// rows) and single-resource results (a two-column Field/Value view), so that the
// -f/--format and -c/--column flags behave identically across the whole CLI.
//
// Supported formats mirror python-openstackclient:
//
//	table  human-readable ASCII table (default)
//	json   JSON (array for lists, object for a single resource)
//	yaml   YAML
//	value  plain, tab-separated values, no headers (for scripting)
//	csv    RFC 4180 CSV with a header row
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/pflag"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// Format enumerates the supported output formats.
const (
	FormatTable = "table"
	FormatJSON  = "json"
	FormatYAML  = "yaml"
	FormatValue = "value"
	FormatCSV   = "csv"
)

var allFormats = []string{FormatTable, FormatJSON, FormatYAML, FormatValue, FormatCSV}

// maxTableCell caps how many runes a single table cell may hold before the
// table formatter elides it to a "<N bytes; …>" placeholder. Oversized opaque
// blobs (base64 user_data, cert bundles) would otherwise dominate the table and,
// pre-cap, blew a single `server show` up to >1 MB. Machine formats (json/yaml/
// value/csv) never elide, and an explicit `-c <column>` selection disables
// elision for the chosen columns, so the full value is always reachable.
const maxTableCell = 1024

// Options holds the formatting flags shared by all commands. It is registered
// once on the root command's persistent flags and threaded into every command.
type Options struct {
	Format   string
	Columns  []string
	MaxWidth int  // --max-width: hard cap on table width; 0 = auto (fit to TTY)
	FitWidth bool // --fit-width: fit the table to the display width even when piped
}

// AddFlags registers -f/--format and -c/--column on the given flag set.
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVarP(&o.Format, "format", "f", FormatTable,
		fmt.Sprintf("output format, one of: %s", strings.Join(allFormats, ", ")))
	fs.StringArrayVarP(&o.Columns, "column", "c", nil,
		"specify the column(s) to include; can be repeated (default: all)")
	fs.IntVar(&o.MaxWidth, "max-width", 0,
		"maximum display width for table output; 0 fits to the terminal (or is unbounded when piped)")
	fs.BoolVar(&o.FitWidth, "fit-width", false,
		"fit table output to the display width even when not a terminal")
}

// Validate checks that the requested format is supported.
func (o *Options) Validate() error {
	for _, f := range allFormats {
		if o.Format == f {
			return nil
		}
	}
	return fmt.Errorf("invalid output format %q: must be one of %s", o.Format, strings.Join(allFormats, ", "))
}

// Table is a generic tabular result: a set of column headers and the data rows.
type Table struct {
	Columns []string
	Rows    [][]any
}

// WriteList renders a multi-row result (e.g. "node list") in the selected format.
func (o *Options) WriteList(w io.Writer, t Table) error {
	if err := o.validateColumns(t.Columns); err != nil {
		return err
	}
	cols, idx := o.selectColumns(t.Columns)
	rows := make([][]any, len(t.Rows))
	for i, r := range t.Rows {
		row := make([]any, len(idx))
		for j, c := range idx {
			if c < len(r) {
				row[j] = r[c]
			}
		}
		rows[i] = row
	}

	switch o.Format {
	case FormatJSON:
		return writeJSON(w, rowsToMaps(cols, rows))
	case FormatYAML:
		return writeYAML(w, rowsToMaps(cols, rows))
	case FormatCSV:
		return writeCSV(w, cols, rows)
	case FormatValue:
		return writeValue(w, rows)
	default:
		return writeTable(w, cols, rows, o.fitWidth(w), 8, len(o.Columns) == 0)
	}
}

// WriteSingle renders a single resource as a Field/Value view (e.g. "node show").
func (o *Options) WriteSingle(w io.Writer, fields []string, values []any) error {
	if len(values) != len(fields) {
		return fmt.Errorf("internal error: %d field(s) but %d value(s)", len(fields), len(values))
	}
	if err := o.validateColumns(fields); err != nil {
		return err
	}
	// Column selection filters which fields are shown.
	if len(o.Columns) > 0 {
		var fFields []string
		var fValues []any
		for i, f := range fields {
			if o.columnSelected(f) {
				fFields = append(fFields, f)
				if i < len(values) {
					fValues = append(fValues, values[i])
				} else {
					fValues = append(fValues, nil)
				}
			}
		}
		fields, values = fFields, fValues
	}

	switch o.Format {
	case FormatJSON:
		m := make(map[string]any, len(fields))
		for i, f := range fields {
			m[f] = values[i]
		}
		return writeJSON(w, m)
	case FormatYAML:
		m := make(map[string]any, len(fields))
		for i, f := range fields {
			m[f] = values[i]
		}
		return writeYAML(w, m)
	case FormatCSV:
		rows := make([][]any, len(fields))
		for i, f := range fields {
			rows[i] = []any{f, values[i]}
		}
		return writeCSV(w, []string{"Field", "Value"}, rows)
	case FormatValue:
		rows := make([][]any, len(values))
		for i := range values {
			rows[i] = []any{values[i]}
		}
		return writeValue(w, rows)
	default:
		rows := make([][]any, len(fields))
		for i, f := range fields {
			rows[i] = []any{f, values[i]}
		}
		return writeTable(w, []string{"Field", "Value"}, rows, o.fitWidth(w), 16, len(o.Columns) == 0)
	}
}

// validateColumns errors when a requested -c/--column name matches none of the
// available headers (case-insensitively), matching OSC, which rejects unknown
// columns rather than silently dropping them.
func (o *Options) validateColumns(all []string) error {
	if len(o.Columns) == 0 {
		return nil
	}
	var unknown []string
	for _, want := range o.Columns {
		found := false
		for _, have := range all {
			if strings.EqualFold(strings.TrimSpace(want), have) {
				found = true
				break
			}
		}
		if !found {
			unknown = append(unknown, want)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown column(s): %s (available: %s)",
			strings.Join(unknown, ", "), strings.Join(all, ", "))
	}
	return nil
}

// selectColumns returns the effective column headers and the indices into the
// source rows, honoring -c/--column selection (case-insensitive) while
// preserving the user-requested order.
func (o *Options) selectColumns(all []string) (cols []string, idx []int) {
	if len(o.Columns) == 0 {
		idx = make([]int, len(all))
		for i := range all {
			idx[i] = i
		}
		return all, idx
	}
	for _, want := range o.Columns {
		for i, have := range all {
			if strings.EqualFold(strings.TrimSpace(want), have) {
				cols = append(cols, have)
				idx = append(idx, i)
				break
			}
		}
	}
	return cols, idx
}

func (o *Options) columnSelected(field string) bool {
	for _, want := range o.Columns {
		if strings.EqualFold(strings.TrimSpace(want), field) {
			return true
		}
	}
	return false
}

func rowsToMaps(cols []string, rows [][]any) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		m := make(map[string]any, len(cols))
		for j, c := range cols {
			if j < len(r) {
				m[c] = r[j]
			}
		}
		out[i] = m
	}
	return out
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}
	return nil
}

func writeYAML(w io.Writer, v any) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encoding YAML: %w", err)
	}
	return enc.Close()
}

func writeCSV(w io.Writer, cols []string, rows [][]any) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(cols); err != nil {
		return fmt.Errorf("writing CSV header: %w", err)
	}
	for _, r := range rows {
		rec := make([]string, len(r))
		for i, v := range r {
			rec[i] = cell(v)
		}
		if err := cw.Write(rec); err != nil {
			return fmt.Errorf("writing CSV row: %w", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("flushing CSV: %w", err)
	}
	return nil
}

// writeValue emits tab-separated values with no header, one row per line, for
// scripting (`-f value`). Because the record and field separators are newline
// and tab, any tab/newline embedded in a value is collapsed to a space so the
// output stays one-record-per-line and column counts are stable.
func writeValue(w io.Writer, rows [][]any) error {
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, v := range r {
			cells[i] = oneLine(cell(v))
		}
		if _, err := fmt.Fprintln(w, strings.Join(cells, "\t")); err != nil {
			return fmt.Errorf("writing value output: %w", err)
		}
	}
	return nil
}

// writeTable renders an ASCII table. When fitWidth > 0 the column widths are
// shrunk (and over-long cells wrapped across physical lines) so the table fits
// within fitWidth, mirroring python-openstackclient/cliff; fitWidth == 0 leaves
// the table unbounded (piped output). When elide is true, cells longer than
// maxTableCell are replaced by a placeholder so a single opaque blob cannot
// dominate the table. minWidth is the floor a wrapped column is shrunk to.
func writeTable(w io.Writer, cols []string, rows [][]any, fitWidth, minWidth int, elide bool) error {
	// Column widths are measured in runes (not bytes) so multi-byte content
	// (e.g. Cyrillic names on a KeyStack cloud) aligns with the ASCII border,
	// matching fmt's rune-based %-*s padding used below.
	natural := make([]int, len(cols))
	for i, c := range cols {
		natural[i] = utf8.RuneCountInString(c)
	}
	strRows := make([][]string, len(rows))
	for ri, r := range rows {
		sr := make([]string, len(cols))
		for ci := range cols {
			var v any
			if ci < len(r) {
				v = r[ci]
			}
			s := oneLine(cell(v))
			if elide {
				s = elideCell(s)
			}
			sr[ci] = s
			if n := utf8.RuneCountInString(s); n > natural[ci] {
				natural[ci] = n
			}
		}
		strRows[ri] = sr
	}

	assigned := natural
	if fitWidth > 0 {
		assigned = shrinkWidths(natural, fitWidth, minWidth)
	}

	// Wrap the header and every cell to its assigned width, then set the final
	// column widths to the widest wrapped line so the borders stay tight.
	widths := make([]int, len(cols))
	wrapHeader := make([][]string, len(cols))
	for i, c := range cols {
		wrapHeader[i] = wrapText(c, assigned[i])
		widths[i] = maxLineWidth(wrapHeader[i])
	}
	wrapRows := make([][][]string, len(strRows))
	for ri, sr := range strRows {
		wr := make([][]string, len(cols))
		for ci := range cols {
			wr[ci] = wrapText(sr[ci], assigned[ci])
			if n := maxLineWidth(wr[ci]); n > widths[ci] {
				widths[ci] = n
			}
		}
		wrapRows[ri] = wr
	}

	border := func() error {
		var b strings.Builder
		b.WriteByte('+')
		for _, wdt := range widths {
			b.WriteString(strings.Repeat("-", wdt+2))
			b.WriteByte('+')
		}
		_, err := fmt.Fprintln(w, b.String())
		return err
	}
	// row prints a logical row whose cells may each span several physical lines,
	// padding shorter cells with blanks so every column stays aligned.
	row := func(cells [][]string) error {
		h := 1
		for _, c := range cells {
			if len(c) > h {
				h = len(c)
			}
		}
		for li := 0; li < h; li++ {
			var b strings.Builder
			b.WriteByte('|')
			for ci := range cells {
				var s string
				if li < len(cells[ci]) {
					s = cells[ci][li]
				}
				fmt.Fprintf(&b, " %-*s |", widths[ci], s)
			}
			if _, err := fmt.Fprintln(w, b.String()); err != nil {
				return err
			}
		}
		return nil
	}

	if err := border(); err != nil {
		return err
	}
	if err := row(wrapHeader); err != nil {
		return err
	}
	if err := border(); err != nil {
		return err
	}
	for _, wr := range wrapRows {
		if err := row(wr); err != nil {
			return err
		}
	}
	return border()
}

// fitWidth resolves the width the table should be fitted to: an explicit
// --max-width wins; otherwise a TTY is fitted to its size (or --fit-width forces
// fitting when piped), falling back to $COLUMNS then 80. Piped output with no
// explicit request returns 0 (unbounded), matching OSC.
func (o *Options) fitWidth(w io.Writer) int {
	if o.MaxWidth > 0 {
		return o.MaxWidth
	}
	isTTY, width := terminalSize(w)
	if !o.FitWidth && !isTTY {
		return 0
	}
	if width <= 0 {
		width = columnsEnv()
	}
	if width <= 0 {
		width = 80
	}
	return width
}

// terminalSize reports whether w is a terminal and, if so, its width in columns.
func terminalSize(w io.Writer) (isTTY bool, width int) {
	f, ok := w.(*os.File)
	if !ok {
		return false, 0
	}
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return false, 0
	}
	if wd, _, err := term.GetSize(fd); err == nil && wd > 0 {
		return true, wd
	}
	return true, 0
}

func columnsEnv() int {
	if c := os.Getenv("COLUMNS"); c != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(c)); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// shrinkWidths reduces column widths so the rendered table fits termWidth,
// following cliff's algorithm: columns already narrower than the per-column
// optimum are left untouched, and the surplus width is divided among the
// wider ("shrinkable") columns, each floored at minWidth. If the table already
// fits, the natural widths are returned unchanged.
func shrinkWidths(natural []int, termWidth, minWidth int) []int {
	ncols := len(natural)
	total := 1
	for _, wd := range natural {
		total += wd + 3
	}
	if total <= termWidth || ncols == 0 {
		return natural
	}
	usable := termWidth - 1 - 3*ncols
	if usable < 0 {
		usable = 0
	}
	optimal := usable / ncols
	shrinkRemaining := usable
	var shrink []int
	for i, wd := range natural {
		if wd <= optimal {
			shrinkRemaining -= wd
		} else {
			shrink = append(shrink, i)
		}
	}
	if len(shrink) == 0 {
		return natural
	}
	out := make([]int, ncols)
	copy(out, natural)
	shrinkTo := shrinkRemaining / len(shrink)
	for _, i := range shrink[:len(shrink)-1] {
		out[i] = max(minWidth, shrinkTo)
		shrinkRemaining -= shrinkTo
	}
	last := shrink[len(shrink)-1]
	out[last] = max(minWidth, shrinkRemaining)
	return out
}

// elideCell replaces a cell longer than maxTableCell with a placeholder naming
// its size and how to see the full value.
func elideCell(s string) string {
	if utf8.RuneCountInString(s) <= maxTableCell {
		return s
	}
	return fmt.Sprintf("<%d bytes; use -f yaml or -c <column> to show full value>", len(s))
}

func maxLineWidth(lines []string) int {
	n := 0
	for _, ln := range lines {
		if w := utf8.RuneCountInString(ln); w > n {
			n = w
		}
	}
	return n
}

// wrapText breaks s into physical lines no wider than width (in runes),
// preferring to wrap at spaces but hard-breaking tokens longer than width.
// A width <= 0, or a string already within width, is returned as a single line.
func wrapText(s string, width int) []string {
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return []string{s}
	}
	var lines []string
	var cur []rune
	flush := func() {
		lines = append(lines, string(cur))
		cur = cur[:0]
	}
	for _, word := range strings.Split(s, " ") {
		wr := []rune(word)
		for len(wr) > width {
			if len(cur) > 0 {
				flush()
			}
			lines = append(lines, string(wr[:width]))
			wr = wr[width:]
		}
		switch {
		case len(cur) == 0:
			cur = append(cur, wr...)
		case len(cur)+1+len(wr) <= width:
			cur = append(cur, ' ')
			cur = append(cur, wr...)
		default:
			flush()
			cur = append(cur, wr...)
		}
	}
	if len(cur) > 0 || len(lines) == 0 {
		lines = append(lines, string(cur))
	}
	return lines
}

// ansiRe matches ANSI/VT escape sequences: CSI (ESC [ …), OSC (ESC ] … BEL/ST),
// and the two-byte ESC-prefixed forms. Server-supplied strings are run through
// this so a hostile or buggy endpoint cannot embed cursor moves, screen clears,
// or color codes in a resource name and rewrite the operator's terminal.
var ansiRe = regexp.MustCompile("\x1b(?:\\[[0-9;?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|[@-Z\\\\-_])")

// stripControl removes ANSI escapes and C0/C1 control characters from
// text-format output. Tab and newline are preserved here (they carry meaning in
// CSV, and are collapsed to spaces by oneLine for table/value output); every
// other control rune — including the terminal-hijacking ESC, CR, and BEL — is
// dropped. JSON/YAML output does not pass through here; their encoders escape
// control characters safely.
func stripControl(s string) string {
	if strings.IndexFunc(s, isDangerousControl) < 0 {
		return s
	}
	s = ansiRe.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' {
			return r
		}
		if isDangerousControl(r) {
			return -1
		}
		return r
	}, s)
}

func isDangerousControl(r rune) bool {
	if r == '\t' || r == '\n' {
		return false
	}
	return r < 0x20 || (r >= 0x7f && r < 0xa0)
}

// oneLine collapses tab/newline/carriage-return to spaces so a value renders on
// a single physical row in the table and value formats.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\t\n\r") {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

// cell renders a single value for text-based output, with control characters
// and ANSI escapes stripped (see stripControl). Complex values (maps, slices)
// are rendered as compact JSON so table/csv/value output stays on one line,
// matching OSC's behavior for structured fields.
func cell(v any) string {
	return stripControl(cellRaw(v))
}

func cellRaw(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprintf("%v", t)
	case []string:
		return strings.Join(t, ", ")
	case map[string]string:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s='%s'", k, t[k]))
		}
		return strings.Join(parts, ", ")
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}
