// Package procwalk identifies processes that hold open file descriptors
// against accelerator devices, and (where the kernel exposes it) reports
// their per-engine busy fractions via fdinfo.
//
// Two device families are handled:
//
//   - /dev/dri/* — GPU. The kernel publishes per-fd "drm-cycles-<engine>"
//     and "drm-total-cycles-<engine>" counters in fdinfo for both i915 and
//     xe drivers. Busy fraction is dCycles/dTotal between samples, which
//     is naturally bounded to 0..1 without wall-time bookkeeping.
//
//   - /dev/accel/* — NPU. The intel_vpu driver does not currently expose
//     any per-fd counters in fdinfo (verified empirically on kernel 6.17).
//     We still surface the PID/comm so users can see which processes
//     hold the NPU open, just without per-process busy attribution.
//
// Discovery is via /proc/<pid>/fd symlinks, so the package needs to run
// with sufficient privileges to read other processes' fds (typically
// CAP_SYS_PTRACE or root).
package procwalk

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Snapshot is one polling result. It is fully self-contained — the caller
// can stash, JSON-serialize, or diff it without holding any tracker locks.
type Snapshot struct {
	Timestamp time.Time    `json:"timestamp"`
	GPU       []ProcessGPU `json:"gpu,omitempty"`
	NPU       []ProcessNPU `json:"npu,omitempty"`
	CPU       []ProcessCPU `json:"cpu,omitempty"`
}

// ProcessGPU is one process attributing to a single DRM client (one fd).
// A single PID may have multiple ProcessGPU entries if it opened the
// device several times — in practice the dashboard sums them by PID.
type ProcessGPU struct {
	PID         int                `json:"pid"`
	Comm        string             `json:"comm"`
	Driver      string             `json:"driver,omitempty"`     // "xe" or "i915"
	ClientID    string             `json:"client_id,omitempty"`  // drm-client-id
	GTTBytes    uint64             `json:"gtt_bytes,omitempty"`  // total GTT memory
	EngineBusy  map[string]float64 `json:"engine_busy,omitempty"` // engine -> 0..1, since previous sample
	TotalBusy   float64            `json:"total_busy"`            // sum across engines, clamped to 0..1
}

// ProcessNPU is one process holding an /dev/accel/* fd. No per-process
// busy data is available on current intel_vpu kernels.
type ProcessNPU struct {
	PID  int    `json:"pid"`
	Comm string `json:"comm"`
}

// fdState holds previous-sample counters for one (pid, fd) pair so we
// can compute deltas.
type fdState struct {
	prevCycles      map[string]uint64
	prevTotalCycles map[string]uint64
}

// Tracker keeps prior-sample state across calls so it can compute deltas.
// It is safe for concurrent use; in practice the collector loop calls
// Sample() from a single goroutine.
type Tracker struct {
	mu sync.Mutex
	// keyed by "<pid>:<fd>" — we lose state if a process exits, which
	// is fine.
	gpuState map[string]*fdState
	// keyed by pid for CPU jiffie deltas.
	cpuState map[int]*cpuPidState
}

func NewTracker() *Tracker {
	return &Tracker{
		gpuState: map[string]*fdState{},
		cpuState: map[int]*cpuPidState{},
	}
}

// Sample walks /proc once and returns the current snapshot.
func (t *Tracker) Sample() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	snap := Snapshot{Timestamp: now}

	// Walk /proc/<pid>/fd for accelerator targets.
	pidEntries, err := os.ReadDir("/proc")
	if err != nil {
		return snap
	}

	// Collect GPU entries by (pid, fd) so we can carry deltas forward.
	type rawGPU struct {
		pid       int
		comm      string
		fdNum     string
		fdInfoRaw map[string]string
	}
	var rawGPUs []rawGPU

	// Used to dedupe NPU entries by pid (a process might hold multiple
	// accel fds, but the user just wants to see the PID once).
	npuPids := map[int]string{}

	for _, pe := range pidEntries {
		if !pe.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(pe.Name())
		if err != nil {
			continue
		}
		fdDir := "/proc/" + pe.Name() + "/fd"
		fdEntries, err := os.ReadDir(fdDir)
		if err != nil {
			continue // permission denied or process exited
		}
		comm := readProcessName(pid)
		for _, fe := range fdEntries {
			fdNum := fe.Name()
			tgt, err := os.Readlink(filepath.Join(fdDir, fdNum))
			if err != nil {
				continue
			}
			switch {
			case strings.HasPrefix(tgt, "/dev/dri/"):
				info := readFdInfo(pid, fdNum)
				if len(info) == 0 {
					continue
				}
				// Only include fds that look like a DRM client (have a driver field).
				if _, ok := info["drm-driver"]; !ok {
					continue
				}
				rawGPUs = append(rawGPUs, rawGPU{
					pid: pid, comm: comm, fdNum: fdNum, fdInfoRaw: info,
				})
			case strings.HasPrefix(tgt, "/dev/accel/"):
				if _, dup := npuPids[pid]; !dup {
					npuPids[pid] = comm
				}
			}
		}
	}

	// Compute GPU deltas using prior state.
	newState := make(map[string]*fdState, len(rawGPUs))
	for _, r := range rawGPUs {
		key := fmt.Sprintf("%d:%s", r.pid, r.fdNum)
		curCycles, curTotal := parseDrmCycles(r.fdInfoRaw)
		gtt := parseUintField(r.fdInfoRaw, "drm-total-gtt")

		entry := ProcessGPU{
			PID:        r.pid,
			Comm:       r.comm,
			Driver:     r.fdInfoRaw["drm-driver"],
			ClientID:   r.fdInfoRaw["drm-client-id"],
			GTTBytes:   gtt,
			EngineBusy: map[string]float64{},
		}

		var sumBusy float64
		if prev := t.gpuState[key]; prev != nil {
			for eng, total := range curTotal {
				prevC := prev.prevCycles[eng]
				prevT := prev.prevTotalCycles[eng]
				curC := curCycles[eng]
				if total <= prevT {
					continue // counter reset or no progress
				}
				dC := uint64(0)
				if curC >= prevC {
					dC = curC - prevC
				}
				dT := total - prevT
				if dT == 0 {
					continue
				}
				frac := float64(dC) / float64(dT)
				if frac < 0 {
					frac = 0
				}
				if frac > 1 {
					frac = 1
				}
				if frac > 0 {
					entry.EngineBusy[eng] = frac
				}
				sumBusy += frac
			}
		}
		if sumBusy > 1 {
			sumBusy = 1
		}
		entry.TotalBusy = sumBusy

		// Save state for next sample.
		newState[key] = &fdState{
			prevCycles:      curCycles,
			prevTotalCycles: curTotal,
		}

		snap.GPU = append(snap.GPU, entry)
	}
	t.gpuState = newState

	// Sort GPU entries: busy desc, then PID for stability.
	sort.Slice(snap.GPU, func(i, j int) bool {
		if snap.GPU[i].TotalBusy != snap.GPU[j].TotalBusy {
			return snap.GPU[i].TotalBusy > snap.GPU[j].TotalBusy
		}
		return snap.GPU[i].PID < snap.GPU[j].PID
	})

	// Materialize NPU list, sorted by PID.
	for pid, comm := range npuPids {
		snap.NPU = append(snap.NPU, ProcessNPU{PID: pid, Comm: comm})
	}
	sort.Slice(snap.NPU, func(i, j int) bool {
		return snap.NPU[i].PID < snap.NPU[j].PID
	})

	// CPU process attribution. Top 8 by CPU percent.
	snap.CPU = t.sampleCPUProcesses(now, 8)

	return snap
}

// --- helpers ---

// readProcessName returns a display-friendly name for a process. It prefers
// /proc/<pid>/cmdline (which has the actual binary path and script args)
// over /proc/<pid>/comm (which is the kernel's 16-char-truncated thread name).
//
// For a Python service the difference is "python3" (comm) vs
// "python3 imagegen.py" (cmdline). Falls back to comm if cmdline is empty
// (kernel threads, processes that overwrote argv, etc.).
func readProcessName(pid int) string {
	if name := readCmdline(pid); name != "" {
		return name
	}
	return readComm(pid)
}

func readComm(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readCmdline returns a compact display string built from /proc/<pid>/cmdline.
// Algorithm: split argv by NUL, basename each non-flag argument, join with
// spaces, truncate to 40 chars. So "/usr/bin/python3 -u /app/imagegen.py"
// becomes "python3 imagegen.py".
func readCmdline(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return ""
	}
	// argv[0] etc. are NUL-terminated; trim trailing NULs first.
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return ""
	}
	args := strings.Split(string(b), "\x00")
	var parts []string
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		parts = append(parts, filepath.Base(a))
	}
	if len(parts) == 0 {
		return ""
	}
	out := strings.Join(parts, " ")
	const maxLen = 40
	if len(out) > maxLen {
		out = out[:maxLen-1] + "\u2026"
	}
	return out
}

// readFdInfo parses /proc/<pid>/fdinfo/<fd> as a "key:\tvalue" map.
// Values keep any trailing unit suffix (e.g. "5970176 KiB"); callers
// decide how to interpret them via parseUintField / firstUint.
func readFdInfo(pid int, fd string) map[string]string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/%s", pid, fd))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		k := line[:i]
		v := strings.TrimSpace(line[i+1:])
		out[k] = v
	}
	return out
}

// parseDrmCycles extracts every drm-cycles-<engine> and drm-total-cycles-<engine>
// pair from a parsed fdinfo map.
func parseDrmCycles(info map[string]string) (cycles, total map[string]uint64) {
	cycles = map[string]uint64{}
	total = map[string]uint64{}
	for k, v := range info {
		switch {
		case strings.HasPrefix(k, "drm-cycles-"):
			if n, ok := firstUint(v); ok {
				cycles[strings.TrimPrefix(k, "drm-cycles-")] = n
			}
		case strings.HasPrefix(k, "drm-total-cycles-"):
			if n, ok := firstUint(v); ok {
				total[strings.TrimPrefix(k, "drm-total-cycles-")] = n
			}
		}
	}
	return cycles, total
}

// firstUint parses the first whitespace-separated token of v as a uint64.
// Returns ok=false on empty input or parse error rather than panicking.
func firstUint(v string) (uint64, bool) {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseUintField pulls a "12345 KiB" or "12345" style numeric value out of a
// parsed fdinfo map and converts to bytes if a known suffix is present.
func parseUintField(info map[string]string, key string) uint64 {
	v, ok := info[key]
	if !ok {
		return 0
	}
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	if len(fields) >= 2 {
		switch strings.ToLower(fields[1]) {
		case "kib", "kb":
			n *= 1024
		case "mib", "mb":
			n *= 1024 * 1024
		case "gib", "gb":
			n *= 1024 * 1024 * 1024
		}
	}
	return n
}
