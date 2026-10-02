package collector

import (
	"log"
	"sync"
	"time"

	"github.com/scottmbaker/topswatch/internal/module"
	"github.com/scottmbaker/topswatch/internal/procwalk"
)

// Sample holds one snapshot from all modules.
type Sample struct {
	Timestamp time.Time                    `json:"timestamp"`
	Metrics   map[string][]module.Metric   `json:"metrics"`
	Devices   map[string]module.DeviceInfo `json:"devices"`
	Processes procwalk.Snapshot            `json:"processes"`
	Warnings  []Warning                    `json:"warnings,omitempty"`
}

// Collector polls modules at a fixed interval and stores samples in a ring buffer.
type Collector struct {
	modules  []module.Module
	interval time.Duration
	maxHist  int
	procs    *procwalk.Tracker
	warnings *WarningEvaluator
	multi    *MultiHistory

	mu      sync.RWMutex
	ring    []Sample
	pos     int
	count   int
	devices map[string]module.DeviceInfo

	// SSE subscribers
	subMu sync.Mutex
	subs  map[chan Sample]struct{}

	stopCh chan struct{}
}

func New(modules []module.Module, interval time.Duration, history int) *Collector {
	devices := map[string]module.DeviceInfo{}
	for _, m := range modules {
		devices[m.Name()] = m.DeviceInfo()
	}

	return &Collector{
		modules:  modules,
		interval: interval,
		maxHist:  history,
		procs:    procwalk.NewTracker(),
		warnings: NewWarningEvaluator(),
		multi:    NewMultiHistory(),
		ring:     make([]Sample, history),
		devices:  devices,
		subs:     make(map[chan Sample]struct{}),
		stopCh:   make(chan struct{}),
	}
}

// SetProcessRescan sets how often the per-process open-file scan for
// GPU/NPU clients runs (see procwalk.Tracker.SetRescanInterval). Zero
// scans on every sample.
func (c *Collector) SetProcessRescan(d time.Duration) {
	if c.procs != nil {
		c.procs.SetRescanInterval(d)
	}
}

// CollectOnce performs a single collection across all modules and returns the sample.
func (c *Collector) CollectOnce() Sample {
	s := Sample{
		Timestamp: time.Now(),
		Metrics:   make(map[string][]module.Metric),
		Devices:   c.devices,
	}
	for _, m := range c.modules {
		metrics, err := m.Collect()
		if err != nil {
			log.Printf("[collector] %s: %v", m.Name(), err)
			continue
		}
		s.Metrics[m.Name()] = metrics
	}
	if c.procs != nil {
		s.Processes = c.procs.Sample()
		if _, ok := s.Metrics["gpu"]; ok {
			s.Metrics["gpu"] = append(s.Metrics["gpu"], gpuMemoryMetrics(s.Processes)...)
		}
	}
	if c.warnings != nil {
		s.Warnings = c.warnings.Evaluate(s)
	}
	return s
}

// gpuMemoryMetrics turns the per-client memory split from the process
// walk into gpu module gauges, so it reaches history, the dashboard and
// Prometheus by the same path as every other metric. It lives here
// rather than in the gpu module because only the collector sees both the
// module output and the process walk.
//
// Every class is emitted on every sample, including empty ones: a class
// that stopped being reported would leave its last value frozen in the
// dashboard's history instead of dropping to zero.
func gpuMemoryMetrics(snap procwalk.Snapshot) []module.Metric {
	classes := []string{
		procwalk.ClassCompute,
		procwalk.ClassGraphics,
		procwalk.ClassComputeGraphics,
		procwalk.ClassVideo,
		procwalk.ClassIdle,
	}
	out := make([]module.Metric, 0, len(classes))
	for _, c := range classes {
		out = append(out, module.Metric{
			Name:   "memory_used",
			Value:  float64(snap.GPUMem.ByClass[c]),
			Unit:   "bytes",
			Labels: map[string]string{"class": c},
		})
	}
	return out
}

// Start begins the periodic collection loop.
func (c *Collector) Start() {
	go c.loop()
}

// Stop halts the collection loop.
func (c *Collector) Stop() {
	close(c.stopCh)
}

func (c *Collector) loop() {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// Collect immediately on start
	c.tick()

	for {
		select {
		case <-ticker.C:
			c.tick()
		case <-c.stopCh:
			return
		}
	}
}

func (c *Collector) tick() {
	s := c.CollectOnce()

	c.mu.Lock()
	c.ring[c.pos] = s
	c.pos = (c.pos + 1) % c.maxHist
	if c.count < c.maxHist {
		c.count++
	}
	c.mu.Unlock()

	if c.multi != nil {
		c.multi.Ingest(s)
	}

	// Notify SSE subscribers
	c.subMu.Lock()
	for ch := range c.subs {
		select {
		case ch <- s:
		default:
			// Slow subscriber, drop sample
		}
	}
	c.subMu.Unlock()
}

// Latest returns the most recent sample.
func (c *Collector) Latest() (Sample, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.count == 0 {
		return Sample{}, false
	}

	idx := (c.pos - 1 + c.maxHist) % c.maxHist
	return c.ring[idx], true
}

// History returns all stored samples in chronological order.
func (c *Collector) History() []Sample {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.count == 0 {
		return nil
	}

	result := make([]Sample, c.count)
	start := 0
	if c.count == c.maxHist {
		start = c.pos
	}
	for i := 0; i < c.count; i++ {
		idx := (start + i) % c.maxHist
		result[i] = c.ring[idx]
	}
	return result
}

// HistoryRange returns the downsampled history for a named tier (e.g.
// "5min", "1h", "24h"). Falls back to the short tier if name is empty
// or unknown. Returns ReducedSamples (no processes/warnings/devices).
func (c *Collector) HistoryRange(name string) []ReducedSample {
	if c.multi == nil {
		return nil
	}
	return c.multi.Range(name)
}

// AvailableRanges lists the named history tiers in display order.
func (c *Collector) AvailableRanges() []string {
	if c.multi == nil {
		return nil
	}
	return c.multi.AvailableRanges()
}

// Devices returns the static device info map.
func (c *Collector) Devices() map[string]module.DeviceInfo {
	return c.devices
}

// Subscribe returns a channel that receives new samples as they are collected.
func (c *Collector) Subscribe() chan Sample {
	ch := make(chan Sample, 8)
	c.subMu.Lock()
	c.subs[ch] = struct{}{}
	c.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (c *Collector) Unsubscribe(ch chan Sample) {
	c.subMu.Lock()
	delete(c.subs, ch)
	c.subMu.Unlock()
	close(ch)
}
