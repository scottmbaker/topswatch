package power

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottmbaker/topswatch/internal/module"
)

func write(t *testing.T, root, rel, val string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(val+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func raplZone(t *testing.T, root, zone, name string, uj string) {
	write(t, root, "class/powercap/"+zone+"/name", name)
	write(t, root, "class/powercap/"+zone+"/energy_uj", uj)
	write(t, root, "class/powercap/"+zone+"/max_energy_range_uj", "1000000000")
}

func get(ms []module.Metric, name, domain string) (float64, bool) {
	for _, m := range ms {
		if m.Name == name && m.Labels["domain"] == domain {
			return m.Value, true
		}
	}
	return 0, false
}

func getPlain(ms []module.Metric, name string) (float64, bool) {
	for _, m := range ms {
		if m.Name == name && len(m.Labels) == 0 {
			return m.Value, true
		}
	}
	return 0, false
}

// clock is a controllable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestModule(root string) (*Module, *clock) {
	c := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	m := NewWithRoot(root)
	m.now = c.now
	return m, c
}

func TestNothingAvailableIsAnInitError(t *testing.T) {
	m, _ := newTestModule(t.TempDir())
	if err := m.Init(); err == nil {
		t.Fatal("expected Init error with no RAPL and no battery")
	}
}

func TestRAPLDomainsAccumulateAndWrap(t *testing.T) {
	root := t.TempDir()
	raplZone(t, root, "intel-rapl:0", "package-0", "999000000") // 1 J below max
	raplZone(t, root, "intel-rapl:0:0", "core", "5000000")
	raplZone(t, root, "intel-rapl:0:1", "uncore", "0")
	raplZone(t, root, "intel-rapl:0:2", "dram", "0")
	raplZone(t, root, "intel-rapl:1", "psys", "0")
	raplZone(t, root, "intel-rapl-mmio:0", "package-0", "123") // must be ignored

	m, c := newTestModule(root)
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	if got := m.DeviceInfo().Extra["domains"]; got != "package,core,uncore,dram,psys" {
		t.Fatalf("domains = %q", got)
	}
	if got := m.DeviceInfo().Extra["system_source"]; got != "psys" {
		t.Fatalf("system_source = %q", got)
	}

	ms, _ := m.Collect() // primes baselines
	if v, ok := get(ms, "energy", "package"); !ok || v != 0 {
		t.Fatalf("first sample package energy = %v %v, want 0", v, ok)
	}
	if _, ok := get(ms, "power", "package"); ok {
		t.Fatal("power emitted before a delta exists")
	}

	// +2 s: package wraps (999 -> 3 J past zero = 4 J), core +10 J.
	c.advance(2 * time.Second)
	write(t, root, "class/powercap/intel-rapl:0/energy_uj", "3000000")
	write(t, root, "class/powercap/intel-rapl:0:0/energy_uj", "15000000")
	write(t, root, "class/powercap/intel-rapl:1/energy_uj", "30000000")
	ms, _ = m.Collect()
	if v, _ := get(ms, "energy", "package"); v != 4 {
		t.Fatalf("package energy after wrap = %v, want 4", v)
	}
	if v, _ := get(ms, "power", "package"); v != 2 {
		t.Fatalf("package power = %v, want 2 W", v)
	}
	if v, _ := get(ms, "energy", "core"); v != 10 {
		t.Fatalf("core energy = %v, want 10", v)
	}
	if v, _ := get(ms, "energy", "psys"); v != 30 {
		t.Fatalf("psys energy = %v, want 30", v)
	}
	if _, ok := get(ms, "energy", "battery"); ok {
		t.Fatal("battery metrics emitted without a battery")
	}

	// Energy keeps accumulating.
	c.advance(time.Second)
	write(t, root, "class/powercap/intel-rapl:0/energy_uj", "4500000")
	ms, _ = m.Collect()
	if v, _ := get(ms, "energy", "package"); v != 5.5 {
		t.Fatalf("package energy = %v, want 5.5", v)
	}
}

func setBattery(t *testing.T, root, status, uA, uV string) {
	write(t, root, "class/power_supply/BAT1/type", "Battery")
	write(t, root, "class/power_supply/BAT1/present", "1")
	write(t, root, "class/power_supply/BAT1/model_name", "TESTPACK")
	write(t, root, "class/power_supply/BAT1/status", status)
	write(t, root, "class/power_supply/BAT1/capacity", "80")
	write(t, root, "class/power_supply/BAT1/current_now", uA)
	write(t, root, "class/power_supply/BAT1/voltage_now", uV)
}

func TestBatteryOnlyCountsWhileDischarging(t *testing.T) {
	root := t.TempDir()
	write(t, root, "class/power_supply/ADP1/type", "Mains")
	setBattery(t, root, "Discharging", "2000000", "12000000") // 24 W
	m, c := newTestModule(root)
	if err := m.Init(); err != nil {
		t.Fatalf("battery-only device should init: %v", err)
	}
	if got := m.DeviceInfo().Extra["system_source"]; got != "battery" {
		t.Fatalf("system_source = %q", got)
	}

	ms, _ := m.Collect()
	if v, _ := get(ms, "power", "battery"); v != 24 {
		t.Fatalf("battery power = %v, want 24", v)
	}
	if v, _ := getPlain(ms, "battery_discharging"); v != 1 {
		t.Fatal("battery_discharging should be 1")
	}
	if v, _ := getPlain(ms, "battery_capacity"); v != 80 {
		t.Fatalf("capacity = %v", v)
	}

	// 10 s at 24 W -> 36 W: trapezoid = 300 J.
	c.advance(10 * time.Second)
	setBattery(t, root, "Discharging", "3000000", "12000000")
	ms, _ = m.Collect()
	if v, _ := get(ms, "energy", "battery"); math.Abs(v-300) > 1e-9 {
		t.Fatalf("battery energy = %v, want 300", v)
	}
	if v, _ := get(ms, "measured_seconds", "battery"); v != 10 {
		t.Fatalf("measured_seconds = %v, want 10", v)
	}

	// Plugged in: no power metric, energy and coverage stop advancing.
	c.advance(10 * time.Second)
	setBattery(t, root, "Charging", "1500000", "12500000")
	ms, _ = m.Collect()
	if _, ok := get(ms, "power", "battery"); ok {
		t.Fatal("battery power emitted while charging")
	}
	if v, _ := getPlain(ms, "battery_discharging"); v != 0 {
		t.Fatal("battery_discharging should be 0 on AC")
	}
	if v, _ := get(ms, "energy", "battery"); math.Abs(v-300) > 1e-9 {
		t.Fatalf("energy advanced while charging: %v", v)
	}
	if v, _ := get(ms, "measured_seconds", "battery"); v != 10 {
		t.Fatalf("coverage advanced while charging: %v", v)
	}

	// Unplugged again: the charging gap is not integrated across.
	c.advance(10 * time.Second)
	setBattery(t, root, "Discharging", "2000000", "12000000")
	ms, _ = m.Collect()
	if v, _ := get(ms, "energy", "battery"); math.Abs(v-300) > 1e-9 {
		t.Fatalf("energy integrated across the AC gap: %v", v)
	}
	c.advance(5 * time.Second)
	ms, _ = m.Collect()
	if v, _ := get(ms, "energy", "battery"); math.Abs(v-420) > 1e-9 {
		t.Fatalf("energy = %v, want 420 (300 + 5 s x 24 W)", v)
	}
}

func TestBatteryWithPowerNowAndNegativeSign(t *testing.T) {
	root := t.TempDir()
	write(t, root, "class/power_supply/BAT0/type", "Battery")
	write(t, root, "class/power_supply/BAT0/status", "Discharging")
	write(t, root, "class/power_supply/BAT0/power_now", "-15000000") // some drivers sign it
	m, _ := newTestModule(root)
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	ms, _ := m.Collect()
	if v, _ := get(ms, "power", "battery"); v != 15 {
		t.Fatalf("power = %v, want 15", v)
	}
}

func TestPeripheralBatteryIgnored(t *testing.T) {
	root := t.TempDir()
	write(t, root, "class/power_supply/hid-mouse/type", "Battery")
	write(t, root, "class/power_supply/hid-mouse/scope", "Device")
	write(t, root, "class/power_supply/hid-mouse/voltage_now", "3000000")
	write(t, root, "class/power_supply/hid-mouse/current_now", "1000")
	raplZone(t, root, "intel-rapl:0", "package-0", "0")
	m, _ := newTestModule(root)
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	if m.bat != nil {
		t.Fatal("peripheral battery was used as the system battery")
	}
	if got := m.DeviceInfo().Extra["system_source"]; got != "none" {
		t.Fatalf("system_source = %q, want none", got)
	}
}

// A counter that wraps earlier than max_energy_range_uj advertises (or
// resets across suspend) must not inject a huge phantom delta.
func TestImplausibleJumpIsDropped(t *testing.T) {
	root := t.TempDir()
	write(t, root, "class/powercap/intel-rapl:1/name", "psys")
	write(t, root, "class/powercap/intel-rapl:1/energy_uj", "4294000000")
	write(t, root, "class/powercap/intel-rapl:1/max_energy_range_uj", "262143328850")
	m, c := newTestModule(root)
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Collect()
	c.advance(time.Second)
	write(t, root, "class/powercap/intel-rapl:1/energy_uj", "4294900000") // +0.9 J
	_, _ = m.Collect()
	c.advance(time.Second)
	write(t, root, "class/powercap/intel-rapl:1/energy_uj", "500000") // wrapped at 32 bits
	ms, _ := m.Collect()
	if v, _ := get(ms, "energy", "psys"); v != 0.9 {
		t.Fatalf("energy after early wrap = %v, want 0.9 (bad tick dropped)", v)
	}
	if v, _ := get(ms, "power", "psys"); v != 0 {
		t.Fatalf("power on the dropped tick = %v, want 0", v)
	}
	c.advance(time.Second)
	write(t, root, "class/powercap/intel-rapl:1/energy_uj", "30500000") // +30 J, normal again
	ms, _ = m.Collect()
	if v, _ := get(ms, "energy", "psys"); v != 30.9 {
		t.Fatalf("energy = %v, want 30.9", v)
	}
}

// Seen on a real device: gauge says "Discharging" with the adapter flagged
// offline, but the battery supplies a fraction of a watt while the SoC
// draws several. That is external power, and must not be counted as
// system draw.
func TestDischargingStatusWithoutRealDischarge(t *testing.T) {
	root := t.TempDir()
	raplZone(t, root, "intel-rapl:0", "package-0", "0")
	setBattery(t, root, "Discharging", "25000", "12500000") // 0.31 W
	m, c := newTestModule(root)
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	pkg := 0
	step := func() []module.Metric {
		c.advance(time.Second)
		pkg += 8000000 // SoC at 8 W
		write(t, root, "class/powercap/intel-rapl:0/energy_uj", itoa(pkg))
		ms, _ := m.Collect()
		return ms
	}
	_, _ = m.Collect()
	// The very first sample with a package reading already displays the
	// truth, which is what a one-shot --text run (two samples) relies on.
	ms := step()
	if v, _ := getPlain(ms, "battery_discharging"); v != 0 {
		t.Fatal("first comparable sample still shown as discharging")
	}
	if _, ok := get(ms, "power", "battery"); ok {
		t.Fatal("first comparable sample still shows battery as system power")
	}
	for i := 0; i < 5; i++ {
		ms = step()
	}
	if v, _ := getPlain(ms, "battery_discharging"); v != 0 {
		t.Fatal("battery reported as powering the device while supplying 0.3 W against an 8 W SoC")
	}
	if _, ok := get(ms, "power", "battery"); ok {
		t.Fatal("battery power emitted as system power")
	}
	covered, _ := get(ms, "measured_seconds", "battery")
	if covered > 2 {
		t.Fatalf("battery coverage kept advancing: %v s over 6 s", covered)
	}

	// Really on battery now (25 W against the 8 W SoC): counted again.
	setBattery(t, root, "Discharging", "2000000", "12500000")
	step()
	ms = step()
	if v, _ := getPlain(ms, "battery_discharging"); v != 1 {
		t.Fatal("real discharge not recognised")
	}
	if v, _ := get(ms, "power", "battery"); v != 25 {
		t.Fatalf("battery power = %v, want 25", v)
	}
	if after, _ := get(ms, "measured_seconds", "battery"); after <= covered {
		t.Fatal("coverage did not resume")
	}
}

// A gauge lagging a sudden load step by a sample or two must not be
// mistaken for external power.
func TestGaugeLagDoesNotInterruptCoverage(t *testing.T) {
	root := t.TempDir()
	raplZone(t, root, "intel-rapl:0", "package-0", "0")
	setBattery(t, root, "Discharging", "1000000", "12000000") // 12 W
	m, c := newTestModule(root)
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Collect()
	pkg := 0
	var ms []module.Metric
	for i, soc := range []int{5, 40, 40, 40, 40} { // load step; gauge catches up on the 4th sample
		c.advance(time.Second)
		pkg += soc * 1000000
		write(t, root, "class/powercap/intel-rapl:0/energy_uj", itoa(pkg))
		if i == 3 {
			setBattery(t, root, "Discharging", "4500000", "12000000") // 54 W
		}
		ms, _ = m.Collect()
	}
	if v, _ := get(ms, "measured_seconds", "battery"); v != 5 {
		t.Fatalf("coverage = %v s, want 5 (two lagging samples must be tolerated)", v)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
