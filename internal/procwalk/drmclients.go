package procwalk

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/scottmbaker/topswatch/internal/sysfile"
)

// drmDebugfs is where the DRM core publishes one "clients" file per
// device, listing every process that holds it open. Reading it for the
// handful of GPU clients is far cheaper than walking every process's
// open files to find them. It needs debugfs (root), which the daemon
// already has for RAPL, PMT and perf; without it the full walk is used.
const drmDebugfs = "/sys/kernel/debug/dri"

// drmClientPIDs returns the set of processes holding any DRM device
// open, and whether a client list was readable at all. Primary minors
// (0..63) are read; render minors (128+) are links to the same devices.
func drmClientPIDs(root string) (map[int]bool, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, false
	}
	pids := map[int]bool{}
	readAny := false
	buf := make([]byte, 64*1024)
	for _, e := range entries {
		minor, err := strconv.Atoi(e.Name())
		if err != nil || minor >= 128 {
			continue
		}
		b, err := sysfile.ReadInto(filepath.Join(root, e.Name(), "clients"), buf)
		if err != nil {
			continue
		}
		readAny = true
		for _, pid := range parseDRMClients(b) {
			pids[pid] = true
		}
	}
	return pids, readAny
}

// parseDRMClients extracts the tgid column of a DRM clients file:
//
//	    command  tgid dev master a   uid      magic ... name  id
//	gnome-shell  2232   0   n    y  1001          1 ... <unset>  11
//
// The command is a right-aligned fixed-width column that may contain
// spaces ("Web Content"), so the tgid is taken as the last field that
// ends where the header's "tgid" ends rather than by field number.
func parseDRMClients(b []byte) []int {
	lines := strings.Split(string(b), "\n")
	if len(lines) == 0 {
		return nil
	}
	hdr := lines[0]
	col := strings.Index(hdr, "tgid")
	if col < 0 {
		return nil
	}
	end := col + len("tgid")
	var out []int
	for _, line := range lines[1:] {
		if len(line) < end {
			continue
		}
		fields := strings.Fields(line[:end])
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid <= 0 {
			continue
		}
		out = append(out, pid)
	}
	return out
}
