package energy

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/module"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func reading(sec float64, j map[string]float64, batSec float64, hasBat bool) Reading {
	return Reading{Time: t0.Add(time.Duration(sec * float64(time.Second))), Joules: j, BatterySeconds: batSec, HasBattery: hasBat}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func line(t *testing.T, r Report, key string) Line {
	t.Helper()
	l, ok := r.Line(key)
	if !ok {
		t.Fatalf("no line %s", key)
	}
	return l
}

func TestFromSample(t *testing.T) {
	s := collector.Sample{Timestamp: t0, Metrics: map[string][]module.Metric{
		"power": {
			{Name: "energy", Value: 100, Unit: "J", Labels: map[string]string{"domain": "package"}},
			{Name: "power", Value: 5, Unit: "W", Labels: map[string]string{"domain": "package"}},
			{Name: "energy", Value: 40, Unit: "J", Labels: map[string]string{"domain": "battery"}},
			{Name: "measured_seconds", Value: 7, Unit: "s", Labels: map[string]string{"domain": "battery"}},
			{Name: "battery_capacity", Value: 80, Unit: "%"},
		},
		"npu": {{Name: "energy", Value: 3, Unit: "J", Labels: map[string]string{"domain": "npu"}}},
		"cpu": {{Name: "power", Value: 5, Unit: "W"}},
	}}
	r := FromSample(s)
	if !r.Available() || r.Joules["package"] != 100 || r.Joules["npu"] != 3 || r.Joules["battery"] != 40 {
		t.Fatalf("joules = %v", r.Joules)
	}
	if !r.HasBattery || r.BatterySeconds != 7 {
		t.Fatalf("battery fields: %+v", r)
	}
	if FromSample(collector.Sample{}).Available() {
		t.Fatal("empty sample reported as available")
	}
}

func TestDiffBreakdownSumsToTotals(t *testing.T) {
	a := reading(0, map[string]float64{"package": 0, "core": 0, "uncore": 0, "npu": 0, "dram": 0, "battery": 0}, 0, true)
	b := reading(100, map[string]float64{"package": 1000, "core": 600, "uncore": 200, "npu": 50, "dram": 100, "battery": 3600}, 100, true)
	r := Diff(a, b)
	if r.SystemSource != "battery" {
		t.Fatalf("source = %q (%s)", r.SystemSource, r.SystemNote)
	}
	if l := line(t, r, KeySoCOther); !near(l.Joules, 150) {
		t.Fatalf("soc other = %v, want 150", l.Joules)
	}
	if l := line(t, r, KeyRest); !near(l.Joules, 2500) {
		t.Fatalf("rest = %v, want 2500", l.Joules)
	}
	sys := line(t, r, KeySystem)
	if !near(sys.WattHours, 1) || !near(sys.Watts, 36) || !near(sys.Share, 1) {
		t.Fatalf("system line = %+v", sys)
	}
	// Components sum to the totals with no double counting.
	soc := line(t, r, KeyCPU).Joules + line(t, r, KeyGPU).Joules + line(t, r, KeyNPU).Joules + line(t, r, KeySoCOther).Joules
	if !near(soc, line(t, r, KeySoC).Joules) {
		t.Fatalf("SoC components sum to %v, SoC total %v", soc, line(t, r, KeySoC).Joules)
	}
	all := line(t, r, KeySoC).Joules + line(t, r, KeyDRAM).Joules + line(t, r, KeyRest).Joules
	if !near(all, sys.Joules) {
		t.Fatalf("rows sum to %v, system total %v", all, sys.Joules)
	}
	if l := line(t, r, KeyCPU); !near(l.Share, 600.0/3600) {
		t.Fatalf("cpu share = %v", l.Share)
	}
}

func TestDiffOnACHasNoSystemTotal(t *testing.T) {
	a := reading(0, map[string]float64{"package": 0, "core": 0, "battery": 500}, 50, true)
	// 100 s elapsed but the battery only measured 40 s of it.
	b := reading(100, map[string]float64{"package": 800, "core": 500, "battery": 900}, 90, true)
	r := Diff(a, b)
	if r.SystemSource != "" || line(t, r, KeySystem).Available || line(t, r, KeyRest).Available {
		t.Fatalf("system should be unavailable: %+v", r)
	}
	if !strings.Contains(r.SystemNote, "external power") {
		t.Fatalf("note = %q", r.SystemNote)
	}
	// Shares fall back to the SoC total.
	if l := line(t, r, KeyCPU); !near(l.Share, 500.0/800) {
		t.Fatalf("cpu share = %v", l.Share)
	}
}

func TestDiffPsysFallbackAndSanity(t *testing.T) {
	a := reading(0, map[string]float64{"package": 0, "core": 0, "psys": 0}, 0, false)
	b := reading(10, map[string]float64{"package": 100, "core": 60, "psys": 250}, 0, false)
	if r := Diff(a, b); r.SystemSource != "psys" || !near(line(t, r, KeyRest).Joules, 150) {
		t.Fatalf("psys fallback: %+v", r)
	}
	// psys lower than package is not a system total.
	b.Joules["psys"] = 40
	r := Diff(a, b)
	if r.SystemSource != "" || !strings.Contains(r.SystemNote, "psys reads lower") {
		t.Fatalf("implausible psys accepted: %q %q", r.SystemSource, r.SystemNote)
	}
}

func TestDiffNoSystemSourceAndMissingDomains(t *testing.T) {
	a := reading(0, map[string]float64{"package": 10}, 0, false)
	b := reading(5, map[string]float64{"package": 60}, 0, false)
	r := Diff(a, b)
	if line(t, r, KeyCPU).Available || line(t, r, KeySoCOther).Available || line(t, r, KeyDRAM).Available {
		t.Fatal("rows without a source should be unavailable")
	}
	if l := line(t, r, KeySoC); !l.Available || !near(l.Watts, 10) {
		t.Fatalf("soc = %+v", l)
	}
	if !strings.Contains(r.SystemNote, "no whole-device") {
		t.Fatalf("note = %q", r.SystemNote)
	}
	if !strings.Contains(r.Table(), "n/a") {
		t.Fatal("table should mark unavailable rows")
	}
}

func TestDaemonRestartInvalidatesDomain(t *testing.T) {
	a := reading(0, map[string]float64{"package": 500, "core": 300}, 0, false)
	b := reading(10, map[string]float64{"package": 20, "core": 10}, 0, false) // counters reset
	r := Diff(a, b)
	if line(t, r, KeySoC).Available || line(t, r, KeyCPU).Available {
		t.Fatal("negative delta should invalidate the row")
	}
}

func TestWithBaseline(t *testing.T) {
	idleA := reading(0, map[string]float64{"package": 0, "core": 0}, 0, false)
	idleB := reading(10, map[string]float64{"package": 30, "core": 10}, 0, false) // 3 W, 1 W idle
	base := Diff(idleA, idleB)
	a := reading(100, map[string]float64{"package": 100, "core": 50}, 0, false)
	b := reading(120, map[string]float64{"package": 400, "core": 250}, 0, false) // 300 J, 200 J in 20 s
	r := Diff(a, b).WithBaseline(base)
	soc := line(t, r, KeySoC)
	if !soc.HasBaseline || !near(soc.IdleWatts, 3) || !near(soc.NetJoules, 240) {
		t.Fatalf("soc baseline = %+v", soc)
	}
	if cpu := line(t, r, KeyCPU); !near(cpu.NetJoules, 180) {
		t.Fatalf("cpu net = %v, want 180", cpu.NetJoules)
	}
	if !strings.Contains(r.Table(), "above idle") {
		t.Fatal("table missing baseline columns")
	}
	if got := len(r.Footnotes()); got < 3 {
		t.Fatalf("footnotes = %d", got)
	}
}

func TestFormat(t *testing.T) {
	if got := FormatWh(3600); got != "1.000 Wh" {
		t.Errorf("FormatWh(3600) = %q", got)
	}
	if got := FormatWh(45); got != "12.5 mWh" {
		t.Errorf("FormatWh(45) = %q", got)
	}
	if got := FormatDuration(65200 * time.Millisecond); got != "1m05.2s" {
		t.Errorf("FormatDuration = %q", got)
	}
	if got := FormatDuration(12300 * time.Millisecond); got != "12.3s" {
		t.Errorf("FormatDuration = %q", got)
	}
}

func TestSessionStopwatchAndBaseline(t *testing.T) {
	s := NewSession(10 * time.Second)
	s.Toggle() // no readings yet: ignored
	if s.Supported() || s.State() != Idle {
		t.Fatal("session started without data")
	}
	mk := func(sec, pkg float64) Reading {
		return reading(sec, map[string]float64{"package": pkg, "core": pkg / 2}, 0, false)
	}
	s.Observe(Reading{}) // unavailable reading ignored
	s.Observe(mk(0, 0))
	if !s.Supported() {
		t.Fatal("not supported after a reading")
	}

	// Baseline: 2 W idle over 10 s.
	s.StartBaseline()
	s.Observe(mk(5, 10))
	if cap, rem, _ := s.Baseline(); !cap || rem != 5*time.Second {
		t.Fatalf("baseline capture state: %v %v", cap, rem)
	}
	s.Observe(mk(10, 20))
	cap, _, base := s.Baseline()
	if cap || base == nil || !near(line(t, *base, KeySoC).Watts, 2) {
		t.Fatalf("baseline not captured: %v %+v", cap, base)
	}

	// Measure 20 s at 12 W.
	s.Toggle()
	if s.State() != Running {
		t.Fatal("not running")
	}
	s.Observe(mk(20, 140))
	live, ok := s.Report()
	if !ok || !near(line(t, live, KeySoC).Joules, 120) {
		t.Fatalf("live report: %+v", live)
	}
	s.Observe(mk(30, 260))
	s.Toggle()
	if s.State() != Stopped {
		t.Fatal("not stopped")
	}
	s.Observe(mk(40, 999)) // later samples do not change a frozen result
	final, _ := s.Report()
	soc := line(t, final, KeySoC)
	if !near(soc.Joules, 240) || !near(soc.NetJoules, 200) || final.Seconds != 20 {
		t.Fatalf("final = %+v (seconds %v)", soc, final.Seconds)
	}

	// Toggling again starts fresh from the latest reading.
	s.Toggle()
	fresh, _ := s.Report()
	if s.State() != Running || fresh.Seconds != 0 {
		t.Fatalf("restart: state %v seconds %v", s.State(), fresh.Seconds)
	}
	s.Reset()
	if _, ok := s.Report(); ok {
		t.Fatal("report available after reset")
	}
	s.ClearBaseline()
	if _, _, b := s.Baseline(); b != nil {
		t.Fatal("baseline survived ClearBaseline")
	}
}

func TestSoCOtherLabelWhenGPUDomainMissing(t *testing.T) {
	a := reading(0, map[string]float64{"package": 0, "core": 0}, 0, false)
	b := reading(10, map[string]float64{"package": 100, "core": 60}, 0, false)
	r := Diff(a, b)
	if l := line(t, r, KeySoCOther); l.Label != "SoC other+GPU" || !near(l.Joules, 40) {
		t.Fatalf("soc other = %+v", l)
	}
	if line(t, r, KeyGPU).Available {
		t.Fatal("GPU row should be unavailable without an uncore domain")
	}
}

func TestBatteryBelowSoCIsNotASystemTotal(t *testing.T) {
	a := reading(0, map[string]float64{"package": 0, "core": 0, "battery": 0}, 0, true)
	// Full coverage claimed, but 15 J from the battery against 460 J in the SoC.
	b := reading(30, map[string]float64{"package": 460, "core": 360, "battery": 15}, 30, true)
	r := Diff(a, b)
	if r.SystemSource != "" || line(t, r, KeySystem).Available {
		t.Fatalf("implausible battery total accepted: %+v", r)
	}
	if !strings.Contains(r.SystemNote, "less energy than the SoC") {
		t.Fatalf("note = %q", r.SystemNote)
	}
	if l := line(t, r, KeyCPU); l.Share > 1 {
		t.Fatalf("share = %v, must be relative to the SoC total", l.Share)
	}
}

// A battery device that was plugged in for part of the window must fall
// back to psys and say why (seen live on a laptop when the cable came out
// mid-run).
func TestBatteryInterruptedFallsBackToPsysWithReason(t *testing.T) {
	a := reading(0, map[string]float64{"package": 0, "core": 0, "psys": 0, "battery": 0}, 0, true)
	b := reading(60, map[string]float64{"package": 108, "core": 60, "psys": 405, "battery": 300}, 48, true)
	r := Diff(a, b)
	if r.SystemSource != "psys" || !line(t, r, KeySystem).Available {
		t.Fatalf("expected psys fallback: %+v", r)
	}
	if !strings.Contains(r.SystemNote, "battery not used") {
		t.Fatalf("note = %q", r.SystemNote)
	}
	joined := strings.Join(r.Footnotes(), " ")
	if !strings.Contains(joined, "psys") || !strings.Contains(joined, "Battery not used") {
		t.Fatalf("footnotes = %v", r.Footnotes())
	}
}
