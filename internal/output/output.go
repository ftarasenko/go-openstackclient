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
	"sort"
	"strings"

	"github.com/spf13/pflag"
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

// Options holds the formatting flags shared by all commands. It is registered
// once on the root command's persistent flags and threaded into every command.
type Options struct {
	Format  string
	Columns []string
}

// AddFlags registers -f/--format and -c/--column on the given flag set.
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVarP(&o.Format, "format", "f", FormatTable,
		fmt.Sprintf("output format, one of: %s", strings.Join(allFormats, ", ")))
	fs.StringArrayVarP(&o.Columns, "column", "c", nil,
		"specify the column(s) to include; can be repeated (default: all)")
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
		return writeTable(w, cols, rows)
	}
}

// WriteSingle renders a single resource as a Field/Value view (e.g. "node show").
func (o *Options) WriteSingle(w io.Writer, fields []string, values []any) error {
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
		return writeTable(w, []string{"Field", "Value"}, rows)
	}
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
// scripting (`-f value`).
func writeValue(w io.Writer, rows [][]any) error {
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, v := range r {
			cells[i] = cell(v)
		}
		if _, err := fmt.Fprintln(w, strings.Join(cells, "\t")); err != nil {
			return fmt.Errorf("writing value output: %w", err)
		}
	}
	return nil
}

func writeTable(w io.Writer, cols []string, rows [][]any) error {
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len(c)
	}
	strRows := make([][]string, len(rows))
	for ri, r := range rows {
		sr := make([]string, len(cols))
		for ci := range cols {
			var v any
			if ci < len(r) {
				v = r[ci]
			}
			s := cell(v)
			sr[ci] = s
			if len(s) > widths[ci] {
				widths[ci] = len(s)
			}
		}
		strRows[ri] = sr
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
	line := func(cells []string) error {
		var b strings.Builder
		b.WriteByte('|')
		for i, c := range cells {
			fmt.Fprintf(&b, " %-*s |", widths[i], c)
		}
		_, err := fmt.Fprintln(w, b.String())
		return err
	}

	if err := border(); err != nil {
		return err
	}
	if err := line(cols); err != nil {
		return err
	}
	if err := border(); err != nil {
		return err
	}
	for _, sr := range strRows {
		if err := line(sr); err != nil {
			return err
		}
	}
	return border()
}

// cell renders a single value for text-based output. Complex values (maps,
// slices) are rendered as compact JSON so table/csv/value output stays on one
// line, matching OSC's behavior for structured fields.
func cell(v any) string {
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
