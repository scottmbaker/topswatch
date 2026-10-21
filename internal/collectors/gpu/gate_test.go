package gpu

import "testing"

func TestEngineGate(t *testing.T) {
	g := newEngineGate()
	idle := map[string]float64{"rcs0": 0, "ccs0": 0}
	busy := map[string]float64{"rcs0": 0, "ccs0": 0.4}

	// Open and busy: stays open; the first sample after opening has no
	// deltas and is not counted as idle.
	if !g.next(true, nil, 0) || !g.next(true, busy, 0) {
		t.Fatal("busy engines must keep the events open")
	}
	// Four idle samples: still open. The fifth closes.
	for i := 0; i < engineCloseAfter-1; i++ {
		if !g.next(true, idle, 1) {
			t.Fatalf("closed after %d idle samples", i+1)
		}
	}
	if g.next(true, idle, 1) {
		t.Fatal("expected close after engineCloseAfter idle samples")
	}
	// Closed: a sleeping GT keeps it closed, a working one reopens.
	if g.next(false, nil, 0.01) {
		t.Fatal("a GT asleep must not reopen")
	}
	if !g.next(false, nil, 0.5) {
		t.Fatal("an active GT must reopen")
	}
	// Reopened: the idle run starts over.
	for i := 0; i < engineCloseAfter-1; i++ {
		if !g.next(true, idle, 1) {
			t.Fatalf("closed again after %d idle samples", i+1)
		}
	}
	// A busy sample resets the run.
	if !g.next(true, busy, 1) || g.idleRun != 0 {
		t.Fatal("busy must reset the idle run")
	}
}
