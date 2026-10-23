package gpu

// engineGate decides when the engine-busy perf events are worth holding
// open. While they are open the GuC keeps the GT awake to maintain them,
// about 1.15 W on an idle Panther Lake package, so they are released once
// every engine has sat idle for closeAfter samples and reopened as soon
// as the GT's idle residency shows it doing work again. While they are
// closed every engine reports 0% busy, which is what it is.
type engineGate struct {
	// closeAfter is how many consecutive all-idle samples close the
	// events. One busy sample reopens them.
	closeAfter int
	// wakeFraction is the share of a sample interval a GT must spend out
	// of C6 before the events are reopened. A desktop compositor's
	// occasional frame stays below it; any real work is far above.
	wakeFraction float64

	idleRun int
}

const (
	engineCloseAfter   = 5
	engineWakeFraction = 0.05
)

func newEngineGate() *engineGate {
	return &engineGate{closeAfter: engineCloseAfter, wakeFraction: engineWakeFraction}
}

// next returns whether the events should be open after this sample.
// open is their state now; busy is this sample's per-engine busy
// fractions (meaningful only when open); active is the GT's share of
// the interval out of C6 (meaningful only when closed, since open
// events keep the GT awake).
func (g *engineGate) next(open bool, busy map[string]float64, active float64) bool {
	if !open {
		if active >= g.wakeFraction {
			g.idleRun = 0
			return true
		}
		return false
	}
	if len(busy) == 0 {
		// No deltas yet (just opened): neither idle nor busy.
		return true
	}
	for _, f := range busy {
		if f > 0 {
			g.idleRun = 0
			return true
		}
	}
	g.idleRun++
	return g.idleRun < g.closeAfter
}
