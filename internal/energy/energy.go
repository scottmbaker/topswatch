// Package energy turns the daemon's cumulative energy counters into a
// per-component breakdown for a time window. It is client-side: a viewer
// takes a Reading when a measurement starts and another when it stops and
// calls Diff. The daemon keeps no session state, so any number of viewers
// and scripts can measure independently.
package energy

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/scottmbaker/topswatch/internal/collector"
)

// Raw counter domains (the "domain" label on energy metrics).
const (
	domPackage = "package"
	domCore    = "core"
	domUncore  = "uncore"
	domDRAM    = "dram"
	domPsys    = "psys"
	domBattery = "battery"
	domNPU     = "npu"
)

// Reading is the set of cumulative counters at one instant.
type Reading struct {
	Time   time.Time
	Joules map[string]float64 // by raw domain
	// BatterySeconds is how long the battery source has been measuring
	// (it only measures while discharging). HasBattery is false on
	// devices without a fuel gauge.
	BatterySeconds float64
	HasBattery     bool
}

// FromSample extracts the counters from a daemon sample. Energy metrics
// are recognised by name "energy", unit "J" and a "domain" label, in any
// module (the power collector and the NPU collector both emit them).
func FromSample(s collector.Sample) Reading {
	r := Reading{Time: s.Timestamp, Joules: map[string]float64{}}
	for _, ms := range s.Metrics {
		for _, m := range ms {
			d := m.Labels["domain"]
			if d == "" {
				continue
			}
			switch {
			case m.Name == "energy" && m.Unit == "J":
				r.Joules[d] = m.Value
				if d == domBattery {
					r.HasBattery = true
				}
			case m.Name == "measured_seconds" && d == domBattery:
				r.BatterySeconds = m.Value
			}
		}
	}
	return r
}

// Available reports whether the daemon exposes any energy counters.
func (r Reading) Available() bool { return len(r.Joules) > 0 }

// Line keys, in display order.
const (
	KeyCPU      = "cpu"
	KeyGPU      = "gpu"
	KeyNPU      = "npu"
	KeySoCOther = "soc_other"
	KeySoC      = "soc"
	KeyDRAM     = "dram"
	KeyRest     = "rest"
	KeySystem   = "system"
)

// Line is one row of the breakdown.
type Line struct {
	Key       string  `json:"key"`
	Label     string  `json:"label"`
	Available bool    `json:"available"`
	Joules    float64 `json:"joules"`
	WattHours float64 `json:"watt_hours"`
	Watts     float64 `json:"avg_watts"`
	// Share is the fraction of the reference total: the system total when
	// one is available, otherwise the SoC total.
	Share float64 `json:"share"`
	// Total marks subtotal/total rows (SoC, System) as opposed to
	// components.
	Total bool `json:"total"`
	// Baseline fields are set by WithBaseline.
	IdleWatts    float64 `json:"idle_watts,omitempty"`
	NetJoules    float64 `json:"net_joules,omitempty"`
	NetWattHours float64 `json:"net_watt_hours,omitempty"`
	HasBaseline  bool    `json:"has_baseline,omitempty"`
	Note         string  `json:"note,omitempty"`
}

// Report is the breakdown for one window.
type Report struct {
	Start   time.Time     `json:"start"`
	End     time.Time     `json:"end"`
	Elapsed time.Duration `json:"-"`
	Seconds float64       `json:"seconds"`
	Lines   []Line        `json:"lines"`
	// SystemSource is "battery", "psys", or "" when no whole-device figure
	// is available; SystemNote says why.
	SystemSource string `json:"system_source"`
	SystemNote   string `json:"system_note,omitempty"`
	// BaselineSeconds is the length of the idle window used, 0 if none.
	BaselineSeconds float64 `json:"baseline_seconds,omitempty"`
}

// Line returns the row with the given key.
func (r Report) Line(key string) (Line, bool) {
	for _, l := range r.Lines {
		if l.Key == key {
			return l, true
		}
	}
	return Line{}, false
}

// batteryFloor is the fraction of SoC energy below which a battery figure
// cannot be the whole device's. RAPL is an estimate and the gauge lags, so
// this is deliberately loose; a genuine reading is well above 1.
const batteryFloor = 0.8

func delta(a, b Reading, dom string) (float64, bool) {
	av, aok := a.Joules[dom]
	bv, bok := b.Joules[dom]
	if !aok || !bok {
		return 0, false
	}
	d := bv - av
	if d < 0 {
		// Daemon restarted between the readings; the window is unusable
		// for this domain.
		return 0, false
	}
	return d, true
}

// Diff computes the breakdown between two readings.
//
// RAPL domains nest: "package" is the whole SoC and already contains the
// CPU cores ("core"), the integrated GPU ("uncore") and the NPU. DRAM is
// a separate domain outside the package. The breakdown therefore reports
// the components, the remainder of the package as "SoC other", and the
// remainder of the system as "Rest of system" (display, storage, radios,
// conversion losses), so the rows sum to the total without double
// counting.
func Diff(a, b Reading) Report {
	rep := Report{Start: a.Time, End: b.Time, Elapsed: b.Time.Sub(a.Time)}
	rep.Seconds = rep.Elapsed.Seconds()

	core, hasCore := delta(a, b, domCore)
	gpu, hasGPU := delta(a, b, domUncore)
	npu, hasNPU := delta(a, b, domNPU)
	pkg, hasPkg := delta(a, b, domPackage)
	dram, hasDRAM := delta(a, b, domDRAM)

	// --- system total ---
	system, hasSystem := 0.0, false
	if a.HasBattery && b.HasBattery {
		covered := b.BatterySeconds - a.BatterySeconds
		// While discharging throughout, coverage equals elapsed time.
		tol := 0.5 + 0.01*rep.Seconds
		j, ok := delta(a, b, domBattery)
		switch {
		case ok && rep.Seconds > 0 && covered >= rep.Seconds-tol && hasPkg && j < batteryFloor*pkg:
			// Defence in depth for the daemon's own check: the whole
			// device cannot use less than its SoC.
			rep.SystemNote = "battery reports less energy than the SoC alone used; the device is on external power"
		case ok && rep.Seconds > 0 && covered >= rep.Seconds-tol:
			system, hasSystem, rep.SystemSource = j, true, domBattery
		default:
			rep.SystemNote = "on external power for part of the window; battery cannot measure system draw unless it is supplying the device"
		}
	}
	if !hasSystem {
		if j, ok := delta(a, b, domPsys); ok {
			if hasPkg && j < pkg {
				rep.SystemNote = "psys reads lower than the SoC package on this board; not usable as a system total"
			} else {
				system, hasSystem, rep.SystemSource = j, true, domPsys
				// Keep the reason the battery was not used, if there was
				// one: the reader should know this total is the estimate.
				if rep.SystemNote != "" {
					rep.SystemNote = "battery not used: " + rep.SystemNote
				}
			}
		} else if rep.SystemNote == "" {
			rep.SystemNote = "no whole-device power source on this device (no battery gauge, no psys)"
		}
	}

	// --- derived rows ---
	socOther, hasSocOther := 0.0, false
	if hasPkg && hasCore {
		socOther = pkg - core
		if hasGPU {
			socOther -= gpu
		}
		if hasNPU {
			socOther -= npu
		}
		socOther, hasSocOther = math.Max(socOther, 0), true
	}
	rest, hasRest := 0.0, false
	if hasSystem && hasPkg {
		rest = system - pkg
		if hasDRAM {
			rest -= dram
		}
		rest, hasRest = math.Max(rest, 0), true
	}

	ref := 0.0
	switch {
	case hasSystem:
		ref = system
	case hasPkg:
		ref = pkg
	}

	add := func(key, label string, j float64, ok, total bool) {
		l := Line{Key: key, Label: label, Available: ok, Total: total}
		if ok {
			l.Joules = j
			l.WattHours = j / 3600
			if rep.Seconds > 0 {
				l.Watts = j / rep.Seconds
			}
			if ref > 0 {
				l.Share = j / ref
			}
		}
		rep.Lines = append(rep.Lines, l)
	}
	add(KeyCPU, "CPU cores", core, hasCore, false)
	add(KeyGPU, "GPU", gpu, hasGPU, false)
	add(KeyNPU, "NPU", npu, hasNPU, false)
	// Some boards expose no "uncore" domain; the GPU's energy is then part
	// of the remainder and the label has to say so.
	otherLabel := "SoC other"
	if hasSocOther && !hasGPU {
		otherLabel = "SoC other+GPU"
	}
	add(KeySoCOther, otherLabel, socOther, hasSocOther, false)
	add(KeySoC, "SoC total", pkg, hasPkg, true)
	add(KeyDRAM, "DRAM", dram, hasDRAM, false)
	add(KeyRest, "Rest of system", rest, hasRest, false)
	add(KeySystem, "System total", system, hasSystem, true)
	return rep
}

// WithBaseline annotates the report with idle power taken from base (a
// Diff over an idle window) and the energy used above idle. Rows missing
// from the baseline are left without baseline figures.
func (r Report) WithBaseline(base Report) Report {
	out := r
	out.Lines = append([]Line(nil), r.Lines...)
	out.BaselineSeconds = base.Seconds
	for i, l := range out.Lines {
		bl, ok := base.Line(l.Key)
		if !ok || !bl.Available || !l.Available {
			continue
		}
		l.HasBaseline = true
		l.IdleWatts = bl.Watts
		l.NetJoules = l.Joules - bl.Watts*r.Seconds
		l.NetWattHours = l.NetJoules / 3600
		out.Lines[i] = l
	}
	return out
}

// FormatWh renders joules as watt-hours, switching to mWh below 1 Wh so
// short runs stay readable.
func FormatWh(joules float64) string {
	wh := joules / 3600
	switch {
	case math.Abs(wh) >= 1:
		return fmt.Sprintf("%.3f Wh", wh)
	default:
		return fmt.Sprintf("%.1f mWh", wh*1000)
	}
}

// FormatDuration renders an elapsed time compactly (1m05.2s, 12.3s).
func FormatDuration(d time.Duration) string {
	s := d.Seconds()
	if s >= 60 {
		return fmt.Sprintf("%dm%04.1fs", int(s)/60, math.Mod(s, 60))
	}
	return fmt.Sprintf("%.1fs", s)
}

// Table renders the report as fixed-width text. It is what --measure
// prints and what the viewers show.
func (r Report) Table() string {
	hasBase := r.BaselineSeconds > 0
	var b strings.Builder
	if hasBase {
		fmt.Fprintf(&b, "%-16s %12s %9s %6s %9s %12s\n", "", "energy", "avg W", "share", "idle W", "above idle")
	} else {
		fmt.Fprintf(&b, "%-16s %12s %9s %6s\n", "", "energy", "avg W", "share")
	}
	for _, l := range r.Lines {
		label := l.Label
		if !l.Total {
			label = "  " + label
		}
		if !l.Available {
			fmt.Fprintf(&b, "%-16s %12s\n", label, "n/a")
			continue
		}
		fmt.Fprintf(&b, "%-16s %12s %9.2f %5.0f%%", label, FormatWh(l.Joules), l.Watts, l.Share*100)
		if hasBase && l.HasBaseline {
			fmt.Fprintf(&b, " %9.2f %12s", l.IdleWatts, FormatWh(l.NetJoules))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Footnotes returns the caveats that apply to this report, one per line.
func (r Report) Footnotes() []string {
	var out []string
	switch r.SystemSource {
	case domBattery:
		out = append(out, "System total measured by the battery gauge (whole device, including display).")
	case domPsys:
		note := "System total from RAPL psys (platform estimate, about 3% high at idle and 8% low under load against a battery gauge; excludes power-supply losses)."
		if r.SystemNote != "" {
			note += " " + strings.ToUpper(r.SystemNote[:1]) + r.SystemNote[1:] + "."
		}
		out = append(out, note)
	default:
		if r.SystemNote != "" {
			out = append(out, "System total unavailable: "+r.SystemNote+".")
		}
	}
	out = append(out, "SoC figures are RAPL estimates; 'SoC other' is the package minus cores, GPU and NPU.")
	if r.BaselineSeconds > 0 {
		out = append(out, fmt.Sprintf("'above idle' subtracts idle power measured over %.0fs before the run.", r.BaselineSeconds))
	}
	return out
}
