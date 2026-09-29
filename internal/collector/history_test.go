package collector_test

import (
	"testing"
	"time"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/testfixture"
)

func metric(rs collector.ReducedSample, mod, name string) (float64, bool) {
	for _, m := range rs.Metrics[mod] {
		if m.Name == name && len(m.Labels) == 0 {
			return m.Value, true
		}
	}
	return 0, false
}

// TestMultiHistoryOneSecondInterval is the baseline: the daemon's default
// 1s collection interval feeding the 1s short tier and 10s medium tier.
func TestMultiHistoryOneSecondInterval(t *testing.T) {
	mh := collector.NewMultiHistory()
	for i := 0; i < 60; i++ {
		mh.Ingest(testfixture.Sample(i))
	}
	// 60 samples at 1s: buckets 0..58 committed, bucket 59 still open.
	if got := len(mh.Range(collector.TierShort)); got != 59 {
		t.Fatalf("short tier: %d entries, want 59", got)
	}
	// 10s buckets: 0..49 committed (5 buckets), 50..59 open.
	if got := len(mh.Range(collector.TierMedium)); got != 5 {
		t.Fatalf("medium tier: %d entries, want 5", got)
	}
	if got := len(mh.Range(collector.TierLong)); got != 0 {
		t.Fatalf("long tier: %d entries, want 0 (5min bucket not yet closed)", got)
	}

	// Medium tier averages the 10 samples in each bucket.
	med := mh.Range(collector.TierMedium)
	var want float64
	for i := 0; i < 10; i++ {
		v, _ := sampleMetric(i, "cpu", "utilization")
		want += v
	}
	want /= 10
	got, ok := metric(med[0], "cpu", "utilization")
	if !ok || abs(got-want) > 1e-9 {
		t.Fatalf("medium bucket 0 cpu utilization = %v, want %v", got, want)
	}
	if !med[0].Timestamp.Equal(testfixture.Epoch) {
		t.Fatalf("bucket timestamp %v, want %v", med[0].Timestamp, testfixture.Epoch)
	}
}

// TestMultiHistorySlowerInterval checks that a daemon collecting every 5s
// (a candidate lower-overhead default) still produces sane tiers: the
// short tier simply holds fewer, sparser entries and no empty buckets.
func TestMultiHistorySlowerInterval(t *testing.T) {
	mh := collector.NewMultiHistory()
	for i := 0; i < 60; i += 5 {
		mh.Ingest(testfixture.Sample(i))
	}
	short := mh.Range(collector.TierShort)
	// 12 samples; the last one's bucket is still open.
	if len(short) != 11 {
		t.Fatalf("short tier: %d entries, want 11", len(short))
	}
	for i, rs := range short {
		want := testfixture.Epoch.Add(time.Duration(i*5) * time.Second)
		if !rs.Timestamp.Equal(want) {
			t.Fatalf("entry %d timestamp %v, want %v", i, rs.Timestamp, want)
		}
		if _, ok := metric(rs, "gpu", "utilization"); !ok {
			t.Fatalf("entry %d missing gpu utilization", i)
		}
	}
	if got := len(mh.Range(collector.TierMedium)); got != 5 {
		t.Fatalf("medium tier: %d entries, want 5", got)
	}
}

// TestMultiHistoryLabeledMetricsStaySeparate guards the per-core and
// per-class series against being averaged together.
func TestMultiHistoryLabeledMetricsStaySeparate(t *testing.T) {
	mh := collector.NewMultiHistory()
	for i := 0; i < 3; i++ {
		mh.Ingest(testfixture.Sample(i))
	}
	rs := mh.Range(collector.TierShort)[0]
	cores := map[string]bool{}
	classes := map[string]bool{}
	for _, m := range rs.Metrics["cpu"] {
		if m.Name == "utilization" && m.Labels["core"] != "" {
			cores[m.Labels["core"]] = true
		}
	}
	for _, m := range rs.Metrics["gpu"] {
		if m.Name == "memory_used" {
			classes[m.Labels["class"]] = true
		}
	}
	if len(cores) != 8 {
		t.Fatalf("expected 8 per-core series, got %d", len(cores))
	}
	if len(classes) != 5 {
		t.Fatalf("expected 5 gpu memory classes, got %d", len(classes))
	}
}

func TestMultiHistoryUnknownRangeFallsBackToShort(t *testing.T) {
	mh := collector.NewMultiHistory()
	for i := 0; i < 5; i++ {
		mh.Ingest(testfixture.Sample(i))
	}
	if a, b := len(mh.Range("bogus")), len(mh.Range(collector.TierShort)); a != b {
		t.Fatalf("unknown range returned %d entries, short tier has %d", a, b)
	}
	if got := mh.AvailableRanges(); len(got) != 3 || got[0] != "5min" || got[2] != "24h" {
		t.Fatalf("AvailableRanges = %v", got)
	}
}

func sampleMetric(i int, mod, name string) (float64, bool) {
	for _, m := range testfixture.Sample(i).Metrics[mod] {
		if m.Name == name && len(m.Labels) == 0 {
			return m.Value, true
		}
	}
	return 0, false
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
