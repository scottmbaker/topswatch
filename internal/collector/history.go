package collector

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scottmbaker/topswatch/internal/module"
)

// HistoryTier names. Exposed via the ?range= query parameter on
// /api/metrics/history.
const (
	TierShort  = "5min" // 1-second resolution, 5 minutes back
	TierMedium = "1h"   // 10-second resolution, 1 hour back
	TierLong   = "24h"  // 5-minute resolution, 24 hours back
)

// tierConfig describes one downsampling tier.
type tierConfig struct {
	name     string
	bucket   time.Duration
	capacity int
}

var tiers = []tierConfig{
	{TierShort, 1 * time.Second, 300},
	{TierMedium, 10 * time.Second, 360},
	{TierLong, 5 * time.Minute, 288},
}

// ReducedSample is a downsampled history entry. It carries only the
// minimum needed by the dashboard's charts: timestamp + flattened metric
// list. Processes, warnings, and device info are intentionally omitted
// because the page-load history endpoint doesn't render them.
type ReducedSample struct {
	Timestamp time.Time                  `json:"timestamp"`
	Metrics   map[string][]module.Metric `json:"metrics"`
}

// MultiHistory holds three rings (short / medium / long) and downsamples
// new samples into each on the fly. Each tier is its own ring buffer. New
// samples are folded into the current open bucket of each tier; when the
// bucket window has elapsed, the bucket's running average is committed
// and a new bucket starts.
type MultiHistory struct {
	mu    sync.RWMutex
	rings [3]*tierRing
}

type tierRing struct {
	cfg   tierConfig
	ring  []ReducedSample
	pos   int
	count int
	// Open bucket: running sums of metrics by key, plus the count of
	// samples folded in. Committed to ring when bucket end passes.
	bucketStart time.Time
	bucketSums  map[string]*runningSum
}

type runningSum struct {
	mod    string
	metric module.Metric // template for name + unit + labels
	sum    float64
	count  int
}

func NewMultiHistory() *MultiHistory {
	mh := &MultiHistory{}
	for i, t := range tiers {
		mh.rings[i] = &tierRing{
			cfg:        t,
			ring:       make([]ReducedSample, t.capacity),
			bucketSums: map[string]*runningSum{},
		}
	}
	return mh
}

// Ingest folds a fresh sample into all three tiers.
func (mh *MultiHistory) Ingest(s Sample) {
	mh.mu.Lock()
	defer mh.mu.Unlock()
	for _, r := range mh.rings {
		r.ingest(s)
	}
}

// Range returns the requested tier's contents in chronological order.
// Defaults to the short tier if name is empty or unknown.
func (mh *MultiHistory) Range(name string) []ReducedSample {
	mh.mu.RLock()
	defer mh.mu.RUnlock()
	r := mh.rings[0]
	for _, candidate := range mh.rings {
		if candidate.cfg.name == name {
			r = candidate
			break
		}
	}
	return r.snapshot()
}

// AvailableRanges returns the names of all tiers in display order.
func (mh *MultiHistory) AvailableRanges() []string {
	out := make([]string, len(tiers))
	for i, t := range tiers {
		out[i] = t.name
	}
	return out
}

// --- tierRing internals ---

func (r *tierRing) ingest(s Sample) {
	// First sample ever: open the first bucket aligned to the sample's timestamp.
	if r.bucketStart.IsZero() {
		r.bucketStart = s.Timestamp.Truncate(r.cfg.bucket)
	}
	// Walk forward closing buckets until the sample falls inside the open one.
	for !s.Timestamp.Before(r.bucketStart.Add(r.cfg.bucket)) {
		r.commitBucket()
		r.bucketStart = r.bucketStart.Add(r.cfg.bucket)
	}
	// Fold the sample into the open bucket's running sums.
	for mod, metrics := range s.Metrics {
		for _, m := range metrics {
			key := bucketKey(mod, m)
			rs, ok := r.bucketSums[key]
			if !ok {
				// Clone metric so we don't share label maps with the input.
				labels := map[string]string{}
				for k, v := range m.Labels {
					labels[k] = v
				}
				rs = &runningSum{
					mod: mod,
					metric: module.Metric{
						Name: m.Name, Unit: m.Unit, Labels: labels,
					},
				}
				r.bucketSums[key] = rs
			}
			rs.sum += m.Value
			rs.count++
		}
	}
}

func (r *tierRing) commitBucket() {
	if len(r.bucketSums) == 0 {
		return
	}
	rs := ReducedSample{
		Timestamp: r.bucketStart,
		Metrics:   map[string][]module.Metric{},
	}
	for _, sum := range r.bucketSums {
		if sum.count == 0 {
			continue
		}
		m := sum.metric
		m.Value = sum.sum / float64(sum.count)
		rs.Metrics[sum.mod] = append(rs.Metrics[sum.mod], m)
	}
	// Sort metrics within each module by name for deterministic output.
	// Done here at write time so concurrent snapshot readers don't race
	// on the same underlying slices.
	for mod := range rs.Metrics {
		ms := rs.Metrics[mod]
		sort.Slice(ms, func(a, b int) bool { return ms[a].Name < ms[b].Name })
	}
	// Reset open bucket.
	r.bucketSums = map[string]*runningSum{}

	// Append to ring.
	r.ring[r.pos] = rs
	r.pos = (r.pos + 1) % r.cfg.capacity
	if r.count < r.cfg.capacity {
		r.count++
	}
}

func (r *tierRing) snapshot() []ReducedSample {
	if r.count == 0 {
		return nil
	}
	out := make([]ReducedSample, r.count)
	start := 0
	if r.count == r.cfg.capacity {
		start = r.pos
	}
	for i := 0; i < r.count; i++ {
		out[i] = r.ring[(start+i)%r.cfg.capacity]
	}
	return out
}

// bucketKey computes a stable per-metric key incorporating labels so that
// metrics with the same name but different labels (e.g. per-core CPU util)
// don't get averaged together.
func bucketKey(mod string, m module.Metric) string {
	if len(m.Labels) == 0 {
		return mod + "|" + m.Name
	}
	keys := make([]string, 0, len(m.Labels))
	for k := range m.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(mod)
	b.WriteByte('|')
	b.WriteString(m.Name)
	for _, k := range keys {
		b.WriteByte('|')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m.Labels[k])
	}
	return b.String()
}
