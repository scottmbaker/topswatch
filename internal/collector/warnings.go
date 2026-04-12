package collector

import (
	"fmt"
	"sync"
	"time"

	"github.com/scottmbaker/topswatch/internal/module"
)

// Warning is one currently-active warning condition derived from the
// most recent sample. Warnings are stateful: each rule must be true for
// at least its `duration` before the warning is emitted, and disappears
// the instant the condition becomes false. The Since field reports when
// the rule first started being continuously true.
type Warning struct {
	Module   string    `json:"module"`   // "cpu" / "gpu" / "npu"
	Kind     string    `json:"kind"`     // "thermal" / "throttle" / "memory"
	Severity string    `json:"severity"` // "warning" / "critical"
	Message  string    `json:"message"`
	Since    time.Time `json:"since"`
}

// warningRule defines one detection rule.
type warningRule struct {
	id       string
	module   string
	kind     string
	severity string
	duration time.Duration
	// eval inspects the latest sample. Returns (true, message) if the
	// condition is currently met, or (false, "") otherwise.
	eval func(s Sample) (bool, string)
}

// WarningEvaluator runs the rule set against each new sample and tracks
// per-rule "since" timestamps so a rule must be sustained for its
// configured duration before it becomes an active warning.
type WarningEvaluator struct {
	mu    sync.Mutex
	rules []warningRule
	since map[string]time.Time
}

func NewWarningEvaluator() *WarningEvaluator {
	return &WarningEvaluator{
		rules: defaultRules(),
		since: map[string]time.Time{},
	}
}

// Evaluate returns the list of warnings active for this sample. The slice
// is sorted by severity (critical first) then by Since (oldest first).
func (e *WarningEvaluator) Evaluate(s Sample) []Warning {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := s.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	var out []Warning
	seen := make(map[string]bool, len(e.rules))
	for _, r := range e.rules {
		active, msg := r.eval(s)
		seen[r.id] = active
		if !active {
			delete(e.since, r.id)
			continue
		}
		first, ok := e.since[r.id]
		if !ok {
			e.since[r.id] = now
			first = now
		}
		if now.Sub(first) < r.duration {
			continue // not yet sustained long enough
		}
		out = append(out, Warning{
			Module:   r.module,
			Kind:     r.kind,
			Severity: r.severity,
			Message:  msg,
			Since:    first,
		})
	}
	// Drop state for rules we didn't see at all (defensive — shouldn't happen
	// since the rule set is fixed, but keeps the map from growing if rules
	// are ever added at runtime).
	for id := range e.since {
		if _, ok := seen[id]; !ok {
			delete(e.since, id)
		}
	}
	return out
}

// --- rule definitions ---

func defaultRules() []warningRule {
	return []warningRule{
		// CPU thermal: warning at 90C sustained, critical at 95C immediately.
		{
			id: "cpu_temp_critical", module: "cpu", kind: "thermal", severity: "critical",
			duration: 0,
			eval: func(s Sample) (bool, string) {
				v, ok := metricValue(s, "cpu", "temperature", nil)
				if !ok || v < 95 {
					return false, ""
				}
				return true, fmt.Sprintf("CPU at %.0f\u00b0C — critical thermal", v)
			},
		},
		{
			id: "cpu_temp_warning", module: "cpu", kind: "thermal", severity: "warning",
			duration: 10 * time.Second,
			eval: func(s Sample) (bool, string) {
				v, ok := metricValue(s, "cpu", "temperature", nil)
				if !ok || v < 90 {
					return false, ""
				}
				return true, fmt.Sprintf("CPU at %.0f\u00b0C", v)
			},
		},

		// NPU thermal: warning at 90C sustained, critical at 95C.
		{
			id: "npu_temp_critical", module: "npu", kind: "thermal", severity: "critical",
			duration: 0,
			eval: func(s Sample) (bool, string) {
				v, ok := metricValue(s, "npu", "temperature", nil)
				if !ok || v < 95 {
					return false, ""
				}
				return true, fmt.Sprintf("NPU at %.0f\u00b0C — critical thermal", v)
			},
		},
		{
			id: "npu_temp_warning", module: "npu", kind: "thermal", severity: "warning",
			duration: 10 * time.Second,
			eval: func(s Sample) (bool, string) {
				v, ok := metricValue(s, "npu", "temperature", nil)
				if !ok || v < 90 {
					return false, ""
				}
				return true, fmt.Sprintf("NPU at %.0f\u00b0C", v)
			},
		},

		// NPU memory pressure: warning at 80% of system RAM mapped for NPU.
		{
			id: "npu_memory_pressure", module: "npu", kind: "memory", severity: "warning",
			duration: 10 * time.Second,
			eval: func(s Sample) (bool, string) {
				v, ok := metricValue(s, "npu", "memory_used_percent", nil)
				if !ok || v < 80 {
					return false, ""
				}
				return true, fmt.Sprintf("NPU memory at %.0f%% of system RAM", v)
			},
		},

		// GPU throttle: actual freq sustained below 80% of max while busy.
		// "Busy" guard prevents firing when the GPU is just sitting at low
		// freq with nothing to do.
		{
			id: "gpu_throttle", module: "gpu", kind: "throttle", severity: "warning",
			duration: 30 * time.Second,
			eval: func(s Sample) (bool, string) {
				util, ok := metricValue(s, "gpu", "utilization", nil)
				if !ok || util < 50 {
					return false, ""
				}
				act, ok := metricValue(s, "gpu", "frequency_actual", nil)
				if !ok {
					return false, ""
				}
				maxF, ok := metricValue(s, "gpu", "frequency_max", nil)
				if !ok || maxF == 0 {
					return false, ""
				}
				ratio := act / maxF
				if ratio >= 0.8 {
					return false, ""
				}
				return true, fmt.Sprintf("GPU at %.0f/%.0f MHz (%.0f%% of max) while busy", act, maxF, ratio*100)
			},
		},
	}
}

// metricValue finds a metric by module name + metric name + optional label
// match. Returns the most recent matching value from the sample.
func metricValue(s Sample, mod, name string, labels map[string]string) (float64, bool) {
	for _, m := range s.Metrics[mod] {
		if m.Name != name {
			continue
		}
		if labels != nil && !labelMatch(m, labels) {
			continue
		}
		return m.Value, true
	}
	return 0, false
}

func labelMatch(m module.Metric, want map[string]string) bool {
	for k, v := range want {
		if m.Labels[k] != v {
			return false
		}
	}
	return true
}
