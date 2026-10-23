package gpu

import (
	"path/filepath"
	"time"

	"github.com/scottmbaker/topswatch/internal/sysfile"
)

// gtIdle reads the xe driver's per-GT idle residency,
// tile*/gt*/gtidle/idle_residency_ms: the milliseconds each GT has spent
// in its C6 (powered-down) state. It is the signal for whether the GPU
// has done any work since the last sample, and reading it costs nothing
// measurable, unlike holding the engine-busy perf events open, which
// keeps the GT out of C6 (see VIEWERS.md, "Where the daemon's time goes").
type gtIdle struct {
	gts      []*sysfile.File
	prevMs   []uint64
	prevWall time.Time
	primed   bool
}

func discoverGTIdle(deviceDir string) *gtIdle {
	var files []*sysfile.File
	for _, pattern := range []string{
		filepath.Join(deviceDir, "tile*", "gt*", "gtidle", "idle_residency_ms"),
		filepath.Join(deviceDir, "gt", "gt*", "gtidle", "idle_residency_ms"),
	} {
		matches, _ := filepath.Glob(pattern)
		for _, p := range matches {
			if f, err := sysfile.OpenUint(p); err == nil {
				files = append(files, f)
			}
		}
		if len(files) > 0 {
			break
		}
	}
	if len(files) == 0 {
		return nil
	}
	return &gtIdle{gts: files, prevMs: make([]uint64, len(files))}
}

// activeFraction returns the largest share of the interval since the
// previous call that any GT spent out of C6, so 0 for a GPU that slept
// through it and 1 for one that was awake throughout. The first call
// primes and returns 0.
func (g *gtIdle) activeFraction(now time.Time) float64 {
	wall := now.Sub(g.prevWall).Seconds() * 1000
	worst := 0.0
	for i, f := range g.gts {
		ms, err := f.Uint()
		if err != nil {
			continue
		}
		if g.primed && wall > 0 && ms >= g.prevMs[i] {
			idle := float64(ms - g.prevMs[i])
			if active := 1 - idle/wall; active > worst {
				worst = active
			}
		}
		g.prevMs[i] = ms
	}
	g.prevWall = now
	g.primed = true
	if worst > 1 {
		worst = 1
	}
	return worst
}

func (g *gtIdle) close() {
	if g == nil {
		return
	}
	for _, f := range g.gts {
		f.Close() //nolint:errcheck
	}
}
