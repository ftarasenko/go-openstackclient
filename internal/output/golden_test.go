package output

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Golden-file harness for the output layer.
//
// Every case below renders through the public entry points (WriteList /
// WriteSingle) and its exact bytes are compared with testdata/<name>.golden.
// strings.Contains assertions cannot see a reordered column, a dropped header, a
// changed border, a moved wrap point or a shifted elision boundary — the whole
// job of the layout engine (writeTable / shrinkWidths / wrapText / elideCell) —
// so these cases pin the rendering byte for byte.
//
// To regenerate every golden file after an intentional rendering change:
//
//	UPDATE_GOLDEN=1 go test ./internal/output/...
//
// then read the diff before committing it: a golden file that changed without an
// intended reason is the regression this harness exists to catch.
//
// Widths are always pinned with MaxWidth (or left unbounded) rather than
// inherited from a terminal, so the goldens do not depend on the environment.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run UPDATE_GOLDEN=1 go test ./internal/output/... to create it)", path, err)
	}
	if got == string(want) {
		return
	}

	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	t.Errorf("output does not match %s (%d lines, want %d)", path, len(gotLines), len(wantLines))
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		var g, w string
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Errorf("line %d:\n got %q\nwant %q", i+1, g, w)
		}
	}
}

// fixedTime is a stable timestamp so the goldens never move.
var fixedTime = time.Date(2026, 3, 14, 9, 26, 53, 589000000, time.UTC)

// goldenServers is a realistic list result: mixed types, a nil, a timestamp, a
// []string and a map, so the cell renderer's whole type switch is on show.
func goldenServers() Table {
	return Table{
		Columns: []string{"ID", "Name", "Status", "Networks", "Image", "Flavor", "Created At", "Metadata", "Size"},
		Rows: [][]any{
			{
				"11111111-1111-4111-8111-111111111111", "web-01", "ACTIVE",
				[]string{"private=192.0.2.11", "public=198.51.100.11"},
				"ubuntu-22.04", "m1.small", fixedTime,
				map[string]string{"role": "web", "env": "prod"}, 20,
			},
			{
				"22222222-2222-4222-8222-222222222222", "db-01", "SHUTOFF",
				[]string{"private=192.0.2.12"},
				nil, "m1.large", time.Time{},
				map[string]string{}, 100,
			},
			{
				"33333333-3333-4333-8333-333333333333", "batch-worker-with-a-long-name", "ERROR",
				[]string{}, "centos-stream-9", "c4.xlarge", fixedTime, nil, 9,
			},
		},
	}
}

// goldenWide carries multi-byte content: the engine measures widths in runes, so
// Cyrillic aligns while double-width CJK/emoji glyphs do not. The goldens pin
// today's behaviour either way.
func goldenWide() Table {
	return Table{
		Columns: []string{"Zone", "Название", "区域", "Note"},
		Rows: [][]any{
			{"az-1", "Зона доступности 1", "北京区域一", "ok"},
			{"az-2", "Зона-2", "東京", "флаг ✅"},
			{"az-3", "З", "上海自由贸易试验区", "-"},
		},
	}
}

// goldenMultiline has embedded newlines (a zonefile-shaped value), which the
// table renders as extra physical lines inside the row.
func goldenMultiline() Table {
	return Table{
		Columns: []string{"Name", "Records", "TTL"},
		Rows: [][]any{
			{"example.com.", "ns1.example.com.\nns2.example.com.\nns3.example.com.", 3600},
			{"sub.example.com.", "single.example.com.", 300},
		},
	}
}

func TestGolden_WriteList(t *testing.T) {
	t.Parallel()

	blob := strings.Repeat("QUJDREVG", 200) // 1600 runes: over the 1024 elision cap
	nearCap := strings.Repeat("x", maxTableCell)
	overCap := strings.Repeat("x", maxTableCell+1)

	tests := []struct {
		name  string
		opts  Options
		table Table
	}{
		// --- table: the layout engine ---------------------------------------
		{
			// Unbounded (piped) table: natural widths, nothing shrunk or wrapped.
			name:  "list_table_plain",
			opts:  Options{Format: FormatTable},
			table: goldenServers(),
		},
		{
			// MaxWidth 100 forces shrinkWidths to divide the surplus among the wide
			// columns and wrapText to break their cells.
			name:  "list_table_shrink_100",
			opts:  Options{Format: FormatTable, MaxWidth: 100},
			table: goldenServers(),
		},
		{
			// A hard squeeze: several columns hit the minWidth floor (8 for lists).
			name:  "list_table_shrink_40",
			opts:  Options{Format: FormatTable, MaxWidth: 40},
			table: goldenServers(),
		},
		{
			// Absurdly narrow: usable width goes negative and every column lands on
			// the floor, so the table is wider than the request.
			name:  "list_table_shrink_20",
			opts:  Options{Format: FormatTable, MaxWidth: 20},
			table: goldenServers(),
		},
		{
			// Already fits: shrinkWidths must return the natural widths untouched.
			name:  "list_table_fits_maxwidth",
			opts:  Options{Format: FormatTable, MaxWidth: 400},
			table: goldenServers(),
		},
		{
			// Embedded newlines as hard breaks, unbounded.
			name:  "list_table_multiline",
			opts:  Options{Format: FormatTable},
			table: goldenMultiline(),
		},
		{
			// Newlines plus wrapping: each segment is wrapped independently.
			name:  "list_table_multiline_wrapped",
			opts:  Options{Format: FormatTable, MaxWidth: 44},
			table: goldenMultiline(),
		},
		{
			// wrapLine's space-preferring wrap and its hard break for a token longer
			// than the column.
			name: "list_table_wrap_words_and_long_token",
			opts: Options{Format: FormatTable, MaxWidth: 46},
			table: Table{
				Columns: []string{"Name", "Description"},
				Rows: [][]any{
					{"prose", "a short sentence that has to wrap at spaces because it is long"},
					{"token", "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz"},
					{"mixed", "word ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789ABCDEFGHIJ tail"},
				},
			},
		},
		{
			// Multi-byte widths (runes, not bytes; not display cells).
			name:  "list_table_multibyte",
			opts:  Options{Format: FormatTable},
			table: goldenWide(),
		},
		{
			name:  "list_table_multibyte_shrink",
			opts:  Options{Format: FormatTable, MaxWidth: 48},
			table: goldenWide(),
		},
		{
			// Elision: over maxTableCell the cell becomes a placeholder naming the
			// byte count. The 1024-rune cell beside it must survive untouched.
			name: "list_table_elide_blob",
			opts: Options{Format: FormatTable},
			table: Table{
				Columns: []string{"Name", "User Data"},
				Rows: [][]any{
					{"cloud-init", blob},
					{"empty", ""},
				},
			},
		},
		{
			// The exact elision boundary: maxTableCell runes pass, one more elides.
			name: "list_table_elide_boundary",
			opts: Options{Format: FormatTable, MaxWidth: 60},
			table: Table{
				Columns: []string{"Case", "Value"},
				Rows: [][]any{
					{"at-cap", nearCap},
					{"over-cap", overCap},
				},
			},
		},
		{
			// -c disables elision for the selected columns, so the blob renders in
			// full (and is reachable, which is what the placeholder promises).
			name: "list_table_elide_disabled_by_column",
			opts: Options{Format: FormatTable, Columns: []string{"user data"}},
			table: Table{
				Columns: []string{"Name", "User Data"},
				Rows:    [][]any{{"cloud-init", strings.Repeat("Z", 1100)}},
			},
		},
		{
			// Ragged rows: a short row pads with empty cells rather than erroring.
			name: "list_table_ragged_rows",
			opts: Options{Format: FormatTable},
			table: Table{
				Columns: []string{"A", "B", "C"},
				Rows:    [][]any{{"a1", "b1", "c1"}, {"a2"}, {}},
			},
		},
		{
			// Header wider than every cell: the column keeps the header's width.
			name: "list_table_header_dominates",
			opts: Options{Format: FormatTable},
			table: Table{
				Columns: []string{"Availability Zone", "Up"},
				Rows:    [][]any{{"az-1", true}, {"az-2", false}},
			},
		},
		{
			// No rows at all: headers and borders still render.
			name:  "list_table_no_rows",
			opts:  Options{Format: FormatTable},
			table: Table{Columns: []string{"ID", "Name"}, Rows: nil},
		},
		{
			// Control characters and ANSI escapes are stripped; tabs become spaces.
			name: "list_table_control_chars",
			opts: Options{Format: FormatTable},
			table: Table{
				Columns: []string{"Name", "Value"},
				Rows: [][]any{
					{"ansi", "\x1b[31mred\x1b[0m text"},
					{"tabs", "a\tb\tc"},
					{"cr-bell", "line\r\adone"},
				},
			},
		},
		{
			// -c reorders and narrows; --sort-column sorts numerically on a column
			// that is not displayed.
			name:  "list_table_columns_and_sort",
			opts:  Options{Format: FormatTable, Columns: []string{"name", "STATUS"}, SortColumns: []string{"Size"}},
			table: goldenServers(),
		},
		{
			name:  "list_table_sort_by_name",
			opts:  Options{Format: FormatTable, SortColumns: []string{"Name"}},
			table: goldenServers(),
		},

		// --- machine formats -------------------------------------------------
		{name: "list_json", opts: Options{Format: FormatJSON}, table: goldenServers()},
		{name: "list_yaml", opts: Options{Format: FormatYAML}, table: goldenServers()},
		{name: "list_csv", opts: Options{Format: FormatCSV}, table: goldenServers()},
		{
			// KNOWN DEVIATION: -f value joins cells with a TAB where upstream cliff
			// uses a single space, and cells are not escaped.
			name: "list_value", opts: Options{Format: FormatValue}, table: goldenServers(),
		},
		{name: "list_json_no_rows", opts: Options{Format: FormatJSON}, table: Table{Columns: []string{"ID", "Name"}}},
		{name: "list_yaml_no_rows", opts: Options{Format: FormatYAML}, table: Table{Columns: []string{"ID", "Name"}}},
		{name: "list_csv_no_rows", opts: Options{Format: FormatCSV}, table: Table{Columns: []string{"ID", "Name"}}},
		{name: "list_value_no_rows", opts: Options{Format: FormatValue}, table: Table{Columns: []string{"ID", "Name"}}},
		{
			// CSV quoting (RFC 4180) for commas, quotes and embedded newlines; the
			// same values in -f value are unescaped and spill across lines.
			name: "list_csv_quoting",
			opts: Options{Format: FormatCSV},
			table: Table{
				Columns: []string{"Name", "Value"},
				Rows: [][]any{
					{"comma", "a,b"},
					{"quote", `say "hi"`},
					{"newline", "one\ntwo"},
					{"tab", "a\tb"},
				},
			},
		},
		{
			name: "list_value_unescaped",
			opts: Options{Format: FormatValue},
			table: Table{
				Columns: []string{"Name", "Value"},
				Rows: [][]any{
					{"comma", "a,b"},
					{"quote", `say "hi"`},
					{"newline", "one\ntwo"},
					{"tab", "a\tb"},
				},
			},
		},
		{
			// Machine formats never elide, so the full blob is always reachable.
			name: "list_yaml_no_elision",
			opts: Options{Format: FormatYAML},
			table: Table{
				Columns: []string{"Name", "User Data"},
				Rows:    [][]any{{"cloud-init", strings.Repeat("Z", 1100)}},
			},
		},
		{name: "list_json_multibyte", opts: Options{Format: FormatJSON}, table: goldenWide()},
		{name: "list_yaml_multiline", opts: Options{Format: FormatYAML}, table: goldenMultiline()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := tc.opts
			var buf bytes.Buffer
			if err := o.WriteList(&buf, tc.table); err != nil {
				t.Fatalf("WriteList: %v", err)
			}
			assertGolden(t, tc.name, buf.String())
		})
	}
}

// goldenSingleFields is a "server show"-shaped single resource: the two-column
// Field/Value layout with a 16-rune floor on the value column.
func goldenSingleFields() ([]string, []any) {
	fields := []string{
		"id", "name", "status", "created_at", "deleted_at", "addresses",
		"security_groups", "properties", "description", "power_state", "flavor_disk",
	}
	values := []any{
		"11111111-1111-4111-8111-111111111111",
		"web-01",
		"ACTIVE",
		fixedTime,
		time.Time{},
		"private=192.0.2.11, 2001:db8::11\npublic=198.51.100.11",
		[]string{"default", "web"},
		map[string]string{"role": "web", "env": "prod"},
		"a long description that will have to wrap once the table is fitted to a narrow terminal",
		1,
		20,
	}
	return fields, values
}

func TestGolden_WriteSingle(t *testing.T) {
	t.Parallel()

	fields, values := goldenSingleFields()

	tests := []struct {
		name   string
		opts   Options
		fields []string
		values []any
	}{
		{name: "single_table_plain", opts: Options{Format: FormatTable}, fields: fields, values: values},
		{
			// Fitted: the value column shrinks and wraps, the Field column does not.
			name: "single_table_maxwidth_60", opts: Options{Format: FormatTable, MaxWidth: 60},
			fields: fields, values: values,
		},
		{
			// Below the 16-rune floor the value column stops shrinking.
			name: "single_table_maxwidth_30", opts: Options{Format: FormatTable, MaxWidth: 30},
			fields: fields, values: values,
		},
		{
			name: "single_table_columns", opts: Options{Format: FormatTable, Columns: []string{"name", "status", "id"}},
			fields: fields, values: values,
		},
		{name: "single_json", opts: Options{Format: FormatJSON}, fields: fields, values: values},
		{name: "single_yaml", opts: Options{Format: FormatYAML}, fields: fields, values: values},
		{name: "single_csv", opts: Options{Format: FormatCSV}, fields: fields, values: values},
		{
			// KNOWN DEVIATION: -f value prints the values only, one per line, with no
			// field names and no escaping (a multi-line value spills over lines).
			name: "single_value", opts: Options{Format: FormatValue}, fields: fields, values: values,
		},
		{
			name: "single_table_elide", opts: Options{Format: FormatTable},
			fields: []string{"name", "user_data"},
			values: []any{"cloud-init", strings.Repeat("QUJD", 300)},
		},
		{
			name: "single_table_empty", opts: Options{Format: FormatTable},
			fields: []string{}, values: []any{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := tc.opts
			var buf bytes.Buffer
			if err := o.WriteSingle(&buf, tc.fields, tc.values); err != nil {
				t.Fatalf("WriteSingle: %v", err)
			}
			assertGolden(t, tc.name, buf.String())
		})
	}
}
