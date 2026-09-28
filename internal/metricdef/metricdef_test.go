package metricdef

import (
	"testing"

	"github.com/scottmbaker/topswatch/internal/module"
)

func TestRawSkipsLabeledUnlessAggregate(t *testing.T) {
	ms := []module.Metric{
		{Name: "utilization", Value: 50, Labels: map[string]string{"core": "0"}},
		{Name: "utilization", Value: 70, Labels: map[string]string{"core": "1"}},
		{Name: "utilization", Value: 60},
	}
	if v, ok := Raw(ms, Def{Key: "utilization"}); !ok || v != 60 {
		t.Fatalf("unlabeled: got %v %v, want 60 true", v, ok)
	}
	if v, ok := Raw(ms, Def{Key: "utilization", Aggregate: true}); !ok || v != 180 {
		t.Fatalf("aggregate: got %v %v, want 180 true", v, ok)
	}
	if _, ok := Raw(ms, Def{Key: "missing"}); ok {
		t.Fatal("missing key reported found")
	}
}

func TestValueAppliesTransform(t *testing.T) {
	ms := []module.Metric{{Name: "memory_used", Value: 2 * gib, Labels: map[string]string{"class": "compute"}}}
	d := Cards["gpu"][4]
	if d.Key != "memory_used" {
		t.Fatalf("gpu card 4 is %s, expected memory_used", d.Key)
	}
	if v, ok := Value(ms, d); !ok || v != 2 {
		t.Fatalf("got %v %v, want 2 true", v, ok)
	}
	if got := Format(2, d); got != "2.00" {
		t.Fatalf("Format = %q", got)
	}
}

func TestTableShape(t *testing.T) {
	for _, mod := range Order {
		defs, ok := Cards[mod]
		if !ok || len(defs) == 0 {
			t.Fatalf("module %s has no card definitions", mod)
		}
		for _, d := range defs {
			if d.Key == "" || d.Label == "" || d.Short == "" || d.Color == "" {
				t.Errorf("%s/%s: incomplete definition %+v", mod, d.Key, d)
			}
			if len(d.Color) != 7 || d.Color[0] != '#' {
				t.Errorf("%s/%s: color %q is not #rrggbb", mod, d.Key, d.Color)
			}
		}
	}
}
