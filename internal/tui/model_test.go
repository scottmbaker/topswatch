package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/scottmbaker/topswatch/internal/client"
	"github.com/scottmbaker/topswatch/internal/testfixture"
)

// TestSampleSuppliesDevices covers the daemon-unreachable-at-startup path:
// the devices fetch fails, then the first live sample fills devices in.
func TestSampleSuppliesDevices(t *testing.T) {
	c, _ := client.New("localhost:1")
	m := newModel(c, Options{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.Update(errMsg{err: client.ErrNoData})
	if m.devices != nil {
		t.Fatal("devices set before any sample")
	}
	m.Update(sampleMsg(testfixture.Sample(0)))
	if m.devices == nil || m.devices["gpu"].Name == "" {
		t.Fatal("first sample did not supply devices")
	}
	if m.lastErr != "" {
		t.Fatalf("error not cleared after sample: %q", m.lastErr)
	}
	if _, ok := m.store.last("cpu", "utilization"); !ok {
		t.Fatal("sample not pushed into store")
	}
	m.cancel()
}

func TestRangeCycleResetsStore(t *testing.T) {
	c, _ := client.New("localhost:1")
	m := newModel(c, Options{})
	m.Update(rangesMsg{"5min", "1h", "24h"})
	m.Update(sampleMsg(testfixture.Sample(0)))
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m.tier() != "1h" {
		t.Fatalf("tier after r = %s", m.tier())
	}
	if _, ok := m.store.last("cpu", "utilization"); ok {
		t.Fatal("store not reset on range change")
	}
	// Live samples are not appended to downsampled tiers.
	m.Update(sampleMsg(testfixture.Sample(1)))
	if _, ok := m.store.last("cpu", "utilization"); ok {
		t.Fatal("live sample appended to 1h tier")
	}
	// A stale history reply for another tier is ignored.
	m.Update(historyMsg{tier: "24h"})
	m.cancel()
}
