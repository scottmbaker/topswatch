package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/metricdef"
	"github.com/scottmbaker/topswatch/internal/testfixture"
)

func fixtureStore(n int) *store {
	st := newStore()
	for i := 0; i < n; i++ {
		s := testfixture.Sample(i)
		st.push(s.Timestamp, s.Metrics)
	}
	return st
}

func TestStorePushUsesSharedDefinitions(t *testing.T) {
	st := fixtureStore(3)
	for _, mod := range metricdef.Order {
		for _, d := range metricdef.Cards[mod] {
			pts := st.get(mod, d.Key)
			if len(pts) != 3 {
				t.Errorf("%s/%s: %d points, want 3", mod, d.Key, len(pts))
			}
		}
	}
	// gpu memory is the summed, GB-transformed value.
	v, _ := st.last("gpu", "memory_used")
	if v < 1.9 || v > 2.0 {
		t.Errorf("gpu memory_used = %v GB, want ~1.94", v)
	}
	// npu ddr bandwidth is MB/s -> GB/s.
	raw, _ := metricdef.Raw(testfixture.Sample(2).Metrics["npu"], metricdef.Cards["npu"][4])
	v, _ = st.last("npu", "ddr_bandwidth")
	if v != raw/1000 {
		t.Errorf("ddr_bandwidth = %v, want %v", v, raw/1000)
	}
}

func TestStoreCapsPoints(t *testing.T) {
	st := fixtureStore(maxPoints + 50)
	if n := len(st.get("cpu", "utilization")); n != maxPoints {
		t.Fatalf("series length %d, want %d", n, maxPoints)
	}
}

func TestLoadReducedReplaces(t *testing.T) {
	mh := collector.NewMultiHistory()
	for i := 0; i < 40; i++ {
		mh.Ingest(testfixture.Sample(i))
	}
	st := fixtureStore(5)
	st.loadReduced(mh.Range(collector.TierMedium))
	if n := len(st.get("cpu", "power")); n != 3 {
		t.Fatalf("after loadReduced: %d points, want 3 (10s buckets over 40s)", n)
	}
}

func TestSparkline(t *testing.T) {
	if got := sparkline(nil, 5, 0); got != "     " {
		t.Errorf("empty: %q", got)
	}
	got := sparkline([]float64{0, 50, 100}, 5, 100)
	if got != "  ▁▄█" {
		t.Errorf("fixed max: %q", got)
	}
	// Autoscale: min maps to lowest block, max to highest.
	got = sparkline([]float64{10, 20}, 2, 0)
	if got != "▁█" {
		t.Errorf("autoscale: %q", got)
	}
	// More values than width keeps the newest.
	got = sparkline([]float64{0, 0, 0, 100}, 2, 100)
	if got != "▁█" {
		t.Errorf("tail: %q", got)
	}
}

func TestComputeLayout(t *testing.T) {
	l := computeLayout(120, 50, 3)
	if l.chartH < minChartH {
		t.Fatalf("tall terminal got chartH %d", l.chartH)
	}
	l = computeLayout(80, 18, 3)
	if l.chartH != 0 || !l.gap {
		t.Fatalf("short terminal should drop charts but keep gaps, got %+v", l)
	}
	l = computeLayout(60, 16, 3)
	if l.chartH != 0 || l.gap {
		t.Fatalf("very short terminal should drop gaps too, got %+v", l)
	}
}

func TestRenderFrame(t *testing.T) {
	f := frame{
		addr:    "http://localhost:9876",
		now:     testfixture.Epoch,
		tier:    "5min",
		mode:    "stream",
		devices: testfixture.Devices(),
		store:   fixtureStore(90),
		warnings: []collector.Warning{{
			Module: "cpu", Kind: "thermal", Severity: "critical", Message: "package 99C",
		}},
	}
	for _, size := range [][2]int{{120, 50}, {80, 24}, {60, 16}} {
		w, h := size[0], size[1]
		out := render(f, computeLayout(w, h, 3))
		lines := strings.Split(out, "\n")
		if len(lines) > h {
			t.Errorf("%dx%d: rendered %d lines, exceeds height", w, h, len(lines))
		}
		for i, ln := range lines {
			if lw := visibleWidth(ln); lw > w {
				t.Errorf("%dx%d: line %d is %d cells wide: %q", w, h, i, lw, ln)
			}
		}
		for _, want := range []string{"TopsWatch", "CPU", "NPU", "GPU", "UTIL", "package 99C"} {
			if !strings.Contains(out, want) {
				t.Errorf("%dx%d: output missing %q", w, h, want)
			}
		}
	}
}

func TestHeaderStripsScheme(t *testing.T) {
	out := renderHeader(frame{addr: "http://nuc335.local:9876", now: testfixture.Epoch, tier: "5min", mode: "stream"}, 100)
	if strings.Contains(out, "http://") || !strings.Contains(out, "nuc335.local:9876") {
		t.Fatalf("header = %q", out)
	}
}

func TestRenderEmptyStore(t *testing.T) {
	f := frame{addr: "x", now: time.Now(), tier: "5min", devices: testfixture.Devices(), store: newStore()}
	out := render(f, computeLayout(100, 40, 3))
	if !strings.Contains(out, "--") {
		t.Fatal("expected placeholder values before first sample")
	}
}

func visibleWidth(s string) int { return lipglossWidth(s) }

func TestChartCacheInvalidation(t *testing.T) {
	st := fixtureStore(30)
	c := newChartCache()
	calls := 0
	build := func() string { calls++; return "x" }
	c.get("cpu", 100, 8, st.gen, build)
	c.get("cpu", 100, 8, st.gen, build)
	if calls != 1 {
		t.Fatalf("same gen/size rebuilt: %d calls", calls)
	}
	c.get("cpu", 90, 8, st.gen, build)
	if calls != 2 {
		t.Fatalf("resize did not rebuild: %d calls", calls)
	}
	s := testfixture.Sample(30)
	st.push(s.Timestamp, s.Metrics)
	c.get("cpu", 90, 8, st.gen, build)
	if calls != 3 {
		t.Fatalf("new sample did not rebuild: %d calls", calls)
	}
	// A nil cache always builds (tests and one-shot renders).
	var nc *chartCache
	nc.get("cpu", 1, 1, 0, build)
	if calls != 4 {
		t.Fatal("nil cache should build")
	}
}
