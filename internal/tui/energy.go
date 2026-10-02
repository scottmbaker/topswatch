package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/scottmbaker/topswatch/internal/energy"
	"github.com/scottmbaker/topswatch/internal/metricdef"
)

// energyView is the snapshot of the stopwatch session that the renderer
// needs. It is nil when the panel is disabled (the default).
type energyView struct {
	supported bool
	state     energy.State
	report    energy.Report
	hasReport bool

	baseCapturing bool
	baseRemaining time.Duration
	base          *energy.Report
	baselineFor   time.Duration
}

func newEnergyView(s *energy.Session) *energyView {
	if s == nil {
		return nil
	}
	v := &energyView{supported: s.Supported(), state: s.State(), baselineFor: s.BaselineFor}
	v.report, v.hasReport = s.Report()
	v.baseCapturing, v.baseRemaining, v.base = s.Baseline()
	return v
}

var (
	styRun  = lipgloss.NewStyle().Foreground(lipgloss.Color(metricdef.ColorGreen)).Bold(true)
	styStop = lipgloss.NewStyle().Foreground(lipgloss.Color(metricdef.ColorOrange)).Bold(true)
	styBold = lipgloss.NewStyle().Foreground(lipgloss.Color("#e6edf3")).Bold(true)
)

// lines returns the panel's rows, unstyled width-agnostic. The panel is
// one line until a measurement exists, so enabling it costs little space.
func (v *energyView) lines() []string {
	if v == nil {
		return nil
	}
	title := styAccent.Render("ENERGY") + "  "
	if !v.supported {
		return []string{title + styDim.Render("waiting for energy counters (the daemon needs its power collector, running as root)")}
	}

	var status string
	switch v.state {
	case energy.Running:
		status = styRun.Render("● measuring ") + styBold.Render(energy.FormatDuration(v.report.Elapsed))
	case energy.Stopped:
		status = styStop.Render("■ stopped ") + styBold.Render(energy.FormatDuration(v.report.Elapsed)) +
			styDim.Render("   s starts a new measurement")
	default:
		status = styDim.Render("press s to start measuring")
	}

	var base string
	switch {
	case v.baseCapturing:
		base = styStop.Render(fmt.Sprintf("capturing idle baseline, %.0fs left (keep the device idle)", v.baseRemaining.Seconds()))
	case v.base != nil:
		base = styDim.Render("idle baseline: " + baselineSummary(*v.base))
	default:
		base = styDim.Render(fmt.Sprintf("no idle baseline (b measures %s of idle)", v.baselineFor))
	}
	out := []string{title + status + styDim.Render("   |   ") + base}

	if !v.hasReport {
		return out
	}
	for i, ln := range strings.Split(strings.TrimRight(v.report.Table(), "\n"), "\n") {
		switch {
		case i == 0:
			out = append(out, styDim.Render(ln))
		case !strings.HasPrefix(ln, "  "):
			out = append(out, styBold.Render(ln)) // SoC total / System total
		default:
			out = append(out, ln)
		}
	}
	if notes := v.report.Footnotes(); len(notes) > 0 {
		out = append(out, styDim.Render(notes[0]))
	}
	return out
}

// baselineSummary names the widest total the baseline has.
func baselineSummary(b energy.Report) string {
	for _, key := range []string{energy.KeySystem, energy.KeySoC} {
		if l, ok := b.Line(key); ok && l.Available {
			return fmt.Sprintf("%s %.2f W over %.0fs", strings.ToLower(l.Label), l.Watts, b.Seconds)
		}
	}
	return "captured"
}

// height is the number of rows the panel occupies, including its blank
// separator line.
func (v *energyView) height() int {
	if v == nil {
		return 0
	}
	return len(v.lines()) + 1
}

func renderEnergy(v *energyView, w int) string {
	if v == nil {
		return ""
	}
	var b strings.Builder
	for _, ln := range v.lines() {
		b.WriteString(truncate(ln, w))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}
