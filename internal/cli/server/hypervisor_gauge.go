package server

import (
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// gaugeOpts holds the rich "--gauge" hypervisor-list settings.
type gaugeOpts struct {
	barWidth      int // 0 = auto (per profile)
	ascii         bool
	color         *bool // nil = auto (TTY + !NO_COLOR)
	warnPct       float64
	critPct       float64
	includeIronic bool
	aggregate     string
	sortKey       string
	reverse       bool
	width         int // 0 = auto-detect
	checkActual   bool
	ne            neOpts
}

// hostRow is the computed per-hypervisor data used for rendering and sorting.
type hostRow struct {
	name       string
	aggregate  string
	htype      string
	vms        int
	vcpusUsed  int
	vcpusTotal int
	overcommit float64
	ramUsedMB  float64
	ramTotalMB float64
	ramPct     float64
	diskUsedGB float64
	diskTotGB  float64
	diskPct    float64
	cpuModel   string
	state      string
	status     string
	hostIP     string

	// actual (node_exporter) — populated only with --check-actual.
	actualErr    string
	cpuAllocPct  float64
	cpuPhysPct   float64 // -1 if unknown
	ramPhysPct   float64 // -1 if unknown
	ramPhysUsedB float64
}

// profile is the responsive column set chosen by terminal width.
type profile int

const (
	profileCompact profile = iota // ~80, default
	profileWide                   // ~160
	profileFull                   // ~240 (and --check-actual)
)

const (
	barCompact = 8
	barWide    = 16
	barFull    = 20
	barMin     = 4
)

// detectWidth resolves the render width: --width, else the TTY size, else
// $COLUMNS, else 80.
func detectWidth(override int) int {
	if override > 0 {
		return override
	}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	if c := os.Getenv("COLUMNS"); c != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(c)); err == nil && n > 0 {
			return n
		}
	}
	return 80
}

func pickProfile(width int, checkActual bool) profile {
	if checkActual || width >= 240 {
		return profileFull
	}
	if width >= 160 {
		return profileWide
	}
	return profileCompact
}

func defaultBarWidth(p profile) int {
	switch p {
	case profileFull:
		return barFull
	case profileWide:
		return barWide
	default:
		return barCompact
	}
}

// colorEnabled resolves auto/forced color: explicit flag wins, else on when
// stdout is a TTY and NO_COLOR is unset.
func colorEnabled(setting *bool) bool {
	if setting != nil {
		return *setting
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

const csi = "\x1b["

func colorize(s string, code int, enable bool) string {
	if !enable {
		return s
	}
	return fmt.Sprintf("%s%dm%s%s0m", csi, code, s, csi)
}

// colorByPct: green < warn, yellow >= warn, red >= crit.
func colorByPct(s string, pct, warn, crit float64, enable bool) string {
	switch {
	case pct >= crit:
		return colorize(s, 31, enable)
	case pct >= warn:
		return colorize(s, 33, enable)
	default:
		return colorize(s, 32, enable)
	}
}

// colorOvercommit: green < 1.0x, yellow 1–4x, red >= 4x.
func colorOvercommit(oc float64, enable bool) string {
	s := fmt.Sprintf("%.2fx", oc)
	switch {
	case oc >= 4.0:
		return colorize(s, 31, enable)
	case oc >= 1.0:
		return colorize(s, 33, enable)
	default:
		return colorize(s, 32, enable)
	}
}

// bar renders a gauge like "[████░░░░] 52%", colored by threshold.
func bar(pct float64, width int, ascii, enable bool, warn, crit float64) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(math.Round(float64(width) * pct / 100))
	if filled > width {
		filled = width
	}
	fillCh, emptyCh := "█", " "
	if ascii {
		fillCh, emptyCh = "#", "-"
	}
	b := strings.Repeat(fillCh, filled) + strings.Repeat(emptyCh, width-filled)
	pctStr := fmt.Sprintf("%.0f%%", pct)
	return "[" + colorByPct(b, pct, warn, crit, enable) + "] " + colorByPct(pctStr, pct, warn, crit, enable)
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// visLen is the printed width of a cell, ignoring ANSI codes.
func visLen(s string) int {
	return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, ""))
}

func humanGiBFromMB(mb float64) string {
	return fmt.Sprintf("%.0f", mb/1024)
}

// column defines one table column and how to render a cell for a row.
type column struct {
	header string
	min    profile
	actual bool // only shown with --check-actual
	render func(r hostRow, o *gaugeOpts, barW int, color bool) string
}

func gaugeColumns() []column {
	return []column{
		{"Name", profileCompact, false, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string { return r.name }},
		{"VMs", profileCompact, false, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string { return strconv.Itoa(r.vms) }},
		{"OC", profileCompact, false, func(r hostRow, _ *gaugeOpts, _ int, c bool) string { return colorOvercommit(r.overcommit, c) }},
		{"RAM", profileCompact, false, func(r hostRow, o *gaugeOpts, bw int, c bool) string {
			return bar(r.ramPct, bw, o.ascii, c, o.warnPct, o.critPct)
		}},
		{"Disk", profileCompact, false, func(r hostRow, o *gaugeOpts, bw int, c bool) string {
			return bar(r.diskPct, bw, o.ascii, c, o.warnPct, o.critPct)
		}},
		{"St", profileCompact, false, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string { return r.state }},
		{"Aggregate", profileWide, false, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string { return r.aggregate }},
		{"Type", profileWide, false, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string { return r.htype }},
		{"vCPU(u/t)", profileWide, false, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string {
			return fmt.Sprintf("%d/%d", r.vcpusUsed, r.vcpusTotal)
		}},
		{"RAM(u/t GiB)", profileWide, false, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string {
			return humanGiBFromMB(r.ramUsedMB) + "/" + humanGiBFromMB(r.ramTotalMB)
		}},
		{"Disk(u/t GiB)", profileWide, false, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string {
			return fmt.Sprintf("%.0f/%.0f", r.diskUsedGB, r.diskTotGB)
		}},
		{"CPU Model", profileWide, false, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string { return r.cpuModel }},
		{"CPUa%", profileFull, true, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string {
			return fmt.Sprintf("%.0f%%", r.cpuAllocPct)
		}},
		{"CPU_phys", profileFull, true, func(r hostRow, o *gaugeOpts, bw int, c bool) string {
			if r.actualErr != "" {
				return "err"
			}
			if r.cpuPhysPct < 0 {
				return "-"
			}
			return bar(r.cpuPhysPct, bw, o.ascii, c, o.warnPct, o.critPct)
		}},
		{"RAM_phys", profileFull, true, func(r hostRow, o *gaugeOpts, bw int, c bool) string {
			if r.actualErr != "" || r.ramPhysPct < 0 {
				return "-"
			}
			return bar(r.ramPhysPct, bw, o.ascii, c, o.warnPct, o.critPct)
		}},
		{"RAM_phys_used", profileFull, true, func(r hostRow, _ *gaugeOpts, _ int, _ bool) string {
			if r.ramPhysUsedB <= 0 {
				return "-"
			}
			return fmt.Sprintf("%.0f GiB", r.ramPhysUsedB/(1024*1024*1024))
		}},
	}
}

// renderGauge writes the responsive gauge table for the given rows.
func renderGauge(w io.Writer, rows []hostRow, o *gaugeOpts) {
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(w, "No hypervisors matched your filters.")
		return
	}
	width := detectWidth(o.width)
	prof := pickProfile(width, o.checkActual)
	color := colorEnabled(o.color)

	barW := defaultBarWidth(prof)
	if o.barWidth > 0 {
		barW = o.barWidth
	}

	// Select columns for the profile.
	var cols []column
	for _, c := range gaugeColumns() {
		if c.min > prof {
			continue
		}
		if c.actual && !o.checkActual {
			continue
		}
		cols = append(cols, c)
	}

	// Render, shrinking bars until the table fits (down to barMin).
	var headers []string
	var cells [][]string
	for {
		headers, cells = renderCells(cols, rows, o, barW, color)
		if tableWidth(headers, cells) <= width || barW <= barMin {
			break
		}
		barW--
	}

	writeGaugeTable(w, headers, cells)
}

func renderCells(cols []column, rows []hostRow, o *gaugeOpts, barW int, color bool) ([]string, [][]string) {
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.header
	}
	cells := make([][]string, len(rows))
	for i, r := range rows {
		row := make([]string, len(cols))
		for j, c := range cols {
			row[j] = c.render(r, o, barW, color)
		}
		cells[i] = row
	}
	return headers, cells
}

// tableWidth computes the printed width of a borderless table with a 2-space
// gutter between columns.
func tableWidth(headers []string, cells [][]string) int {
	widths := colWidths(headers, cells)
	total := 0
	for _, wd := range widths {
		total += wd
	}
	if len(widths) > 1 {
		total += 2 * (len(widths) - 1)
	}
	return total
}

func colWidths(headers []string, cells [][]string) []int {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = visLen(h)
	}
	for _, row := range cells {
		for i := 0; i < len(row) && i < len(widths); i++ {
			if l := visLen(row[i]); l > widths[i] {
				widths[i] = l
			}
		}
	}
	return widths
}

// writeGaugeTable prints a borderless, left-aligned table (kubectl-style),
// padding by visible width so ANSI-colored cells align correctly.
func writeGaugeTable(w io.Writer, headers []string, cells [][]string) {
	widths := colWidths(headers, cells)
	writeGaugeRow(w, headers, widths)
	for _, row := range cells {
		writeGaugeRow(w, row, widths)
	}
}

func writeGaugeRow(w io.Writer, cells []string, widths []int) {
	var b strings.Builder
	for i, c := range cells {
		b.WriteString(c)
		if i < len(cells)-1 {
			b.WriteString(strings.Repeat(" ", widths[i]-visLen(c)+2))
		}
	}
	_, _ = fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
}

// sortRows sorts by the requested key (falls back to name).
func sortRows(rows []hostRow, key string, reverse bool) {
	less := map[string]func(a, b hostRow) bool{
		"name":       func(a, b hostRow) bool { return a.name < b.name },
		"aggregate":  func(a, b hostRow) bool { return a.aggregate < b.aggregate },
		"vms":        func(a, b hostRow) bool { return a.vms < b.vms },
		"overcommit": func(a, b hostRow) bool { return a.overcommit < b.overcommit },
		"ram":        func(a, b hostRow) bool { return a.ramPct < b.ramPct },
		"disk":       func(a, b hostRow) bool { return a.diskPct < b.diskPct },
	}
	fn := less[key]
	if fn == nil {
		fn = less["name"]
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if reverse {
			return fn(rows[j], rows[i])
		}
		return fn(rows[i], rows[j])
	})
}

func ratio(used, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return used / total
}
