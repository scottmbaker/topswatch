package cpu

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/scottmbaker/topswatch/internal/module"
)

type Module struct {
	info    module.DeviceInfo
	threads int

	// Discovered paths (empty if unavailable)
	raplEnergyPath string
	raplMaxRange   uint64
	hwmonTempPath  string

	// Delta state
	prevStat       cpuStat
	prevStatSet    bool
	prevEnergyUj   uint64
	prevEnergyTime time.Time
	prevEnergySet  bool

	// Per-core state
	cores       []coreInfo          // empty if topology discovery failed
	prevPerCore map[int]perCoreStat // baselines for delta
}

type cpuStat struct {
	total uint64
	idle  uint64
}

func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "cpu" }

func (m *Module) Init() error {
	cpuinfo, err := parseCPUInfo("/proc/cpuinfo")
	if err != nil {
		return err
	}

	m.threads, _ = strconv.Atoi(cpuinfo["threads"])
	if m.threads < 1 {
		m.threads = 1
	}

	m.info = module.DeviceInfo{
		Name: cpuinfo["model name"],
		Extra: map[string]string{
			"vendor":    cpuinfo["vendor_id"],
			"cores":     cpuinfo["cores"],
			"threads":   cpuinfo["threads"],
			"family":    cpuinfo["cpu family"],
			"model":     cpuinfo["model"],
			"stepping":  cpuinfo["stepping"],
			"microcode": cpuinfo["microcode"],
			"arch":      runtime.GOARCH,
		},
	}

	// RAPL package energy (best-effort — often root-only)
	if path, maxRange, err := discoverRAPL(); err == nil {
		m.raplEnergyPath = path
		m.raplMaxRange = maxRange
		log.Printf("[cpu] RAPL package energy at %s", path)
	} else {
		log.Printf("[cpu] RAPL unavailable: %v (power metric disabled)", err)
	}

	// hwmon CPU temperature (best-effort)
	if path, err := discoverCPUHwmon(); err == nil {
		m.hwmonTempPath = path
		log.Printf("[cpu] package temperature at %s", path)
	} else {
		log.Printf("[cpu] CPU temperature unavailable: %v", err)
	}

	// Prime utilization baseline
	if st, err := readCPUStat(); err == nil {
		m.prevStat = st
		m.prevStatSet = true
	}

	// Discover per-core topology and prime per-core baselines.
	if cores, err := discoverTopology(); err == nil && len(cores) > 0 {
		m.cores = cores
		var p, e, lp int
		for _, c := range cores {
			switch c.coreType {
			case CoreTypePerformance:
				p++
			case CoreTypeEfficient:
				e++
			case CoreTypeLowPower:
				lp++
			}
		}
		log.Printf("[cpu] per-core topology: %d cores visible (P=%d E=%d LP=%d)",
			len(cores), p, e, lp)
		if pcs, err := readPerCoreStats(); err == nil {
			m.prevPerCore = pcs
		}
		// Stash core type counts in DeviceInfo so the UI can render badges.
		m.info.Extra["p_cores"] = strconv.Itoa(p)
		m.info.Extra["e_cores"] = strconv.Itoa(e)
		m.info.Extra["lp_cores"] = strconv.Itoa(lp)
	} else if err != nil {
		log.Printf("[cpu] per-core topology unavailable: %v", err)
	}

	return nil
}

func (m *Module) DeviceInfo() module.DeviceInfo { return m.info }

func (m *Module) Collect() ([]module.Metric, error) {
	var metrics []module.Metric

	// Utilization (delta of /proc/stat aggregate cpu line). Reported two
	// ways: as a 0-100% aggregate, and as "cores busy" (0..N) which is
	// the Irix-style sum that matches top's process column.
	if st, err := readCPUStat(); err == nil {
		if m.prevStatSet && st.total > m.prevStat.total {
			dTotal := st.total - m.prevStat.total
			dIdle := st.idle - m.prevStat.idle
			frac := float64(dTotal-dIdle) / float64(dTotal)
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			metrics = append(metrics,
				module.Metric{Name: "utilization", Value: frac * 100.0, Unit: "%"},
				module.Metric{Name: "cores_used", Value: frac * float64(m.threads), Unit: ""},
			)
		}
		m.prevStat = st
		m.prevStatSet = true
	}

	// Frequency (average across logical CPUs, MHz)
	if mhz, ok := readAvgFreqMHz(); ok {
		metrics = append(metrics, module.Metric{
			Name: "frequency", Value: mhz, Unit: "MHz",
		})
	}

	// Power (RAPL energy_uj delta)
	if m.raplEnergyPath != "" {
		if uj, err := readUint(m.raplEnergyPath); err == nil {
			now := time.Now()
			if m.prevEnergySet {
				var dUj uint64
				if uj >= m.prevEnergyUj {
					dUj = uj - m.prevEnergyUj
				} else if m.raplMaxRange > 0 {
					dUj = (m.raplMaxRange - m.prevEnergyUj) + uj
				}
				dT := now.Sub(m.prevEnergyTime).Seconds()
				if dT > 0 {
					watts := float64(dUj) / 1e6 / dT
					metrics = append(metrics, module.Metric{
						Name: "power", Value: watts, Unit: "W",
					})
				}
			}
			m.prevEnergyUj = uj
			m.prevEnergyTime = now
			m.prevEnergySet = true
		}
	}

	// System memory. "Used" is total minus the kernel's MemAvailable
	// estimate, which is what free(1) reports; on an integrated GPU the
	// GPU's buffers live in this same pool and are part of "used".
	if total, avail, ok := readMemInfo(); ok {
		used := total - avail
		metrics = append(metrics,
			module.Metric{Name: "memory_used", Value: float64(used), Unit: "bytes"},
			module.Metric{Name: "memory_total", Value: float64(total), Unit: "bytes"},
			module.Metric{Name: "memory_used_percent", Value: float64(used) / float64(total) * 100, Unit: "%"},
		)
	}

	// Temperature (hwmon coretemp/k10temp, millidegrees -> degrees)
	if m.hwmonTempPath != "" {
		if mC, err := readUint(m.hwmonTempPath); err == nil {
			metrics = append(metrics, module.Metric{
				Name: "temperature", Value: float64(mC) / 1000.0, Unit: "C",
			})
		}
	}

	// --- Per-core metrics ---
	if len(m.cores) > 0 {
		metrics = append(metrics, m.collectPerCore()...)
	}

	return metrics, nil
}

func (m *Module) collectPerCore() []module.Metric {
	var out []module.Metric
	pcs, err := readPerCoreStats()
	if err != nil {
		return nil
	}

	for _, c := range m.cores {
		cur, ok := pcs[c.id]
		if !ok {
			continue
		}
		idStr := strconv.Itoa(c.id)
		labels := map[string]string{
			"core":      idStr,
			"core_type": c.coreType,
		}

		// Utilization (delta)
		if prev, ok := m.prevPerCore[c.id]; ok && cur.total > prev.total {
			dTotal := cur.total - prev.total
			dIdle := cur.idle - prev.idle
			frac := float64(dTotal-dIdle) / float64(dTotal)
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			out = append(out, module.Metric{
				Name: "core_utilization", Value: frac * 100, Unit: "%",
				Labels: labels,
			})
		}

		// Frequency (instantaneous)
		if mhz, ok := readPerCoreFreqMHz(c.id); ok {
			out = append(out, module.Metric{
				Name: "core_frequency", Value: mhz, Unit: "MHz",
				Labels: labels,
			})
		}
	}

	m.prevPerCore = pcs
	return out
}

func (m *Module) Close() error { return nil }

// --- /proc/cpuinfo (static identity) ---

func parseCPUInfo(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck

	result := map[string]string{}
	coreIDs := map[string]bool{}
	processors := 0

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		switch key {
		case "processor":
			processors++
		case "model name", "vendor_id", "cpu family", "model", "stepping", "microcode":
			if _, exists := result[key]; !exists {
				result[key] = val
			}
		case "core id":
			coreIDs[val] = true
		}
	}

	result["threads"] = strconv.Itoa(processors)
	result["cores"] = strconv.Itoa(len(coreIDs))

	return result, scanner.Err()
}

// --- /proc/stat (utilization) ---

func readCPUStat() (cpuStat, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuStat{}, err
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return cpuStat{}, fmt.Errorf("empty /proc/stat")
	}
	line := scanner.Text()
	if !strings.HasPrefix(line, "cpu ") {
		return cpuStat{}, fmt.Errorf("unexpected /proc/stat format")
	}
	// Fields: user nice system idle iowait irq softirq steal guest guest_nice
	fields := strings.Fields(line)[1:]
	var total, idle uint64
	for i, fs := range fields {
		v, err := strconv.ParseUint(fs, 10, 64)
		if err != nil {
			continue
		}
		total += v
		if i == 3 || i == 4 { // idle + iowait
			idle += v
		}
	}
	return cpuStat{total: total, idle: idle}, nil
}

// --- cpufreq (frequency) ---

func readAvgFreqMHz() (float64, bool) {
	matches, err := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_cur_freq")
	if err != nil || len(matches) == 0 {
		return 0, false
	}
	var sumKhz uint64
	var n int
	for _, p := range matches {
		v, err := readUint(p)
		if err != nil {
			continue
		}
		sumKhz += v
		n++
	}
	if n == 0 {
		return 0, false
	}
	return float64(sumKhz) / float64(n) / 1000.0, true
}

// --- memory ---

// readMemInfo returns MemTotal and MemAvailable in bytes.
func readMemInfo() (total, avail uint64, ok bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return total, avail, total > 0 && avail <= total
}

// --- RAPL (power) ---

func discoverRAPL() (string, uint64, error) {
	// Prefer the package-level zone.
	zones, err := filepath.Glob("/sys/class/powercap/intel-rapl:*")
	if err != nil || len(zones) == 0 {
		return "", 0, fmt.Errorf("no intel-rapl zones found")
	}
	// Filter to top-level package zones (intel-rapl:N, no further colons).
	for _, z := range zones {
		base := filepath.Base(z)
		if strings.Count(base, ":") != 1 {
			continue
		}
		nameBytes, err := os.ReadFile(filepath.Join(z, "name"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(nameBytes))
		if !strings.HasPrefix(name, "package-") {
			continue
		}
		energyPath := filepath.Join(z, "energy_uj")
		if _, err := readUint(energyPath); err != nil {
			return "", 0, fmt.Errorf("cannot read %s: %w", energyPath, err)
		}
		maxRange, _ := readUint(filepath.Join(z, "max_energy_range_uj"))
		return energyPath, maxRange, nil
	}
	return "", 0, fmt.Errorf("no package-level RAPL zone found")
}

// --- hwmon (temperature) ---

func discoverCPUHwmon() (string, error) {
	hwmons, err := filepath.Glob("/sys/class/hwmon/hwmon*")
	if err != nil || len(hwmons) == 0 {
		return "", fmt.Errorf("no hwmon entries")
	}
	for _, h := range hwmons {
		nameBytes, err := os.ReadFile(filepath.Join(h, "name"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(nameBytes))
		switch name {
		case "coretemp", "k10temp", "zenpower":
			// temp1_input is the package on coretemp; on k10temp it is Tctl/Tdie.
			p := filepath.Join(h, "temp1_input")
			if _, err := readUint(p); err == nil {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("no CPU temperature hwmon found")
}

// --- generic ---

func readUint(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}
