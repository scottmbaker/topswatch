//go:build !linux

package gpu

import "fmt"

type engineCounter struct {
	label string
}

type perfReader struct {
	counters []*engineCounter
}

func discoverPerfReader(driver, pciSlot string) (*perfReader, error) {
	return nil, fmt.Errorf("perf_event_open not supported on this platform")
}

func (r *perfReader) readEngineBusy() map[string]float64 { return nil }

func (r *perfReader) labels() []string { return nil }

func (r *perfReader) isOpen() bool { return false }

func (r *perfReader) open() error {
	return fmt.Errorf("perf_event_open not supported on this platform")
}

func (r *perfReader) close() {}

func (r *perfReader) Close() {}
