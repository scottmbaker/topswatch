// Package power collects energy counters: every RAPL domain the platform
// exposes (package, core, uncore, dram, psys) and, on battery-powered
// devices, whole-system power from the battery fuel gauge.
//
// It is adaptive by design. Each source is optional: a desktop without a
// battery, a board without psys, or a kernel that hides RAPL simply yields
// fewer domains. Nothing here is an error at collection time; a source
// that cannot be read is skipped.
//
// Energy is reported as joules accumulated since the daemon started, so a
// client can bracket any workload by subtracting two readings. All
// per-domain metrics carry a "domain" label.
package power

import (
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/scottmbaker/topswatch/internal/collectors/socpmt"
	"github.com/scottmbaker/topswatch/internal/module"
	"github.com/scottmbaker/topswatch/internal/sysfile"
)

// Domain names used in the "domain" label.
const (
	DomainPackage = "package" // whole SoC: cores + uncore + everything else on the die
	DomainCore    = "core"    // CPU cores
	DomainUncore  = "uncore"  // integrated GPU on Intel client SoCs
	DomainDRAM    = "dram"    // memory
	DomainPsys    = "psys"    // platform, where the board reports it
	DomainBattery = "battery" // whole device, measured by the fuel gauge while discharging
)

type zone struct {
	domain   string
	energy   *sysfile.File // RAPL sysfs energy_uj, held open; nil for a PMT-backed zone
	maxRange uint64
	prev     uint64
	prevSet  bool
	warned   bool
	// pmt, when set, supplies the GT (GPU) energy from the SoC telemetry
	// region instead of sysfs. Same counter as the uncore MSR; used when
	// the kernel registers no RAPL uncore zone.
	pmt *socpmt.Device
}

// readUJ returns the zone's cumulative energy in microjoules.
func (z *zone) readUJ() (uint64, error) {
	if z.pmt != nil {
		j, err := z.pmt.GTEnergyJoules()
		return uint64(j * 1e6), err
	}
	return z.energy.Uint()
}

// A fuel gauge's "Discharging" status is not proof that the battery is
// powering the device. Seen on real hardware: a full battery on a board
// fed through a port the firmware does not report as the AC adapter says
// "Discharging" while supplying a fraction of a watt, with the SoC alone
// drawing 7-15 W. Battery power can never be less than what the SoC
// draws, so a battery reading below supplyingFraction of package power
// for notSupplyingAfter consecutive samples means the system is on
// external power. The run length absorbs the gauge lagging a sudden load
// step by a sample or two.
const (
	supplyingFraction = 0.5
	notSupplyingAfter = 3
)

// maxPlausibleWatts bounds a single-tick energy delta. A larger jump means
// the counter did not behave as advertised (reset across suspend, or a
// register that wraps earlier than max_energy_range_uj says), not that a
// client SoC drew kilowatts. Such a tick is dropped rather than folded
// into the total, so one bad reading cannot corrupt a long measurement.
const maxPlausibleWatts = 2000.0

type battery struct {
	dir   string
	name  string
	model string

	// Attributes held open; power or current may be nil (one of the two
	// exists), the rest are optional too.
	status, capacity, voltage, power, current *sysfile.File

	prevWatts       float64
	prevTime        time.Time
	prevDischarging bool

	// below counts consecutive samples in which the battery reported far
	// less power than the SoC alone was drawing (see notSupplyingAfter).
	below  int
	warned bool

	joules  float64 // accumulated while discharging
	seconds float64 // time covered by that accumulation
}

// Module implements module.Module.
type Module struct {
	sysRoot string
	now     func() time.Time

	zones    []*zone
	joules   map[string]float64 // per-domain accumulated energy
	prevTime time.Time
	bat      *battery
}

// New returns a module reading the real /sys.
func New() *Module { return NewWithRoot("/sys") }

// NewWithRoot returns a module reading a sysfs tree rooted elsewhere
// (tests).
func NewWithRoot(root string) *Module {
	return &Module{sysRoot: root, now: time.Now, joules: map[string]float64{}}
}

func (m *Module) Name() string { return "power" }

func (m *Module) Init() error {
	m.discoverRAPL()
	m.discoverBattery()

	if len(m.zones) == 0 && m.bat == nil {
		return fmt.Errorf("no readable RAPL domains and no battery")
	}
	if d := m.domains(); len(d) > 0 {
		log.Printf("[power] RAPL domains: %s", strings.Join(d, " "))
	}
	switch {
	case m.bat != nil:
		log.Printf("[power] battery %s (%s): system power while discharging", m.bat.name, m.bat.model)
	case m.hasDomain(DomainPsys):
		log.Printf("[power] no battery; system power from RAPL psys")
	default:
		log.Printf("[power] no battery and no psys; system total unavailable")
	}
	return nil
}

func (m *Module) discoverRAPL() {
	// "intel-rapl:*" matches both top-level zones (intel-rapl:0) and
	// sub-zones (intel-rapl:0:1). The intel-rapl-mmio duplicates are not
	// matched.
	paths, _ := filepath.Glob(filepath.Join(m.sysRoot, "class/powercap/intel-rapl:*"))
	sort.Strings(paths)
	var unreadable []string
	for _, p := range paths {
		nameBytes, err := os.ReadFile(filepath.Join(p, "name"))
		if err != nil {
			continue
		}
		domain := strings.TrimSpace(string(nameBytes))
		if strings.HasPrefix(domain, "package-") {
			domain = DomainPackage
		}
		f, err := sysfile.OpenUint(filepath.Join(p, "energy_uj"))
		if err != nil {
			unreadable = append(unreadable, domain)
			continue
		}
		maxRange, _ := sysfile.ReadUint(filepath.Join(p, "max_energy_range_uj"))
		m.zones = append(m.zones, &zone{domain: domain, energy: f, maxRange: maxRange})
	}
	if len(unreadable) > 0 {
		log.Printf("[power] RAPL domains not readable (need root): %s", strings.Join(unreadable, " "))
	}
	// Some boards' kernels never register the uncore zone although the
	// hardware counter works. Intel's SoC telemetry carries the same GT
	// energy counter; fall back to it so the GPU row of the energy
	// breakdown does not silently become part of "SoC other".
	if !m.hasDomain(DomainUncore) {
		if d, err := socpmt.OpenUnder(filepath.Join(m.sysRoot, "class/intel_pmt")); err == nil {
			m.zones = append(m.zones, &zone{domain: DomainUncore, pmt: d})
			log.Printf("[power] uncore energy from PMT GT counter (%s); no RAPL uncore zone", d.Generation())
		}
	}
}

func (m *Module) discoverBattery() {
	dirs, _ := filepath.Glob(filepath.Join(m.sysRoot, "class/power_supply/*"))
	sort.Strings(dirs)
	for _, d := range dirs {
		if readString(filepath.Join(d, "type")) != "Battery" {
			continue
		}
		// Peripheral batteries (mice, headsets) report scope=Device.
		if readString(filepath.Join(d, "scope")) == "Device" {
			continue
		}
		if p := readString(filepath.Join(d, "present")); p != "" && p != "1" {
			continue
		}
		// Need either power_now, or voltage_now and current_now.
		b := &battery{dir: d, name: filepath.Base(d), model: readString(filepath.Join(d, "model_name"))}
		b.power = openInt(filepath.Join(d, "power_now"))
		b.voltage = openInt(filepath.Join(d, "voltage_now"))
		b.current = openInt(filepath.Join(d, "current_now"))
		if b.power == nil && (b.voltage == nil || b.current == nil) {
			b.close()
			continue
		}
		b.status, _ = sysfile.Open(filepath.Join(d, "status"))
		b.capacity = openInt(filepath.Join(d, "capacity"))
		m.bat = b
		return
	}
}

func (m *Module) domains() []string {
	seen := map[string]bool{}
	var out []string
	for _, z := range m.zones {
		if !seen[z.domain] {
			seen[z.domain] = true
			out = append(out, z.domain)
		}
	}
	return out
}

func (m *Module) hasDomain(d string) bool {
	for _, z := range m.zones {
		if z.domain == d {
			return true
		}
	}
	return false
}

func (m *Module) DeviceInfo() module.DeviceInfo {
	info := module.DeviceInfo{Name: "Power and energy", Extra: map[string]string{}}
	info.Extra["domains"] = strings.Join(m.domains(), ",")
	switch {
	case m.bat != nil:
		info.Extra["system_source"] = DomainBattery
		info.Extra["battery"] = m.bat.name
		info.Extra["battery_model"] = m.bat.model
	case m.hasDomain(DomainPsys):
		info.Extra["system_source"] = DomainPsys
	default:
		info.Extra["system_source"] = "none"
	}
	return info
}

func (m *Module) Collect() ([]module.Metric, error) {
	now := m.now()
	var metrics []module.Metric

	// --- RAPL ---
	dT := 0.0
	if !m.prevTime.IsZero() {
		dT = now.Sub(m.prevTime).Seconds()
	}
	deltaJ := map[string]float64{}
	for _, z := range m.zones {
		uj, err := z.readUJ()
		if err != nil {
			continue
		}
		if z.prevSet {
			var d uint64
			if uj >= z.prev {
				d = uj - z.prev
			} else if z.maxRange > 0 {
				d = (z.maxRange - z.prev) + uj
			}
			dj := float64(d) / 1e6
			if dT > 0 && dj/dT > maxPlausibleWatts {
				if !z.warned {
					log.Printf("[power] %s: implausible energy jump (%.0f J in %.1fs); counter reset or early wrap, dropping such ticks", z.domain, dj, dT)
					z.warned = true
				}
				dj = 0
			}
			deltaJ[z.domain] += dj
		} else if _, ok := deltaJ[z.domain]; !ok {
			deltaJ[z.domain] = 0
		}
		z.prev = uj
		z.prevSet = true
	}
	m.prevTime = now
	for _, d := range m.domains() {
		dj, ok := deltaJ[d]
		if !ok {
			continue
		}
		m.joules[d] += dj
		lbl := map[string]string{"domain": d}
		metrics = append(metrics, module.Metric{Name: "energy", Value: m.joules[d], Unit: "J", Labels: lbl})
		if dT > 0 {
			metrics = append(metrics, module.Metric{Name: "power", Value: dj / dT, Unit: "W", Labels: lbl})
		}
	}

	// --- Battery ---
	if m.bat != nil {
		pkgWatts, havePkg := 0.0, false
		if dj, ok := deltaJ[DomainPackage]; ok && dT > 0 {
			pkgWatts, havePkg = dj/dT, true
		}
		metrics = append(metrics, m.collectBattery(now, pkgWatts, havePkg)...)
	}
	return metrics, nil
}

// collectBattery reads the fuel gauge. Battery current only equals system
// draw while the battery is actually supplying the device; on external
// power it reflects charging (or nothing), so power and energy are
// reported only while it is supplying. pkgWatts is the SoC package power
// for the same interval, used to sanity-check the gauge's status.
func (m *Module) collectBattery(now time.Time, pkgWatts float64, havePkg bool) []module.Metric {
	b := m.bat
	var out []module.Metric

	status := ""
	if b.status != nil {
		status, _ = b.status.String()
	}
	discharging := status == "Discharging"
	if c, ok := heldInt(b.capacity); ok {
		out = append(out, module.Metric{Name: "battery_capacity", Value: float64(c), Unit: "%"})
	}
	volts := math.NaN()
	if v, ok := heldInt(b.voltage); ok {
		volts = float64(v) / 1e6
		out = append(out, module.Metric{Name: "battery_voltage", Value: volts, Unit: "V"})
	}

	watts, ok := 0.0, false
	if p, pok := heldInt(b.power); pok {
		watts, ok = math.Abs(float64(p))/1e6, true
	} else if i, iok := heldInt(b.current); iok && !math.IsNaN(volts) {
		watts, ok = volts*math.Abs(float64(i))/1e6, true
	}

	// Is the battery really carrying the system? Two answers are kept:
	//   - supplying: this sample looks like real discharge. Drives what is
	//     displayed (the power metric and battery_discharging), so even a
	//     one-shot --text run is right.
	//   - counting: energy keeps accumulating. Tolerates a short run of
	//     implausible samples so a gauge lagging a load step does not
	//     punch a hole in a measurement's coverage.
	plausible := !(havePkg && pkgWatts > 0 && watts < supplyingFraction*pkgWatts)
	if discharging && ok && !plausible {
		b.below++
	} else {
		b.below = 0
	}
	supplying := discharging && ok && plausible
	counting := discharging && ok && b.below < notSupplyingAfter
	if b.below >= notSupplyingAfter && !b.warned {
		log.Printf("[power] battery %s reports %q but supplies %.2f W while the SoC draws %.2f W; the device is on external power, not counting battery as system power", b.name, status, watts, pkgWatts)
		b.warned = true
	}
	// battery_discharging means "the battery is powering the device", which
	// is what a viewer cares about, not the gauge's raw status string.
	dis := 0.0
	if supplying {
		dis = 1
	}
	out = append(out, module.Metric{Name: "battery_discharging", Value: dis})

	lbl := map[string]string{"domain": DomainBattery}
	if supplying {
		out = append(out, module.Metric{Name: "power", Value: watts, Unit: "W", Labels: lbl})
	}
	if counting {
		// Trapezoid between consecutive discharging samples.
		if b.prevDischarging && !b.prevTime.IsZero() {
			dt := now.Sub(b.prevTime).Seconds()
			if dt > 0 {
				b.joules += (b.prevWatts + watts) / 2 * dt
				b.seconds += dt
			}
		}
	}
	b.prevWatts, b.prevTime, b.prevDischarging = watts, now, counting

	// Always emitted so a client can tell "no discharge time in this
	// window" from "no battery source at all".
	out = append(out,
		module.Metric{Name: "energy", Value: b.joules, Unit: "J", Labels: lbl},
		module.Metric{Name: "measured_seconds", Value: b.seconds, Unit: "s", Labels: lbl},
	)
	return out
}

func (m *Module) Close() error {
	for _, z := range m.zones {
		z.energy.Close() //nolint:errcheck
	}
	if m.bat != nil {
		m.bat.close()
	}
	return nil
}

func (b *battery) close() {
	for _, f := range []*sysfile.File{b.status, b.capacity, b.voltage, b.power, b.current} {
		f.Close() //nolint:errcheck
	}
}

// --- helpers ---

func readString(path string) string {
	s, err := sysfile.ReadString(path)
	if err != nil {
		return ""
	}
	return s
}

// openInt holds path open if it exists and currently parses as an
// integer; nil otherwise.
func openInt(path string) *sysfile.File {
	f, err := sysfile.Open(path)
	if err != nil {
		return nil
	}
	if _, err := f.Int(); err != nil {
		f.Close() //nolint:errcheck
		return nil
	}
	return f
}

// heldInt reads a held attribute; false when it is absent or unreadable.
func heldInt(f *sysfile.File) (int64, bool) {
	if f == nil {
		return 0, false
	}
	v, err := f.Int()
	return v, err == nil
}
