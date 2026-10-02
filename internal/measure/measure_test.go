package measure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/module"
)

// fakeDaemon streams samples every 20ms whose package energy grows at
// 100 J per sample (and core at 60), so any window has a known shape.
func fakeDaemon(t *testing.T, withEnergy bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		start := time.Now()
		for i := 0; ; i++ {
			s := collector.Sample{Timestamp: start.Add(time.Duration(i) * time.Second), Metrics: map[string][]module.Metric{}}
			if withEnergy {
				d := func(dom string, v float64) module.Metric {
					return module.Metric{Name: "energy", Value: v, Unit: "J", Labels: map[string]string{"domain": dom}}
				}
				s.Metrics["power"] = []module.Metric{d("package", float64(i)*100), d("core", float64(i)*60)}
			} else {
				s.Metrics["cpu"] = []module.Metric{{Name: "power", Value: 5, Unit: "W"}}
			}
			b, _ := json.Marshal(s)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			f.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRunReportsEnergyAndExitCode(t *testing.T) {
	srv := fakeDaemon(t, true)
	var out bytes.Buffer
	code, err := Run(Options{Addr: srv.URL, Command: []string{"sh", "-c", "sleep 0.1; exit 3"}, Report: &out})
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 {
		t.Fatalf("exit code = %d, want the command's 3", code)
	}
	text := out.String()
	for _, want := range []string{"exit 3", "CPU cores", "SoC total", "System total", "n/a", "System total unavailable"} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "above idle") {
		t.Error("baseline columns shown without --baseline")
	}
}

func TestRunJSONWithBaseline(t *testing.T) {
	srv := fakeDaemon(t, true)
	var out bytes.Buffer
	code, err := Run(Options{Addr: srv.URL, Baseline: 60 * time.Millisecond, JSON: true,
		Command: []string{"sh", "-c", "sleep 0.1"}, Report: &out})
	if err != nil || code != 0 {
		t.Fatalf("code %d err %v", code, err)
	}
	var res Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if res.ExitCode != 0 || res.CommandSeconds < 0.1 || res.Report.BaselineSeconds <= 0 {
		t.Fatalf("result = %+v", res)
	}
	soc, ok := res.Report.Line("soc")
	if !ok || !soc.Available || soc.Joules <= 0 || !soc.HasBaseline {
		t.Fatalf("soc line = %+v", soc)
	}
	// The fake daemon draws a constant 100 W, so energy above idle is ~0.
	if soc.NetJoules > 1e-6 || soc.NetJoules < -1e-6 {
		t.Fatalf("net joules = %v, want 0 for a constant-power daemon", soc.NetJoules)
	}
}

func TestRunErrors(t *testing.T) {
	if code, err := Run(Options{}); err == nil || code != 2 {
		t.Fatalf("no command: code %d err %v", code, err)
	}
	srv := fakeDaemon(t, false)
	var out bytes.Buffer
	code, err := Run(Options{Addr: srv.URL, Command: []string{"true"}, Report: &out})
	if err == nil || !strings.Contains(err.Error(), "no energy counters") {
		t.Fatalf("old daemon: code %d err %v", code, err)
	}
	srv2 := fakeDaemon(t, true)
	code, err = Run(Options{Addr: srv2.URL, Command: []string{"/nonexistent/binary"}, Report: &out})
	if err == nil || code != 127 {
		t.Fatalf("missing binary: code %d err %v", code, err)
	}
}
