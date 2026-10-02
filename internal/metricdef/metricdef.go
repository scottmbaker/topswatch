// Package metricdef is the single Go-side table describing how each
// headline metric is presented: which collector key it reads, its label,
// unit, color, precision, axis handling, and any display transform.
//
// It is shared by the snapshot renderer (internal/web/snapshot.go) and the
// TUI/GUI viewers so they cannot drift from one another. The browser
// dashboard (internal/web/static/app.js) still carries its own copy of the
// same values; folding that in is a separate, web-facing change.
package metricdef

import "github.com/scottmbaker/topswatch/internal/module"

// Def describes the presentation of one metric within a module.
type Def struct {
	// Key is the metric name as emitted by the collector module.
	Key string
	// Label is the long display name ("Utilization"); Short is the
	// abbreviated form used where space is tight ("UTIL").
	Label string
	Short string
	// Unit is the display unit after Transform has been applied.
	Unit string
	// Color is the series color as "#rrggbb", matching style.css.
	Color string
	// Precision is the number of decimals to show.
	Precision int
	// Max, when non-zero, fixes the chart axis to [0, Max]; otherwise the
	// axis autoscales to the data.
	Max float64
	// Aggregate sums every labeled instance sharing Key instead of taking
	// the single unlabeled value. Used for metrics that only exist as a
	// labeled split (gpu memory_used, one per workload class).
	Aggregate bool
	// Chart includes the metric in the module's time-series chart. Card-only
	// metrics have scales unrelated to util/freq/power/temp.
	Chart bool
	// Transform converts the raw collector value to the display unit.
	Transform func(float64) float64
}

// Theme colors, mirroring style.css.
const (
	ColorAccent = "#58a6ff"
	ColorCyan   = "#39d2c0"
	ColorOrange = "#d29922"
	ColorRed    = "#f85149"
	ColorPurple = "#bc8cff"
	ColorGreen  = "#3fb950"
	ColorDim    = "#8b949e"
)

const gib = 1024 * 1024 * 1024

func bytesToGB(v float64) float64 { return v / gib }
func mbToGB(v float64) float64    { return v / 1000.0 }

// Order is the display order of modules.
var Order = []string{"cpu", "npu", "gpu"}

// Cards lists, per module, the headline metrics in display order.
var Cards = map[string][]Def{
	// One temperature, on the CPU row. The CPU package sensor is the SoC
	// die's hottest reading in every state measured (idle, CPU, GPU and
	// NPU load); the NPU's own sensor runs 10-30 C cooler and the GPU has
	// none on current Xe parts. Separate cards only invited comparison of
	// numbers that are not comparable.
	"cpu": {
		{Key: "utilization", Label: "Utilization", Short: "UTIL", Unit: "%", Color: ColorAccent, Precision: 1, Max: 100, Chart: true},
		{Key: "frequency", Label: "Frequency", Short: "FREQ", Unit: "MHz", Color: ColorCyan, Precision: 0, Chart: true},
		{Key: "power", Label: "Power", Short: "POWER", Unit: "W", Color: ColorOrange, Precision: 2, Chart: true},
		{Key: "temperature", Label: "SoC temperature", Short: "SOC TEMP", Unit: "C", Color: ColorRed, Precision: 0, Chart: true},
		{Key: "memory_used", Label: "System memory", Short: "MEM", Unit: "GB", Color: ColorPurple, Precision: 1, Transform: bytesToGB},
	},
	"npu": {
		{Key: "utilization", Label: "Utilization", Short: "UTIL", Unit: "%", Color: ColorAccent, Precision: 1, Max: 100, Chart: true},
		{Key: "frequency", Label: "Frequency", Short: "FREQ", Unit: "MHz", Color: ColorCyan, Precision: 0, Chart: true},
		{Key: "power", Label: "Power", Short: "POWER", Unit: "W", Color: ColorOrange, Precision: 2, Chart: true},
		{Key: "ddr_bandwidth", Label: "DDR Bandwidth", Short: "DDR BW", Unit: "GB/s", Color: ColorPurple, Precision: 2, Transform: mbToGB},
	},
	"gpu": {
		{Key: "utilization", Label: "Utilization", Short: "UTIL", Unit: "%", Color: ColorAccent, Precision: 1, Max: 100, Chart: true},
		{Key: "frequency_actual", Label: "Freq (actual)", Short: "FREQ", Unit: "MHz", Color: ColorCyan, Precision: 0, Chart: true},
		{Key: "power", Label: "Power", Short: "POWER", Unit: "W", Color: ColorOrange, Precision: 2, Chart: true},
		{Key: "memory_used", Label: "Memory", Short: "MEM", Unit: "GB", Color: ColorPurple, Precision: 2, Aggregate: true, Transform: bytesToGB},
	},
}

// Memory-bar inputs, in GB. The viewers draw one stacked bar for system
// RAM: GPU buffers (which live in system RAM on an integrated GPU and are
// already inside "used"), the rest of "used", and free.
var (
	SystemMemoryUsed  = Def{Key: "memory_used", Transform: bytesToGB}
	SystemMemoryTotal = Def{Key: "memory_total", Transform: bytesToGB}
	GPUMemoryUsed     = Def{Key: "memory_used", Aggregate: true, Transform: bytesToGB}
)

// MemoryBar is the stacked memory split, in GB, for one sample.
type MemoryBar struct {
	Total, Used, GPU float64
}

// Memory derives the bar from a sample's cpu and gpu metric lists. ok is
// false when the daemon reports no system memory.
func Memory(cpu, gpu []module.Metric) (MemoryBar, bool) {
	total, ok1 := Value(cpu, SystemMemoryTotal)
	used, ok2 := Value(cpu, SystemMemoryUsed)
	if !ok1 || !ok2 || total <= 0 {
		return MemoryBar{}, false
	}
	g, _ := Value(gpu, GPUMemoryUsed)
	if g > used {
		g = used
	}
	return MemoryBar{Total: total, Used: used, GPU: g}, true
}

// Raw returns the untransformed value for d within one module's metric
// list. Labeled instances are skipped unless d.Aggregate is set, in which
// case they are summed.
func Raw(metrics []module.Metric, d Def) (float64, bool) {
	var sum float64
	var found bool
	for _, m := range metrics {
		if m.Name != d.Key {
			continue
		}
		if !d.Aggregate {
			if len(m.Labels) == 0 {
				return m.Value, true
			}
			continue
		}
		sum += m.Value
		found = true
	}
	return sum, found
}

// Value returns the display value for d: Raw with Transform applied.
func Value(metrics []module.Metric, d Def) (float64, bool) {
	v, ok := Raw(metrics, d)
	if !ok {
		return 0, false
	}
	if d.Transform != nil {
		v = d.Transform(v)
	}
	return v, true
}

// Format renders v with d's precision.
func Format(v float64, d Def) string {
	return formatFloat(v, d.Precision)
}
