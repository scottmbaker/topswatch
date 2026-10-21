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
// GPU clients are found from the DRM core's per-device client list in
// debugfs (see drmclients.go) and only those processes' fd tables are
// read. Without that list, and for the NPU, which has no such list,
// discovery is via every process's /proc/<pid>/fd symlinks, so the
// package needs to run with sufficient privileges to read other
// processes' fds (typically CAP_SYS_PTRACE or root).
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

	"github.com/scottmbaker/topswatch/internal/sysfile"
)

// Snapshot is one polling result. It is fully self-contained — the caller
// can stash, JSON-serialize, or diff it without holding any tracker locks.
type Snapshot struct {
	Timestamp time.Time     `json:"timestamp"`
	GPU       []ProcessGPU  `json:"gpu,omitempty"`
	GPUMem    GPUMemSummary `json:"gpu_mem"`
	NPU       []ProcessNPU  `json:"npu,omitempty"`
	CPU       []ProcessCPU  `json:"cpu,omitempty"`
	// RescanSeconds is how often the whole process table is walked (0 or
	// absent = every sample). Viewers use it to say how fresh the process
	// lists are: the top-CPU list updates at this cadence and a new
	// GPU/NPU client can take this long to appear.
	RescanSeconds float64 `json:"rescan_seconds,omitempty"`
}

// Workload classes for a DRM client, mirroring the C / G / C+G column
// nvidia-smi shows. The class describes the client, not the memory: the
// hardware has no separate graphics and compute pools, so "compute
// memory" means "memory held by clients that do compute work".
const (
	ClassCompute         = "compute"
	ClassGraphics        = "graphics"
	ClassComputeGraphics = "compute+graphics"
	ClassVideo           = "video"
	ClassIdle            = "idle"
)

// ProcessGPU is one process attributing to a single DRM client (one fd).
// A single PID may have multiple ProcessGPU entries if it opened the
// device several times — in practice the dashboard sums them by PID.
type ProcessGPU struct {
	PID         int                `json:"pid"`
	Comm        string             `json:"comm"`
	Driver      string             `json:"driver,omitempty"`    // "xe" or "i915"
	ClientID    string             `json:"client_id,omitempty"` // drm-client-id
	Class       string             `json:"class,omitempty"`     // one of the Class* constants
	MemBytes    uint64             `json:"mem_bytes,omitempty"` // all BOs, summed across regions
	MemResident uint64             `json:"mem_resident_bytes,omitempty"`
	EngineBusy  map[string]float64 `json:"engine_busy,omitempty"` // engine -> 0..1, since previous sample
	TotalBusy   float64            `json:"total_busy"`            // sum across engines, clamped to 0..1
}

// GPUMemSummary aggregates client memory by workload class. Each client
// lands in exactly one bucket — a client doing both compute and graphics
// gets its own — so the buckets sum to Total.
//
// Buffers shared between clients are counted once per client, so Total
// can exceed the device's real footprint when clients share BOs.
type GPUMemSummary struct {
	ByClass map[string]uint64 `json:"by_class,omitempty"`
	Total   uint64            `json:"total_bytes"`
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
	// keyed by "<driver>:<drm-client-id>" (falling back to "<pid>:<fd>"
	// where the driver publishes no client id) — we lose state if a
	// process exits, which is fine.
	gpuState map[string]*fdState
	// keyed by pid for CPU jiffie deltas.
	cpuState map[int]*cpuPidState

	// rescanEvery is how often the process table is walked: the DRM
	// client list is read and those processes' open files scanned for GPU
	// handles, and every process's CPU time is read for the top-CPU list.
	// Zero walks on every sample. Between walks only the handles found
	// last time are re-read, which is far cheaper; the cost is that a
	// newly started client shows up to rescanEvery late and the top-CPU
	// list updates at that cadence. Client exits, per-client GPU usage and
	// memory are still fresh every sample.
	//
	// NPU holders have no kernel list, so finding them means scanning
	// every process's open files, the most expensive thing a sample does.
	// Nothing but a pid and a name comes of it, so that scan runs every
	// npuRescanFactor-th rescan. Where the DRM list is unreadable (no
	// debugfs, not root) the same full scan finds the GPU handles too and
	// runs at the rescan cadence, as before the list was used.
	rescanEvery time.Duration
	lastScan    time.Time
	lastNPUScan time.Time
	knownGPU    []accelFD
	knownNPU    []accelFD
	// drmRoot is the debugfs directory holding the DRM client lists.
	drmRoot string
	// accelDir is the device directory whose presence says an NPU exists.
	accelDir string
	// lastCPU is the top-CPU process list from the most recent full scan.
	// It is refreshed on the same cadence: reading every process's stat is
	// as costly as the open-file scan, and a busiest-processes list
	// averaged over the rescan window is, if anything, steadier.
	lastCPU []ProcessCPU
}

// accelFD is one open handle onto a GPU or NPU device node.
type accelFD struct {
	pid  int
	fd   string
	npu  bool
	comm string
	// info is the handle's fdinfo, held open between scans for GPU
	// clients and re-read in place every sample.
	info *sysfile.File
}

// npuRescanFactor is how many rescans pass between full open-file scans
// for NPU holders.
const npuRescanFactor = 4

// scanDue reports whether a rescan should run at now.
func (t *Tracker) scanDue(now time.Time) bool {
	return t.rescanEvery <= 0 || t.lastScan.IsZero() || now.Sub(t.lastScan) >= t.rescanEvery
}

// npuScanDue reports whether the full open-file scan for NPU holders
// should run at now.
func (t *Tracker) npuScanDue(now time.Time) bool {
	return t.rescanEvery <= 0 || t.lastNPUScan.IsZero() || now.Sub(t.lastNPUScan) >= npuRescanFactor*t.rescanEvery
}

// SetRescanInterval sets how often the full open-file scan runs. Zero (the
// default) scans on every sample.
func (t *Tracker) SetRescanInterval(d time.Duration) {
	t.mu.Lock()
	t.rescanEvery = d
	t.mu.Unlock()
}

const (
	driPrefix   = "/dev/dri/"
	accelPrefix = "/dev/accel/"
)

// scanAccelFDs walks the given processes' fd tables and returns the
// handles onto GPU and NPU device nodes. Over the whole process table
// this is the expensive part of a sample: one directory read per process
// and one readlink per open file. wantGPU and wantNPU select which
// device family's handles are returned.
func scanAccelFDs(pids []int, wantGPU, wantNPU bool) []accelFD {
	var out []accelFD
	for _, pid := range pids {
		fdDir := "/proc/" + strconv.Itoa(pid) + "/fd"
		fdNames, err := readDirNames(fdDir)
		if err != nil {
			continue // permission denied or process exited
		}
		// The display name costs two file reads, and only the few
		// processes holding a GPU or NPU handle need one. Resolve it on
		// first use instead of for every process.
		comm, commSet := "", false
		name := func() string {
			if !commSet {
				comm, commSet = readProcessName(pid), true
			}
			return comm
		}
		for _, fdNum := range fdNames {
			tgt, err := os.Readlink(fdDir + "/" + fdNum)
			if err != nil {
				continue
			}
			switch {
			case wantGPU && strings.HasPrefix(tgt, driPrefix):
				out = append(out, accelFD{pid: pid, fd: fdNum, comm: name()})
			case wantNPU && strings.HasPrefix(tgt, accelPrefix):
				out = append(out, accelFD{pid: pid, fd: fdNum, npu: true, comm: name()})
			}
		}
	}
	return out
}

// onePerClient keeps a single handle per DRM client and holds its fdinfo
// open. A process that dup(2)s its device fd (Mesa opens several per GL
// context) has one fdinfo per fd, all describing the same client, and
// reading fdinfo is one of the costlier things a sample does. Choosing
// the handle once per scan means the samples in between read each client
// exactly once, in place. Handles whose driver publishes no client id
// are all kept, as before.
func onePerClient(fds []accelFD) []accelFD {
	seen := map[string]bool{}
	out := fds[:0:0]
	for _, k := range fds {
		f, err := sysfile.Open(fmt.Sprintf("/proc/%d/fdinfo/%s", k.pid, k.fd))
		if err != nil {
			continue
		}
		info := parseFdInfo(f)
		if _, ok := info["drm-driver"]; !ok {
			f.Close() //nolint:errcheck
			continue  // not a DRM client handle
		}
		if id := info["drm-client-id"]; id != "" {
			ck := info["drm-driver"] + ":" + id
			if seen[ck] {
				f.Close() //nolint:errcheck
				continue
			}
			seen[ck] = true
		}
		k.info = f
		out = append(out, k)
	}
	return out
}

// revalidate keeps the known handles that still point at a GPU/NPU node.
// A closed fd, an exited process, or an fd number reused for something
// else drops out here, so exits are reflected without a full scan.
func revalidate(known []accelFD) []accelFD {
	out := known[:0:0]
	for _, k := range known {
		tgt, err := os.Readlink("/proc/" + strconv.Itoa(k.pid) + "/fd/" + k.fd)
		if err == nil && (k.npu && strings.HasPrefix(tgt, accelPrefix) || !k.npu && strings.HasPrefix(tgt, driPrefix)) {
			out = append(out, k)
			continue
		}
		k.info.Close() //nolint:errcheck
	}
	return out
}

func closeAll(fds []accelFD) {
	for _, k := range fds {
		k.info.Close() //nolint:errcheck
	}
}

func NewTracker() *Tracker {
	return &Tracker{
		gpuState: map[string]*fdState{},
		cpuState: map[int]*cpuPidState{},
		drmRoot:  drmDebugfs,
		accelDir: "/dev/accel",
	}
}

// Close releases the fdinfo handles held for known clients.
func (t *Tracker) Close() {
	t.mu.Lock()
	closeAll(t.knownGPU)
	closeAll(t.knownNPU)
	t.knownGPU, t.knownNPU = nil, nil
	t.mu.Unlock()
}

// hasNPU reports whether an NPU device node exists, so the full
// open-file scan only runs where it can find something.
func (t *Tracker) hasNPU() bool {
	entries, err := os.ReadDir(t.accelDir)
	return err == nil && len(entries) > 0
}

// discover refreshes the known GPU and NPU handles on a rescan. GPU
// clients come from the DRM list where it is readable; otherwise, and
// for the NPU on its slower cadence, from a walk of every process.
func (t *Tracker) discover(now time.Time, pids []int) {
	gpuPIDs, haveList := drmClientPIDs(t.drmRoot)
	npuDue := t.npuScanDue(now) && t.hasNPU()

	closeAll(t.knownGPU)
	switch {
	case haveList:
		// Only the processes the kernel says hold a GPU are scanned.
		list := make([]int, 0, len(gpuPIDs))
		for pid := range gpuPIDs {
			list = append(list, pid)
		}
		sort.Ints(list)
		t.knownGPU = onePerClient(scanAccelFDs(list, true, false))
		if npuDue {
			t.knownNPU = scanAccelFDs(pids, false, true)
			t.lastNPUScan = now
		}
	case npuDue:
		// One full walk serves both families.
		all := scanAccelFDs(pids, true, true)
		var gpu []accelFD
		t.knownNPU = t.knownNPU[:0]
		for _, k := range all {
			if k.npu {
				t.knownNPU = append(t.knownNPU, k)
			} else {
				gpu = append(gpu, k)
			}
		}
		t.knownGPU = onePerClient(gpu)
		t.lastNPUScan = now
	default:
		t.knownGPU = onePerClient(scanAccelFDs(pids, true, false))
	}
}

// Sample walks /proc once and returns the current snapshot.
func (t *Tracker) Sample() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	snap := Snapshot{Timestamp: now, RescanSeconds: t.rescanEvery.Seconds()}

	// The whole-process-table work (open-file scan and CPU attribution)
	// runs only when a scan is due; in between, known clients are re-read.
	scan := t.scanDue(now)
	var pids []int
	if scan {
		var err error
		if pids, err = listPids(); err != nil {
			return snap
		}
	}

	// Collect GPU entries, one per DRM client, so we can carry deltas
	// forward.
	type rawGPU struct {
		pid       int
		comm      string
		fdNum     string
		fdInfoRaw map[string]string
	}
	var rawGPUs []rawGPU

	// A process that dup(2)s its device fd — Mesa opens four for a single
	// GL context — gets one fdinfo per fd, but they all name the same
	// struct drm_file and so report the same buffer objects. Summing
	// per-fd would multiply that client's memory by its fd count. The
	// client id is unique per open, so it is the unit of accounting; the
	// first fd naming a client wins.
	seenClients := map[string]bool{}

	// Used to dedupe NPU entries by pid (a process might hold multiple
	// accel fds, but the user just wants to see the PID once).
	npuPids := map[int]string{}

	// Find the GPU/NPU handles: discovery when a rescan is due, otherwise
	// just re-check the ones found last time.
	if scan {
		t.discover(now, pids)
		t.lastScan = now
	} else {
		t.knownGPU = revalidate(t.knownGPU)
	}
	// NPU handles are revalidated every sample either way: discovery runs
	// rarely for them, but an exit should still show promptly.
	t.knownNPU = revalidate(t.knownNPU)

	for _, k := range t.knownNPU {
		if _, dup := npuPids[k.pid]; !dup {
			npuPids[k.pid] = k.comm
		}
	}
	for _, k := range t.knownGPU {
		info := parseFdInfo(k.info)
		if len(info) == 0 {
			continue
		}
		// Only include fds that look like a DRM client (have a driver field).
		if _, ok := info["drm-driver"]; !ok {
			continue
		}
		if id := info["drm-client-id"]; id != "" {
			ck := info["drm-driver"] + ":" + id
			if seenClients[ck] {
				continue // another fd onto a client we already counted
			}
			seenClients[ck] = true
		}
		rawGPUs = append(rawGPUs, rawGPU{
			pid: k.pid, comm: k.comm, fdNum: k.fd, fdInfoRaw: info,
		})
	}

	// Compute GPU deltas using prior state.
	newState := make(map[string]*fdState, len(rawGPUs))
	for _, r := range rawGPUs {
		// Key prior-sample state by DRM client, not fd: the client outlives
		// any single fd, and the dedupe above may pick a different fd for
		// the same client from one sample to the next.
		key := fmt.Sprintf("%d:%s", r.pid, r.fdNum)
		if id := r.fdInfoRaw["drm-client-id"]; id != "" {
			key = r.fdInfoRaw["drm-driver"] + ":" + id
		}
		curCycles, curTotal := parseDrmCycles(r.fdInfoRaw)
		mem, resident := parseDrmMemory(r.fdInfoRaw)

		entry := ProcessGPU{
			PID:         r.pid,
			Comm:        r.comm,
			Driver:      r.fdInfoRaw["drm-driver"],
			ClientID:    r.fdInfoRaw["drm-client-id"],
			Class:       classifyClient(curCycles),
			MemBytes:    mem,
			MemResident: resident,
			EngineBusy:  map[string]float64{},
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

	// Roll per-client memory up into the by-class split.
	snap.GPUMem = GPUMemSummary{ByClass: map[string]uint64{}}
	for _, g := range snap.GPU {
		snap.GPUMem.ByClass[g.Class] += g.MemBytes
		snap.GPUMem.Total += g.MemBytes
	}

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
	if scan {
		t.lastCPU = t.sampleCPUProcesses(now, 8, pids)
	}
	snap.CPU = t.lastCPU

	return snap
}

// --- helpers ---

// readDirNames lists a directory's entry names without sorting them or
// building DirEntry values, which os.ReadDir does and this walk does not
// need.
func readDirNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	names, err := f.Readdirnames(-1)
	f.Close() //nolint:errcheck
	return names, err
}

// listPids returns the numeric entries of /proc.
func listPids() ([]int, error) {
	names, err := readDirNames("/proc")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(names))
	for _, n := range names {
		if n == "" || n[0] < '0' || n[0] > '9' {
			continue
		}
		if pid, err := strconv.Atoi(n); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// procBuf bounds the one-shot reads of per-process files; comm and
// cmdline are short and stat is a few hundred bytes.
const procBuf = 4096

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
	b, err := sysfile.ReadInto(fmt.Sprintf("/proc/%d/comm", pid), make([]byte, procBuf))
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
	b, err := sysfile.ReadInto(fmt.Sprintf("/proc/%d/cmdline", pid), make([]byte, procBuf))
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

// parseFdInfo re-reads a held /proc/<pid>/fdinfo/<fd> and parses it as
// a "key:\tvalue" map. Values keep any trailing unit suffix (e.g.
// "5970176 KiB"); callers decide how to interpret them via
// parseUintField / firstUint.
func parseFdInfo(f *sysfile.File) map[string]string {
	if f == nil {
		return nil
	}
	b, err := f.Bytes()
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

// engineRole maps a DRM engine name to the workload role it implies. xe
// publishes the short ring names (rcs/ccs/vcs/vecs); i915 uses the long
// class names. Both are listed so classification doesn't depend on which
// driver is loaded.
//
// The blitter (bcs/copy) is deliberately absent: compute clients use it
// to upload weights just as graphics clients use it for transfers, so it
// carries no signal about what kind of client this is.
var engineRole = map[string]string{
	"rcs":           ClassGraphics,
	"render":        ClassGraphics,
	"ccs":           ClassCompute,
	"compute":       ClassCompute,
	"vcs":           ClassVideo,
	"video":         ClassVideo,
	"vecs":          ClassVideo,
	"video-enhance": ClassVideo,
}

// roleThreshold is the share of a client's engine cycles a role must
// account for before it counts toward the client's class.
//
// Mesa dispatches a trickle of compute work on behalf of an otherwise
// pure graphics context: a surfaceless GLES client measured 288 compute
// cycles against 560 million render cycles. Treating any non-zero count
// as significant would label every graphics client compute+graphics and
// file its memory under the wrong bucket.
const roleThreshold = 0.01

// classifyClient labels a DRM client by the engines it has ever used.
//
// The cycle counters are cumulative over the life of the client, not
// per-sample deltas, so the label is sticky: an LLM sitting idle between
// requests keeps its "compute" class instead of decaying to "idle". Only
// a client that has never run anything is ClassIdle. The flip side is
// that a client which changes character mid-life keeps its old label
// until the new work outweighs the old.
func classifyClient(cycles map[string]uint64) string {
	byRole := map[string]uint64{}
	var total uint64
	for eng, n := range cycles {
		role := engineRole[eng]
		if role == "" || n == 0 {
			continue // unknown engine, or the blitter, which tells us nothing
		}
		byRole[role] += n
		total += n
	}
	if total == 0 {
		return ClassIdle
	}
	significant := func(role string) bool {
		return float64(byRole[role])/float64(total) >= roleThreshold
	}
	compute, graphics, video := significant(ClassCompute), significant(ClassGraphics), significant(ClassVideo)
	switch {
	case compute && graphics:
		return ClassComputeGraphics
	case compute:
		return ClassCompute
	case graphics:
		return ClassGraphics
	case video:
		return ClassVideo
	}
	return ClassIdle
}

// parseDrmMemory sums the per-region memory keys published by the DRM
// core (drm-total-<region> and drm-resident-<region>, e.g. region "gtt"
// or "system"). Each buffer object is accounted to exactly one region,
// so summing across regions yields the client's whole footprint.
//
// Note "drm-total-cycles-<engine>" shares the drm-total- prefix and must
// not be mistaken for a memory region.
func parseDrmMemory(info map[string]string) (total, resident uint64) {
	for k := range info {
		switch {
		case strings.HasPrefix(k, "drm-total-cycles-"):
			continue
		case strings.HasPrefix(k, "drm-total-"):
			total += parseUintField(info, k)
		case strings.HasPrefix(k, "drm-resident-"):
			resident += parseUintField(info, k)
		}
	}
	return total, resident
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
