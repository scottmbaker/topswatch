package energy

import "time"

// State of a stopwatch session.
type State int

const (
	Idle    State = iota // nothing measured yet
	Running              // counting
	Stopped              // result frozen
)

// Session is the start/stop stopwatch shared by the TUI and GUI. Feed it
// every reading with Observe; Toggle and StartBaseline are the user
// actions. It holds no clock of its own: time comes from the readings, so
// it behaves the same in streaming and polling viewers.
type Session struct {
	BaselineFor time.Duration // length of an idle-baseline capture

	latest    Reading
	hasLatest bool

	state State
	start Reading
	final Report

	baseCapturing bool
	baseStart     Reading
	base          *Report
}

// NewSession returns a session whose baseline captures last d.
func NewSession(d time.Duration) *Session {
	if d <= 0 {
		d = 10 * time.Second
	}
	return &Session{BaselineFor: d}
}

// Observe records a new reading and completes a baseline capture once
// enough time has passed.
func (s *Session) Observe(r Reading) {
	if !r.Available() {
		return
	}
	s.latest, s.hasLatest = r, true
	if s.baseCapturing && r.Time.Sub(s.baseStart.Time) >= s.BaselineFor {
		rep := Diff(s.baseStart, r)
		s.base = &rep
		s.baseCapturing = false
	}
}

// Supported reports whether the daemon provides energy counters at all.
func (s *Session) Supported() bool { return s.hasLatest }

// Toggle starts a measurement, or stops the running one. Starting again
// after a stop begins a fresh measurement.
func (s *Session) Toggle() {
	if !s.hasLatest {
		return
	}
	switch s.state {
	case Running:
		s.final = s.report(s.latest)
		s.state = Stopped
	default:
		s.start = s.latest
		s.state = Running
	}
}

// Reset clears the measurement (the baseline is kept).
func (s *Session) Reset() { s.state = Idle }

// StartBaseline begins an idle-baseline capture. The device should be
// idle for BaselineFor.
func (s *Session) StartBaseline() {
	if !s.hasLatest {
		return
	}
	s.baseStart = s.latest
	s.baseCapturing = true
}

// ClearBaseline discards the idle baseline.
func (s *Session) ClearBaseline() { s.base, s.baseCapturing = nil, false }

func (s *Session) report(end Reading) Report {
	rep := Diff(s.start, end)
	if s.base != nil {
		rep = rep.WithBaseline(*s.base)
	}
	return rep
}

// State returns the stopwatch state.
func (s *Session) State() State { return s.state }

// Report returns the live report while running and the frozen one after
// a stop. ok is false when nothing has been measured.
func (s *Session) Report() (Report, bool) {
	switch s.state {
	case Running:
		return s.report(s.latest), true
	case Stopped:
		return s.final, true
	}
	return Report{}, false
}

// Baseline describes the idle baseline: capturing (with seconds remaining)
// or captured (with its report).
func (s *Session) Baseline() (capturing bool, remaining time.Duration, base *Report) {
	if s.baseCapturing {
		rem := s.BaselineFor - s.latest.Time.Sub(s.baseStart.Time)
		if rem < 0 {
			rem = 0
		}
		return true, rem, nil
	}
	return false, 0, s.base
}
