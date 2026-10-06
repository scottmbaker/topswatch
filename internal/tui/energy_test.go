package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/scottmbaker/topswatch/internal/client"
	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/module"
	"github.com/scottmbaker/topswatch/internal/testfixture"
)

// energySample is a fixture sample with energy counters at pkg joules.
func energySample(i int, pkg float64) collector.Sample {
	s := testfixture.Sample(i)
	d := func(dom string, v float64) module.Metric {
		return module.Metric{Name: "energy", Value: v, Unit: "J", Labels: map[string]string{"domain": dom}}
	}
	s.Metrics["power"] = []module.Metric{d("package", pkg), d("core", pkg*0.6), d("uncore", pkg*0.2), d("dram", pkg*0.1), d("psys", pkg*2)}
	return s
}

func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func TestEnergyPanelOffByDefault(t *testing.T) {
	c, _ := client.New("localhost:1")
	m := newModel(c, Options{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	m.Update(sampleMsg(energySample(0, 0)))
	m.Update(key('s')) // must be inert without --energy
	out := m.View()
	if strings.Contains(out, "ENERGY") || strings.Contains(out, "idle baseline") {
		t.Fatal("energy panel shown without the flag")
	}
	m.cancel()
}

func TestEnergyPanelStopwatch(t *testing.T) {
	c, _ := client.New("localhost:1")
	m := newModel(c, Options{Energy: true, Baseline: 2 * time.Second})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})

	out := m.View()
	if !strings.Contains(out, "ENERGY") || !strings.Contains(out, "waiting for energy counters") {
		t.Fatalf("expected waiting state, got:\n%s", out)
	}

	m.Update(sampleMsg(energySample(0, 0)))
	out = m.View()
	if !strings.Contains(out, "press s to start") || strings.Contains(out, "System total") {
		t.Fatalf("idle panel should be one line with a hint:\n%s", out)
	}
	if !strings.Contains(out, "b idle baseline") {
		t.Fatal("footer should list the energy keys")
	}

	// Baseline over 2 s at 5 W, then measure 10 s at 36 W package.
	m.Update(key('b'))
	m.Update(sampleMsg(energySample(1, 5)))
	if !strings.Contains(m.View(), "capturing idle baseline") {
		t.Fatal("baseline capture not shown")
	}
	m.Update(sampleMsg(energySample(2, 10)))
	if !strings.Contains(m.View(), "idle baseline: system total 10.00 W") {
		t.Fatalf("baseline summary missing:\n%s", m.View())
	}
	m.Update(key('s'))
	m.Update(sampleMsg(energySample(12, 370)))
	out = m.View()
	for _, want := range []string{"measuring", "10.0s", "CPU cores", "GPU", "DRAM", "Rest of system", "System total", "above idle", "RAPL psys"} {
		if !strings.Contains(out, want) {
			t.Errorf("running panel missing %q", want)
		}
	}
	m.Update(key('s'))
	m.Update(sampleMsg(energySample(20, 9999))) // ignored once stopped
	out = m.View()
	if !strings.Contains(out, "stopped") || !strings.Contains(out, "100.0 mWh") {
		t.Fatalf("stopped panel wrong (want SoC total 360 J = 100.0 mWh):\n%s", out)
	}
	m.Update(key('c'))
	if strings.Contains(m.View(), "System total") {
		t.Fatal("c did not clear the result")
	}

	// Every line fits the terminal, at a size where charts must give way.
	m.Update(key('s'))
	m.Update(sampleMsg(energySample(30, 10000)))
	for _, size := range [][2]int{{120, 60}, {100, 36}, {80, 30}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(m.View(), "\n")
		if len(lines) > size[1] {
			t.Errorf("%dx%d: %d lines exceed height", size[0], size[1], len(lines))
		}
		for i, ln := range lines {
			if lipglossWidth(ln) > size[0] {
				t.Errorf("%dx%d: line %d too wide", size[0], size[1], i)
			}
		}
	}
	m.cancel()
}

func TestEnergyPanelTimedRecording(t *testing.T) {
	c, _ := client.New("localhost:1")
	m := newModel(c, Options{Energy: true, Record: 20 * time.Second})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	m.Update(sampleMsg(energySample(0, 0)))
	if !strings.Contains(m.View(), "t record 20s") {
		t.Fatalf("footer should show the record key and length:\n%s", m.View())
	}
	m.Update(key('t'))
	m.Update(sampleMsg(energySample(5, 50)))
	out := m.View()
	if !strings.Contains(out, "measuring") || !strings.Contains(out, "of 20s, 15.0s left") {
		t.Fatalf("timed status missing:\n%s", out)
	}
	m.Update(sampleMsg(energySample(20, 200)))
	if !strings.Contains(m.View(), "stopped 20.0s") {
		t.Fatalf("did not stop itself:\n%s", m.View())
	}
	m.cancel()
}
