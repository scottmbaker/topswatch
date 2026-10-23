// Package affinity confines the daemon to a set of CPUs. Its purpose is
// energy, not speed: a monitor that wakes every second on whichever core
// is handy keeps the performance cores and their cache out of deep idle
// states; pinned to the low-power E-cores it runs where a wake-up costs
// least and leaves the cores the watched workload uses alone. The thread
// count is lowered to match, which also means fewer wake-ups.
package affinity

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Core type names accepted as a spec, resolved through the topology the
// caller supplies (see the cpu collector's CPUsByType).
const (
	LowPower    = "low_power"
	Efficient   = "efficient"
	Performance = "performance"
)

// Resolve turns a spec into a sorted CPU list. A spec is a core type
// name, or a kernel-style list such as "12-15" or "0,2,4-7". byType is
// the machine's CPUs per core type; a named type the machine lacks is
// an error rather than a silent fallback, since the point is to know
// where the daemon runs.
func Resolve(spec string, byType map[string][]int) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("empty cpu spec")
	}
	switch spec {
	case LowPower, Efficient, Performance:
		cpus := byType[spec]
		if len(cpus) == 0 {
			return nil, fmt.Errorf("this machine has no %s cores", strings.ReplaceAll(spec, "_", "-"))
		}
		out := append([]int(nil), cpus...)
		sort.Ints(out)
		return out, nil
	}
	cpus, err := parseList(spec)
	if err != nil {
		return nil, err
	}
	return cpus, nil
}

func parseList(s string) ([]int, error) {
	seen := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if i := strings.Index(part, "-"); i >= 0 {
			lo, hi = part[:i], part[i+1:]
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || a < 0 || b < a {
			return nil, fmt.Errorf("bad cpu list %q", s)
		}
		for n := a; n <= b; n++ {
			seen[n] = true
		}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("bad cpu list %q", s)
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

// Format renders a CPU list the way the kernel does ("12-15", "0,2-3").
func Format(cpus []int) string {
	if len(cpus) == 0 {
		return ""
	}
	sorted := append([]int(nil), cpus...)
	sort.Ints(sorted)
	var parts []string
	start, prev := sorted[0], sorted[0]
	flush := func() {
		if start == prev {
			parts = append(parts, strconv.Itoa(start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", start, prev))
		}
	}
	for _, c := range sorted[1:] {
		if c == prev+1 {
			prev = c
			continue
		}
		flush()
		start, prev = c, c
	}
	flush()
	return strings.Join(parts, ",")
}
