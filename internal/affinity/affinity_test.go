package affinity

import (
	"reflect"
	"testing"
)

func TestResolve(t *testing.T) {
	byType := map[string][]int{
		Performance: {0, 1, 2, 3},
		Efficient:   {4, 5, 6, 7, 8, 9, 10, 11},
		LowPower:    {15, 14, 13, 12},
	}
	cases := []struct {
		spec string
		want []int
	}{
		{"low_power", []int{12, 13, 14, 15}},
		{"efficient", []int{4, 5, 6, 7, 8, 9, 10, 11}},
		{"performance", []int{0, 1, 2, 3}},
		{"12-15", []int{12, 13, 14, 15}},
		{" 0,2,4-6 ", []int{0, 2, 4, 5, 6}},
		{"3,3,1", []int{1, 3}},
	}
	for _, c := range cases {
		got, err := Resolve(c.spec, byType)
		if err != nil {
			t.Fatalf("%q: %v", c.spec, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%q: got %v, want %v", c.spec, got, c.want)
		}
	}
	for _, bad := range []string{"", "lp", "5-2", "a-b", ",", "-1"} {
		if _, err := Resolve(bad, byType); err == nil {
			t.Fatalf("%q: expected error", bad)
		}
	}
	// A non-hybrid machine has no low-power cores: say so.
	if _, err := Resolve("low_power", map[string][]int{Performance: {0, 1}}); err == nil {
		t.Fatal("expected error for a type the machine lacks")
	}
}

func TestFormat(t *testing.T) {
	cases := map[string][]int{
		"":          nil,
		"12-15":     {12, 13, 14, 15},
		"0,2-3,7":   {7, 0, 2, 3},
		"5":         {5},
		"0-1,4,6-7": {0, 1, 4, 6, 7},
	}
	for want, in := range cases {
		if got := Format(in); got != want {
			t.Fatalf("Format(%v) = %q, want %q", in, got, want)
		}
	}
}
