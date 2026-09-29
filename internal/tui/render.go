package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tslc "github.com/NimbleMarkets/ntcharts/linechart/timeserieslinechart"
	"github.com/charmbracelet/lipgloss"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/metricdef"
	"github.com/scottmbaker/topswatch/internal/module"
)

// Theme, mirroring style.css / snapshot.go.
var (
	styDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e"))
	styAccent = lipgloss.NewStyle().Foreground(lipgloss.Color(metricdef.ColorAccent))
	styWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color(metricdef.ColorOrange))
	styCrit   = lipgloss.NewStyle().Foreground(lipgloss.Color(metricdef.ColorRed)).Bold(true)
	styBorder = lipgloss.NewStyle().Foreground(lipgloss.Color("#30363d"))
)

func colorStyle(hex string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
}

// layout is the vertical budget for one frame.
type layout struct {
	width, height int
	chartH        int  // 0 = no charts (short terminal)
	sparkN        int  // sparkline sample count
	gap           bool // blank line between modules
}

const (
	headerLines  = 1
	footerLines  = 2
	moduleTitleL = 1
	cardLines    = 3 // label / value / sparkline
	moduleGapL   = 1
	minChartH    = 4
	sparkSamples = 60
)

func computeLayout(w, h, nModules int) layout {
	l := layout{width: w, height: h, sparkN: sparkSamples, gap: true}
	if nModules == 0 {
		return l
	}
	fixed := headerLines + footerLines + nModules*(moduleTitleL+cardLines+moduleGapL)
	if fixed > h {
		// Too short even for cards plus gaps: drop the gaps first.
		l.gap = false
		fixed -= nModules * moduleGapL
	}
	spare := h - fixed
	if spare >= nModules*minChartH {
		l.chartH = spare / nModules
		if l.chartH > 12 {
			l.chartH = 12
		}
	}
	return l
}

// chartCache memoizes rendered module charts. Building a braille chart
// is the most expensive part of a frame, and the data only changes when
// a sample arrives, so re-rendering on every event is wasted CPU.
type chartCache struct {
	entries map[string]chartEntry
}

type chartEntry struct {
	gen  uint64
	w, h int
	out  string
}

func newChartCache() *chartCache { return &chartCache{entries: map[string]chartEntry{}} }

func (c *chartCache) get(mod string, w, h int, gen uint64, build func() string) string {
	if c == nil {
		return build()
	}
	if e, ok := c.entries[mod]; ok && e.gen == gen && e.w == w && e.h == h {
		return e.out
	}
	out := build()
	c.entries[mod] = chartEntry{gen, w, h, out}
	return out
}

// frame is everything the view needs; it is built by the model and
// rendered by pure functions so tests can drive it without a terminal.
type frame struct {
	charts   *chartCache
	addr     string
	now      time.Time
	tier     string
	mode     string // "stream" or "poll 2s"
	paused   bool
	err      string
	devices  map[string]module.DeviceInfo
	warnings []collector.Warning
	store    *store
}

func render(f frame, l layout) string {
	mods := presentModules(f.devices)
	var b strings.Builder
	b.WriteString(renderHeader(f, l.width))
	b.WriteByte('\n')
	for _, mod := range mods {
		b.WriteString(renderModule(f, mod, l))
	}
	b.WriteString(renderFooter(f, l.width))
	return b.String()
}

func presentModules(devices map[string]module.DeviceInfo) []string {
	out := make([]string, 0, len(metricdef.Order))
	for _, m := range metricdef.Order {
		if _, ok := devices[m]; ok {
			out = append(out, m)
		}
	}
	return out
}

func renderHeader(f frame, w int) string {
	addr := strings.TrimPrefix(f.addr, "http://")
	left := styAccent.Bold(true).Render("TopsWatch") + styDim.Render("  "+addr)
	status := f.mode
	if f.paused {
		status = "paused"
	}
	right := styDim.Render(fmt.Sprintf("%s   range %s   %s", f.now.Format("2006-01-02 15:04:05"), f.tier, status))
	return joinEnds(left, right, w)
}

func renderModule(f frame, mod string, l layout) string {
	var b strings.Builder
	dev := f.devices[mod]
	title := styAccent.Render(strings.ToUpper(mod))
	desc := styDim.Render(truncate(deviceLine(mod, dev), l.width-len(mod)-3))
	b.WriteString(title + " " + desc + "\n")

	defs := metricdef.Cards[mod]
	b.WriteString(renderCards(f.store, mod, defs, l.width, l.sparkN))
	if l.chartH > 0 {
		b.WriteString(f.charts.get(mod, l.width, l.chartH, f.store.gen, func() string {
			return renderChart(f.store, mod, defs, l.width, l.chartH)
		}))
	}
	if l.gap {
		b.WriteByte('\n')
	}
	return b.String()
}

func deviceLine(mod string, dev module.DeviceInfo) string {
	switch mod {
	case "cpu":
		return fmt.Sprintf("%s  %sC/%sT  %s", dev.Name, dev.Extra["cores"], dev.Extra["threads"], dev.Extra["arch"])
	case "gpu":
		return fmt.Sprintf("%s  %s  %s", dev.Name, dev.PCIDevice, dev.Extra["driver"])
	case "npu":
		return fmt.Sprintf("%s  %s  drv %s  fw %s", dev.Name, dev.PCIDevice, dev.DriverVersion, dev.FirmwareVersion)
	}
	return dev.Name
}

// renderCards lays the module's headline metrics side by side: label,
// value with unit, and a block-character sparkline of the last sparkN
// values, each scaled to its own range (or 0..Max when fixed).
func renderCards(st *store, mod string, defs []metricdef.Def, w, sparkN int) string {
	n := len(defs)
	if n == 0 {
		return ""
	}
	cardW := w / n
	if cardW < 8 {
		cardW = 8
	}
	inner := cardW - 1
	labels := make([]string, n)
	vals := make([]string, n)
	sparks := make([]string, n)
	for i, d := range defs {
		c := colorStyle(d.Color)
		labels[i] = pad(styDim.Render(d.Short), inner)
		v, ok := st.last(mod, d.Key)
		vs := "--"
		if ok {
			vs = metricdef.Format(v, d)
		}
		vals[i] = pad(c.Bold(true).Render(vs)+" "+styDim.Render(d.Unit), inner)
		sparks[i] = pad(c.Render(sparkline(values(st.get(mod, d.Key), sparkN), inner, d.Max)), inner)
	}
	return strings.Join(labels, " ") + "\n" + strings.Join(vals, " ") + "\n" + strings.Join(sparks, " ") + "\n"
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline renders vs into width cells. Fewer values than cells are
// right-aligned so the newest value is always at the right edge.
func sparkline(vs []float64, width int, fixedMax float64) string {
	if width <= 0 {
		return ""
	}
	if len(vs) > width {
		vs = vs[len(vs)-width:]
	}
	if len(vs) == 0 {
		return strings.Repeat(" ", width)
	}
	mn, mx := minMax(vs)
	if fixedMax > 0 {
		mn = 0
		if mx < fixedMax {
			mx = fixedMax
		}
	}
	if mx == mn {
		mx = mn + 1
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", width-len(vs)))
	for _, v := range vs {
		idx := int((v - mn) / (mx - mn) * float64(len(sparkRunes)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkRunes) {
			idx = len(sparkRunes) - 1
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

// renderChart draws the module's chartable series on one braille canvas.
// As in the snapshot renderer, each series is normalized to its own range
// so utilization, frequency, power, and temperature share the canvas.
func renderChart(st *store, mod string, defs []metricdef.Def, w, h int) string {
	var minT, maxT time.Time
	type ds struct {
		d   metricdef.Def
		pts []point
	}
	var sets []ds
	for _, d := range defs {
		if !d.Chart {
			continue
		}
		pts := st.get(mod, d.Key)
		if len(pts) < 2 {
			continue
		}
		sets = append(sets, ds{d, pts})
		if minT.IsZero() || pts[0].t.Before(minT) {
			minT = pts[0].t
		}
		if last := pts[len(pts)-1].t; last.After(maxT) {
			maxT = last
		}
	}
	if len(sets) == 0 {
		return styDim.Render(pad("  (collecting…)", w)) + strings.Repeat("\n", h)
	}
	if !maxT.After(minT) {
		maxT = minT.Add(time.Second)
	}
	c := tslc.New(w, h,
		tslc.WithYRange(0, 100),
		tslc.WithTimeRange(minT, maxT),
		tslc.WithXLabelFormatter(tslc.HourTimeLabelFormatter()),
		tslc.WithYLabelFormatter(func(int, float64) string { return "" }),
		tslc.WithAxesStyles(styBorder, styDim),
	)
	for _, s := range sets {
		c.SetDataSetStyle(s.d.Key, colorStyle(s.d.Color))
		vs := values(s.pts, 0)
		mn, mx := minMax(vs)
		if s.d.Max > 0 {
			mn = 0
			if mx < s.d.Max {
				mx = s.d.Max
			}
		}
		if mx == mn {
			mx = mn + 1
		}
		for _, p := range s.pts {
			c.PushDataSet(s.d.Key, tslc.TimePoint{Time: p.t, Value: (p.v - mn) / (mx - mn) * 100})
		}
	}
	c.SetViewTimeAndYRange(minT, maxT, 0, 100)
	c.DrawBrailleAll()
	return c.View() + "\n"
}

func renderFooter(f frame, w int) string {
	var b strings.Builder
	switch {
	case f.err != "":
		b.WriteString(styCrit.Render(truncate("error: "+f.err, w)))
	case len(f.warnings) > 0:
		parts := make([]string, 0, len(f.warnings))
		for _, wn := range f.warnings {
			s := styWarn
			if wn.Severity == "critical" {
				s = styCrit
			}
			parts = append(parts, s.Render(fmt.Sprintf("%s %s: %s", strings.ToUpper(wn.Module), wn.Kind, wn.Message)))
		}
		b.WriteString(truncate(strings.Join(parts, "   "), w))
	default:
		b.WriteString(styDim.Render("no warnings"))
	}
	b.WriteByte('\n')
	b.WriteString(styDim.Render("q quit   r cycle range   p pause"))
	return b.String()
}

// --- helpers ---

func minMax(vs []float64) (float64, float64) {
	mn, mx := math.Inf(1), math.Inf(-1)
	for _, v := range vs {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return mn, mx
}

// pad right-pads (or truncates) styled text to width cells.
func pad(s string, width int) string {
	cur := lipgloss.Width(s)
	if cur >= width {
		return truncate(s, width)
	}
	return s + strings.Repeat(" ", width-cur)
}

// truncate cuts s to at most width cells. Styled strings are cut on
// visible width via lipgloss.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

func joinEnds(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncate(left+" "+right, w)
	}
	return left + strings.Repeat(" ", gap) + right
}
