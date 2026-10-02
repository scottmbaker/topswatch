package procwalk

import (
	"testing"
	"time"
)

func TestScanDue(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	tr := NewTracker()

	// Default: scan on every sample (the original behaviour).
	if !tr.scanDue(now) {
		t.Fatal("first sample must scan")
	}
	tr.lastScan = now
	if !tr.scanDue(now.Add(time.Second)) {
		t.Fatal("rescan interval 0 must scan every sample")
	}

	tr.SetRescanInterval(5 * time.Second)
	if tr.scanDue(now.Add(4 * time.Second)) {
		t.Fatal("scan ran before the interval elapsed")
	}
	if !tr.scanDue(now.Add(5 * time.Second)) {
		t.Fatal("scan did not run once the interval elapsed")
	}

	// A fresh tracker scans immediately whatever the interval.
	tr2 := NewTracker()
	tr2.SetRescanInterval(time.Hour)
	if !tr2.scanDue(now) {
		t.Fatal("first sample must scan even with a long interval")
	}
}

func TestRevalidateDropsDeadHandles(t *testing.T) {
	// PID 0 never has a /proc entry, so the handle cannot be read back.
	got := revalidate([]accelFD{{pid: 0, fd: "3"}, {pid: 0, fd: "4", npu: true}})
	if len(got) != 0 {
		t.Fatalf("dead handles survived revalidation: %+v", got)
	}
}
