package cpu

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Core type labels — match the kernel's hybrid scheduler terminology.
const (
	CoreTypePerformance = "performance"
	CoreTypeEfficient   = "efficient"
	CoreTypeLowPower    = "low_power"
)

// coreInfo describes one logical CPU.
type coreInfo struct {
	id       int    // logical CPU number (e.g. 0..15)
	coreType string // performance / efficient / low_power
	maxKHz   uint64 // cpuinfo_max_freq, used for binning E vs LP-E
}

// discoverTopology classifies every logical CPU into performance/efficient/
// low_power based on:
//   - membership in /sys/devices/cpu_core/cpus → performance
//   - otherwise: bin /sys/devices/cpu_atom/cpus by cpuinfo_max_freq.
//     The highest-max-freq bin is "efficient", any lower bin is "low_power".
//
// On non-hybrid CPUs (no cpu_core/cpu_atom dirs) every visible CPU is
// classified as "performance" so the rest of the pipeline still works.
//
// "Visible" means: present in /proc/stat. Inside an LXC with a cgroup
// cpuset mask, /proc/stat only lists the cores assigned to us, even though
// the global sysfs paths exist for all 16. We use /proc/stat as the source
// of truth for which cores to actually monitor.
func discoverTopology() ([]coreInfo, error) {
	visible, err := visibleCPUs()
	if err != nil {
		return nil, err
	}
	if len(visible) == 0 {
		return nil, fmt.Errorf("no per-cpu lines in /proc/stat")
	}

	pCores := readCPUList("/sys/devices/cpu_core/cpus")
	aCores := readCPUList("/sys/devices/cpu_atom/cpus")

	pSet := intSet(pCores)
	aSet := intSet(aCores)

	// Read max-freq for every visible CPU.
	maxFreqs := map[int]uint64{}
	for _, id := range visible {
		path := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/cpuinfo_max_freq", id)
		if v, err := readUint(path); err == nil {
			maxFreqs[id] = v
		}
	}

	// Determine the highest max-freq among atom cores. Any atom core with
	// strictly less than that goes into low_power. If we don't have a
	// reliable max-freq, fall back to "efficient" for all atoms.
	var atomMax uint64
	for _, id := range visible {
		if !aSet[id] {
			continue
		}
		if maxFreqs[id] > atomMax {
			atomMax = maxFreqs[id]
		}
	}

	out := make([]coreInfo, 0, len(visible))
	for _, id := range visible {
		ci := coreInfo{id: id, maxKHz: maxFreqs[id]}
		switch {
		case pSet[id]:
			ci.coreType = CoreTypePerformance
		case aSet[id]:
			if atomMax > 0 && ci.maxKHz > 0 && ci.maxKHz < atomMax {
				ci.coreType = CoreTypeLowPower
			} else {
				ci.coreType = CoreTypeEfficient
			}
		default:
			// Non-hybrid (no PMU class dirs) or visible CPU not in either
			// PMU set: treat as performance.
			ci.coreType = CoreTypePerformance
		}
		out = append(out, ci)
	}
	return out, nil
}

// visibleCPUs returns the sorted list of logical CPU IDs that have a
// "cpuN" line in /proc/stat. This respects cgroup cpuset constraints.
func visibleCPUs() ([]int, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck

	var ids []int
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu") || strings.HasPrefix(line, "cpu ") {
			continue
		}
		// Format: "cpuN ...". Parse N.
		end := 3
		for end < len(line) && line[end] >= '0' && line[end] <= '9' {
			end++
		}
		if end == 3 {
			continue
		}
		n, err := strconv.Atoi(line[3:end])
		if err != nil {
			continue
		}
		ids = append(ids, n)
	}
	sort.Ints(ids)
	return ids, scanner.Err()
}

// readCPUList parses a "0-3,8,12-15"-style cpu list file. Returns nil
// (not an error) if the file is missing — that's the non-hybrid case.
func readCPUList(path string) []int {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseCPUList(strings.TrimSpace(string(b)))
}

func parseCPUList(s string) []int {
	if s == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, "-"); i >= 0 {
			lo, err1 := strconv.Atoi(part[:i])
			hi, err2 := strconv.Atoi(part[i+1:])
			if err1 != nil || err2 != nil {
				continue
			}
			for n := lo; n <= hi; n++ {
				out = append(out, n)
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				continue
			}
			out = append(out, n)
		}
	}
	return out
}

func intSet(xs []int) map[int]bool {
	out := make(map[int]bool, len(xs))
	for _, x := range xs {
		out[x] = true
	}
	return out
}

// --- Per-core /proc/stat reader ---

type perCoreStat struct {
	total uint64
	idle  uint64
}

// readPerCoreStats reads every "cpuN" line from /proc/stat into a map
// keyed by logical CPU id.
func readPerCoreStats() (map[int]perCoreStat, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck

	out := map[int]perCoreStat{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu") || strings.HasPrefix(line, "cpu ") {
			continue
		}
		// Find end of "cpuN"
		end := 3
		for end < len(line) && line[end] >= '0' && line[end] <= '9' {
			end++
		}
		if end == 3 {
			continue
		}
		id, err := strconv.Atoi(line[3:end])
		if err != nil {
			continue
		}
		fields := strings.Fields(line[end:])
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
		out[id] = perCoreStat{total: total, idle: idle}
	}
	return out, scanner.Err()
}

// readPerCoreFreqMHz reads scaling_cur_freq for one logical CPU.
func readPerCoreFreqMHz(id int) (float64, bool) {
	path := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_cur_freq", id)
	v, err := readUint(path)
	if err != nil {
		return 0, false
	}
	return float64(v) / 1000.0, true
}
