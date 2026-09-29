package tui

import (
	"time"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/metricdef"
	"github.com/scottmbaker/topswatch/internal/module"
)

// maxPoints bounds each series. The snapshot renderer charts 300 points
// (5 minutes at 1s); the 1h tier is 360 buckets.
const maxPoints = 400

// point is one display-transformed value.
type point struct {
	t time.Time
	v float64
}

// store holds per-module, per-metric series of display values.
type store struct {
	series map[string]map[string][]point // mod -> key -> points
	// gen increments on every mutation; renderers use it to cache work.
	gen uint64
}

func newStore() *store {
	return &store{series: map[string]map[string][]point{}}
}

func (s *store) reset() {
	s.series = map[string]map[string][]point{}
	s.gen++
}

// push appends one sample's headline metrics, using the shared
// definitions so the values match the snapshot and web dashboard.
func (s *store) push(t time.Time, metrics map[string][]module.Metric) {
	for _, mod := range metricdef.Order {
		ms, ok := metrics[mod]
		if !ok {
			continue
		}
		for _, d := range metricdef.Cards[mod] {
			v, ok := metricdef.Value(ms, d)
			if !ok {
				continue
			}
			s.append(mod, d.Key, point{t, v})
		}
	}
}

func (s *store) append(mod, key string, p point) {
	s.gen++
	m, ok := s.series[mod]
	if !ok {
		m = map[string][]point{}
		s.series[mod] = m
	}
	pts := append(m[key], p)
	if len(pts) > maxPoints {
		pts = pts[len(pts)-maxPoints:]
	}
	m[key] = pts
}

// loadReduced replaces the store contents with a history tier.
func (s *store) loadReduced(hist []collector.ReducedSample) {
	s.reset()
	for _, rs := range hist {
		s.push(rs.Timestamp, rs.Metrics)
	}
}

// get returns the series for (mod, key), or nil.
func (s *store) get(mod, key string) []point {
	if m, ok := s.series[mod]; ok {
		return m[key]
	}
	return nil
}

// last returns the newest value for (mod, key).
func (s *store) last(mod, key string) (float64, bool) {
	pts := s.get(mod, key)
	if len(pts) == 0 {
		return 0, false
	}
	return pts[len(pts)-1].v, true
}

// values returns up to n newest values as a plain slice.
func values(pts []point, n int) []float64 {
	if n > 0 && len(pts) > n {
		pts = pts[len(pts)-n:]
	}
	out := make([]float64, len(pts))
	for i, p := range pts {
		out[i] = p.v
	}
	return out
}
