// Package metricdef is the single Go-side table describing how each
// headline metric is presented: which collector key it reads, its label,
// unit, color, precision, axis handling, and any display transform.
//
// It is shared by the snapshot renderer (internal/web/snapshot.go) and the
// TUI/GUI viewers so they cannot drift from one another. The browser
// dashboard (internal/web/static/app.js) still carries its own copy of the
// same values; folding that in is a separate, web-facing change.
package metricdef

import (
	"sort"
	"strings"

	"github.com/scottmbaker/topswatch/internal/module"
)

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

// Detail lists, per module, the metrics shown only in a module's expanded
// view. They join the cards and the chart while the view is open. The GPU
// media GT (the video decode/encode and enhancement engines on their own
// power domain since Meteor Lake) clocks independently of the render GT,
// so its frequency is the one number that says "the decoder is awake".
var Detail = map[string][]Def{
	"gpu": {
		{Key: "media_frequency_actual", Label: "Media freq (actual)", Short: "MEDIA FREQ", Unit: "MHz", Color: ColorGreen, Precision: 0, Chart: true},
	},
}

// Engine describes one GPU engine class for the expanded GPU view. Class
// is the kernel's short name, which both drivers use as the prefix of the
// engine label (rcs0, vcs1, ...).
type Engine struct {
	Class string
	Label string
	Desc  string
	Color string
}

// Engines lists the engine classes in display order. Video decode and
// encode both run on the VCS engines: the kernel exposes no counter that
// separates them, nor anything about codec, resolution or frame rate.
var Engines = []Engine{
	{Class: "rcs", Label: "Render", Desc: "3D, display composition", Color: ColorAccent},
	{Class: "ccs", Label: "Compute", Desc: "GPGPU, AI inference", Color: ColorPurple},
	{Class: "bcs", Label: "Copy", Desc: "blitter, buffer transfers", Color: ColorDim},
	{Class: "vcs", Label: "Video", Desc: "decode and encode", Color: ColorOrange},
	{Class: "vecs", Label: "Video enhance", Desc: "scaling, VPP, deinterlace", Color: ColorCyan},
}

// EngineBusy is the per-engine busy metric; each instance carries an
// "engine" label (rcs0, bcs0, vcs0, vecs0, ccs0, or whatever the kernel
// exposes for the detected GPU).
var EngineBusy = Def{Key: "engine_busy", Label: "Engine busy", Short: "BUSY", Unit: "%", Precision: 1, Max: 100}

// EngineClass returns the class entry for an engine label such as "vcs1".
// Unknown classes get a generic entry so a new engine still shows up.
func EngineClass(label string) Engine {
	class := strings.TrimRight(label, "0123456789")
	for _, e := range Engines {
		if e.Class == class {
			return e
		}
	}
	return Engine{Class: class, Label: strings.ToUpper(class), Color: ColorDim}
}

// EngineLabels returns the engine labels present in a GPU metric list,
// in Engines order (then by instance), so the view is stable from sample
// to sample regardless of map iteration order in the collector.
func EngineLabels(metrics []module.Metric) []string {
	var out []string
	for _, m := range metrics {
		if m.Name == EngineBusy.Key && m.Labels["engine"] != "" {
			out = append(out, m.Labels["engine"])
		}
	}
	SortEngines(out)
	return out
}

// SortEngines orders engine labels by class display order, then by name.
func SortEngines(labels []string) {
	rank := func(l string) int {
		class := strings.TrimRight(l, "0123456789")
		for i, e := range Engines {
			if e.Class == class {
				return i
			}
		}
		return len(Engines)
	}
	sort.Slice(labels, func(i, j int) bool {
		ri, rj := rank(labels[i]), rank(labels[j])
		if ri != rj {
			return ri < rj
		}
		return labels[i] < labels[j]
	})
}

// EngineValue returns the busy percentage of one engine in a GPU metric
// list.
func EngineValue(metrics []module.Metric, label string) (float64, bool) {
	for _, m := range metrics {
		if m.Name == EngineBusy.Key && m.Labels["engine"] == label {
			return m.Value, true
		}
	}
	return 0, false
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
