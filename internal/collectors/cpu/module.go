package cpu

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/scottmbaker/topswatch/internal/module"
	"github.com/scottmbaker/topswatch/internal/sysfile"
)

// Every attribute sampled each tick is held open and re-read in place
// (see sysfile); the discovery functions decide at Init what exists.
type Module struct {
	info    module.DeviceInfo
	threads int

	stat         *sysfile.File // /proc/stat: aggregate and per-core lines in one read
	meminfo      *sysfile.File
	freqs        []freqFile    // scaling_cur_freq of every CPU sysfs lists
	rapl         *sysfile.File // package energy_uj; nil if unavailable
	raplMaxRange uint64
	hwmonTemp    *sysfile.File // nil if unavailable

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

// freqFile is one CPU's scaling_cur_freq held open.
type freqFile struct {
	id int
	f  *sysfile.File
}

type cpuStat struct {
	total uint64
	idle  uint64
}

// perCoreStat is one logical CPU's jiffie totals.
type perCoreStat struct {
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

	stat, err := sysfile.Open("/proc/stat")
	if err != nil {
		return err
	}
	m.stat = stat
	m.meminfo, _ = sysfile.Open("/proc/meminfo")
	m.freqs = openFreqFiles()

	// RAPL package energy (best-effort — often root-only)
	if f, maxRange, err := discoverRAPL(); err == nil {
		m.rapl = f
		m.raplMaxRange = maxRange
		log.Printf("[cpu] RAPL package energy at %s", f.Path())
	} else {
		log.Printf("[cpu] RAPL unavailable: %v (power metric disabled)", err)
	}

	// hwmon CPU temperature (best-effort)
	if f, err := discoverCPUHwmon(); err == nil {
		m.hwmonTemp = f
		log.Printf("[cpu] package temperature at %s", f.Path())
	} else {
		log.Printf("[cpu] CPU temperature unavailable: %v", err)
	}

	// Prime utilization baselines, aggregate and per-core, from one read.
	agg, perCore, err := m.readStat()
	if err == nil {
		m.prevStat = agg
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
		m.prevPerCore = perCore
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

	// One read of /proc/stat serves the aggregate and the per-core
	// utilization; one read of each scaling_cur_freq serves the average
	// and the per-core frequency.
	st, perCore, statErr := m.readStat()
	freqs := m.readFreqs()

	// Utilization (delta of /proc/stat aggregate cpu line). Reported two
	// ways: as a 0-100% aggregate, and as "cores busy" (0..N) which is
	// the Irix-style sum that matches top's process column.
	if statErr == nil {
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
	if len(freqs) > 0 {
		var sum float64
		for _, mhz := range freqs {
			sum += mhz
		}
		metrics = append(metrics, module.Metric{
			Name: "frequency", Value: sum / float64(len(freqs)), Unit: "MHz",
		})
	}

	// Power (RAPL energy_uj delta)
	if m.rapl != nil {
		if uj, err := m.rapl.Uint(); err == nil {
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
	if total, avail, ok := m.readMemInfo(); ok {
		used := total - avail
		metrics = append(metrics,
			module.Metric{Name: "memory_used", Value: float64(used), Unit: "bytes"},
			module.Metric{Name: "memory_total", Value: float64(total), Unit: "bytes"},
			module.Metric{Name: "memory_used_percent", Value: float64(used) / float64(total) * 100, Unit: "%"},
		)
	}

	// Temperature (hwmon coretemp/k10temp, millidegrees -> degrees)
	if m.hwmonTemp != nil {
		if mC, err := m.hwmonTemp.Uint(); err == nil {
			metrics = append(metrics, module.Metric{
				Name: "temperature", Value: float64(mC) / 1000.0, Unit: "C",
			})
		}
	}

	// --- Per-core metrics ---
	if len(m.cores) > 0 && statErr == nil {
		metrics = append(metrics, m.collectPerCore(perCore, freqs)...)
	}

	return metrics, nil
}

func (m *Module) collectPerCore(pcs map[int]perCoreStat, freqs map[int]float64) []module.Metric {
	var out []module.Metric

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
		if mhz, ok := freqs[c.id]; ok {
			out = append(out, module.Metric{
				Name: "core_frequency", Value: mhz, Unit: "MHz",
				Labels: labels,
			})
		}
	}

	m.prevPerCore = pcs
	return out
}

func (m *Module) Close() error {
	m.stat.Close()      //nolint:errcheck
	m.meminfo.Close()   //nolint:errcheck
	m.rapl.Close()      //nolint:errcheck
	m.hwmonTemp.Close() //nolint:errcheck
	for _, ff := range m.freqs {
		ff.f.Close() //nolint:errcheck
	}
	return nil
}

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

// readStat re-reads /proc/stat and parses the aggregate "cpu" line and
// every "cpuN" line in one pass.
func (m *Module) readStat() (cpuStat, map[int]perCoreStat, error) {
	b, err := m.stat.Bytes()
	if err != nil {
		return cpuStat{}, nil, err
	}
	return parseStat(b)
}

func parseStat(b []byte) (cpuStat, map[int]perCoreStat, error) {
	var agg cpuStat
	aggSet := false
	perCore := map[int]perCoreStat{}
	for len(b) > 0 {
		line := b
		if i := strings.IndexByte(string(b), '\n'); i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			b = nil
		}
		if len(line) < 4 || line[0] != 'c' || line[1] != 'p' || line[2] != 'u' {
			continue
		}
		if line[3] == ' ' {
			agg = sumStatFields(string(line[4:]))
			aggSet = true
			continue
		}
		end := 3
		for end < len(line) && line[end] >= '0' && line[end] <= '9' {
			end++
		}
		if end == 3 {
			continue
		}
		id, err := strconv.Atoi(string(line[3:end]))
		if err != nil {
			continue
		}
		st := sumStatFields(string(line[end:]))
		perCore[id] = perCoreStat{total: st.total, idle: st.idle}
	}
	if !aggSet {
		return cpuStat{}, nil, fmt.Errorf("no aggregate cpu line in /proc/stat")
	}
	return agg, perCore, nil
}

// sumStatFields sums the jiffie columns of one /proc/stat cpu line:
// user nice system idle iowait irq softirq steal guest guest_nice.
func sumStatFields(fields string) cpuStat {
	var st cpuStat
	for i, fs := range strings.Fields(fields) {
		v, err := strconv.ParseUint(fs, 10, 64)
		if err != nil {
			continue
		}
		st.total += v
		if i == 3 || i == 4 { // idle + iowait
			st.idle += v
		}
	}
	return st
}

// --- cpufreq (frequency) ---

// openFreqFiles holds open the scaling_cur_freq of every CPU sysfs
// lists, sorted by CPU id. The glob runs once, here, not every sample.
func openFreqFiles() []freqFile {
	matches, err := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_cur_freq")
	if err != nil {
		return nil
	}
	var out []freqFile
	for _, p := range matches {
		idStr := strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(p))), "cpu")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		f, err := sysfile.OpenUint(p)
		if err != nil {
			continue
		}
		out = append(out, freqFile{id: id, f: f})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// readFreqs returns the current frequency of every held CPU in MHz.
func (m *Module) readFreqs() map[int]float64 {
	out := make(map[int]float64, len(m.freqs))
	for _, ff := range m.freqs {
		khz, err := ff.f.Uint()
		if err != nil {
			continue
		}
		out[ff.id] = float64(khz) / 1000.0
	}
	return out
}

// --- memory ---

// readMemInfo returns MemTotal and MemAvailable in bytes.
func (m *Module) readMemInfo() (total, avail uint64, ok bool) {
	if m.meminfo == nil {
		return 0, 0, false
	}
	b, err := m.meminfo.Bytes()
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

func discoverRAPL() (*sysfile.File, uint64, error) {
	// Prefer the package-level zone.
	zones, err := filepath.Glob("/sys/class/powercap/intel-rapl:*")
	if err != nil || len(zones) == 0 {
		return nil, 0, fmt.Errorf("no intel-rapl zones found")
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
		f, err := sysfile.OpenUint(energyPath)
		if err != nil {
			return nil, 0, fmt.Errorf("cannot read %s: %w", energyPath, err)
		}
		maxRange, _ := sysfile.ReadUint(filepath.Join(z, "max_energy_range_uj"))
		return f, maxRange, nil
	}
	return nil, 0, fmt.Errorf("no package-level RAPL zone found")
}

// --- hwmon (temperature) ---

func discoverCPUHwmon() (*sysfile.File, error) {
	hwmons, err := filepath.Glob("/sys/class/hwmon/hwmon*")
	if err != nil || len(hwmons) == 0 {
		return nil, fmt.Errorf("no hwmon entries")
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
			if f, err := sysfile.OpenUint(filepath.Join(h, "temp1_input")); err == nil {
				return f, nil
			}
		}
	}
	return nil, fmt.Errorf("no CPU temperature hwmon found")
}
