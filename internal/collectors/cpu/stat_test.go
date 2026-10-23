package cpu

import "testing"

func TestParseStat(t *testing.T) {
	in := []byte("cpu  10 2 30 400 5 6 7 0 0 0\n" +
		"cpu0 1 0 3 40 1 0 0 0 0 0\n" +
		"cpu12 9 2 27 360 4 6 7 0 0 0\n" +
		"intr 1234 0 0\n" +
		"ctxt 99\n")
	agg, per, err := parseStat(in)
	if err != nil {
		t.Fatal(err)
	}
	if agg.total != 460 || agg.idle != 405 {
		t.Fatalf("aggregate = %+v", agg)
	}
	if len(per) != 2 {
		t.Fatalf("per-core = %v", per)
	}
	if per[0] != (perCoreStat{total: 45, idle: 41}) {
		t.Fatalf("cpu0 = %+v", per[0])
	}
	if per[12] != (perCoreStat{total: 415, idle: 364}) {
		t.Fatalf("cpu12 = %+v", per[12])
	}
	if _, _, err := parseStat([]byte("intr 1\n")); err == nil {
		t.Fatal("expected error without an aggregate line")
	}
}
