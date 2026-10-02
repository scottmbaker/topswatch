package procwalk

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ProcessCPU is one process's CPU consumption since the previous sample.
type ProcessCPU struct {
	PID        int     `json:"pid"`
	Comm       string  `json:"comm"`
	CPUPercent float64 `json:"cpu_percent"` // Irix-style: 0..N*100 where N is core count
	RSSBytes   uint64  `json:"rss_bytes"`
}

// cpuPidState is the prior-sample state per PID for CPU jiffies tracking.
type cpuPidState struct {
	prevJiffies uint64
	prevWall    time.Time
}

// clkTck is the kernel's CONFIG_HZ as reported by sysconf(_SC_CLK_TCK).
// Discovered once at startup via `getconf CLK_TCK` (avoids CGO). Falls
// back to 100 if discovery fails — that's the most common value but Debian/
// Ubuntu sometimes use 250 and a few desktop kernels use 1000, so we try
// to read the real value first.
var clkTck = discoverClkTck()

func discoverClkTck() uint64 {
	out, err := exec.Command("getconf", "CLK_TCK").Output()
	if err != nil {
		return 100
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || v == 0 {
		return 100
	}
	return v
}

var pageSize = uint64(os.Getpagesize())

// readProcStat parses /proc/<pid>/stat and returns (utime+stime in jiffies,
// rss in pages, ok). The stat file format is annoying because field 2
// (comm) is in parens and can contain spaces and parens itself, so we
// find the *last* ')' and parse fields after that as space-separated.
//
// Field layout (1-indexed) post-comm:
//
//	 3: state               14: utime          24: rss (pages)
//	 4: ppid                15: stime
//	... (we don't care about the others)
//
// After "comm) " the index of utime is field 14 - 2 = 12 (0-indexed: 11),
// stime is 12, and rss is 21.
func readProcStat(pid int) (jiffies uint64, rssPages uint64, ok bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, false
	}
	lastParen := strings.LastIndexByte(string(b), ')')
	if lastParen < 0 || lastParen+2 >= len(b) {
		return 0, 0, false
	}
	rest := strings.Fields(string(b[lastParen+2:]))
	// rest[0] is the state char ('R'/'S'/etc.)
	// rest[11] is utime, rest[12] is stime, rest[21] is rss in pages
	if len(rest) < 22 {
		return 0, 0, false
	}
	ut, err1 := strconv.ParseUint(rest[11], 10, 64)
	st, err2 := strconv.ParseUint(rest[12], 10, 64)
	rss, err3 := strconv.ParseUint(rest[21], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, false
	}
	return ut + st, rss, true
}

// sampleCPUProcesses walks every PID in /proc, computes CPU usage delta
// against the prior call, and returns the top maxResults processes by
// CPU percent.
//
// CPU percent uses the Irix convention: a process consuming a fully-busy
// core for the whole interval reads 100%. So a 4-thread workload pegging
// every thread reads 400%. This matches what `top` shows by default and
// matches our `topswatch_cpu_cores_used` metric scaled to percent.
func (t *Tracker) sampleCPUProcesses(now time.Time, maxResults int, pids []int) []ProcessCPU {
	var out []ProcessCPU
	newState := make(map[int]*cpuPidState, len(t.cpuState))

	for _, pid := range pids {
		jiffies, rssPages, ok := readProcStat(pid)
		if !ok {
			continue
		}

		newState[pid] = &cpuPidState{prevJiffies: jiffies, prevWall: now}

		prev := t.cpuState[pid]
		if prev == nil {
			// No baseline yet. Don't emit.
			continue
		}
		dWall := now.Sub(prev.prevWall).Seconds()
		if dWall <= 0 {
			continue
		}
		var dJiffies uint64
		if jiffies >= prev.prevJiffies {
			dJiffies = jiffies - prev.prevJiffies
		}
		// CPU seconds consumed in the interval = dJiffies / clkTck
		// Cores busy = (CPU seconds) / dWall
		// Percent = cores * 100
		cpuPct := (float64(dJiffies) / float64(clkTck) / dWall) * 100
		if cpuPct < 0.05 {
			// Below noise threshold — skip to keep the list short.
			continue
		}

		out = append(out, ProcessCPU{
			PID:        pid,
			Comm:       readProcessName(pid),
			CPUPercent: cpuPct,
			RSSBytes:   rssPages * pageSize,
		})
	}

	t.cpuState = newState

	// Sort by CPU desc, take top N.
	sort.Slice(out, func(i, j int) bool {
		return out[i].CPUPercent > out[j].CPUPercent
	})
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out
}
