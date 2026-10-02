package gpu

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/scottmbaker/topswatch/internal/collectors/socpmt"
	"github.com/scottmbaker/topswatch/internal/module"
)

type Module struct {
	card   *cardDevice
	driver string
	info   module.DeviceInfo

	// Frequency readers (one will be non-nil)
	xeFreq   *freqReader
	i915Freq *i915FreqReader

	// hwmon reader (may be nil if no hwmon found)
	hwmon *hwmonReader

	// RAPL uncore power (may be nil)
	rapl           *raplUncore
	prevEnergyUj   uint64
	prevEnergyTime time.Time
	prevEnergySet  bool
	// PMT GT energy: fallback when the kernel exposes no RAPL uncore zone
	// (seen on a Panther Lake board). Same counter as the uncore MSR.
	gtPMT      *socpmt.Device
	prevGTJ    float64
	prevGTTime time.Time
	prevGTSet  bool

	// perf reader for engine-busy utilization (may be nil)
	perf *perfReader
}

func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "gpu" }

func (m *Module) Init() error {
	card, err := discoverIntelGPU()
	if err != nil {
		return fmt.Errorf("no Intel GPU found: %w", err)
	}
	m.card = card
	m.driver = detectDriver(card.deviceDir)

	m.info = module.DeviceInfo{
		Name:      fmt.Sprintf("Intel GPU (%s)", card.pciID),
		PCIDevice: card.pciID,
		Extra: map[string]string{
			"driver":   m.driver,
			"pci_slot": card.pciSlot,
			"card":     card.cardName,
		},
	}

	// Discover frequency interface
	m.xeFreq = discoverFreqDir(card.deviceDir)
	if m.xeFreq != nil {
		log.Printf("[gpu] Xe frequency sysfs found at %s", m.xeFreq.freqDir)
	} else {
		// Try i915 fallback
		m.i915Freq = discoverI915Freq(card.cardName)
		if m.i915Freq != nil {
			log.Printf("[gpu] i915 frequency sysfs found at %s", m.i915Freq.cardDir)
		} else {
			log.Printf("[gpu] no frequency sysfs found (frequency metrics unavailable)")
		}
	}

	// Discover hwmon (xe_hwmon — typically absent on PTL integrated GPUs)
	m.hwmon = discoverHwmon(card.deviceDir)
	if m.hwmon != nil {
		log.Printf("[gpu] hwmon found at %s", m.hwmon.hwmonDir)
	} else {
		log.Printf("[gpu] no xe_hwmon (GPU temperature unavailable)")
	}

	// Discover RAPL uncore zone for GPU power
	if r, err := discoverGPURapl(); err != nil {
		log.Printf("[gpu] RAPL uncore unavailable: %v (power metric disabled)", err)
	} else {
		m.rapl = r
		log.Printf("[gpu] RAPL uncore at %s", r.energyPath)
	}
	if m.rapl == nil {
		if d, err := socpmt.Open(); err == nil {
			m.gtPMT = d
			log.Printf("[gpu] GT energy from PMT (%s); power metric enabled", d.Generation())
		}
	}

	// Discover perf engine-busy counters
	if perf, err := discoverPerfReader(m.driver, card.pciSlot); err != nil {
		log.Printf("[gpu] perf engine-busy unavailable: %v (utilization metric disabled)", err)
	} else {
		m.perf = perf
		labels := make([]string, 0, len(perf.counters))
		for _, ec := range perf.counters {
			labels = append(labels, ec.label)
		}
		log.Printf("[gpu] perf engine-busy: %d engines (%s)", len(perf.counters), strings.Join(labels, ","))
		// Prime baselines
		_ = m.perf.readEngineBusy()
	}

	log.Printf("[gpu] initialized: %s, driver=%s, card=%s",
		card.pciID, m.driver, card.cardName)
	return nil
}

func (m *Module) DeviceInfo() module.DeviceInfo { return m.info }

func (m *Module) Collect() ([]module.Metric, error) {
	var metrics []module.Metric

	// --- Frequency metrics ---
	metrics = append(metrics, m.collectFreq()...)

	// --- hwmon temperature (when available) ---
	if m.hwmon != nil {
		metrics = append(metrics, m.collectHwmonTemp()...)
	}

	// --- RAPL uncore power ---
	if m.rapl != nil {
		if uj, err := m.rapl.readEnergyUj(); err == nil {
			now := time.Now()
			if m.prevEnergySet {
				var dUj uint64
				if uj >= m.prevEnergyUj {
					dUj = uj - m.prevEnergyUj
				} else if m.rapl.maxRange > 0 {
					dUj = (m.rapl.maxRange - m.prevEnergyUj) + uj
				}
				dT := now.Sub(m.prevEnergyTime).Seconds()
				if dT > 0 {
					metrics = append(metrics, module.Metric{
						Name: "power", Value: float64(dUj) / 1e6 / dT, Unit: "W",
					})
				}
			}
			m.prevEnergyUj = uj
			m.prevEnergyTime = now
			m.prevEnergySet = true
		}
	}

	if m.rapl == nil && m.gtPMT != nil {
		if j, err := m.gtPMT.GTEnergyJoules(); err == nil {
			now := time.Now()
			if m.prevGTSet {
				dT := now.Sub(m.prevGTTime).Seconds()
				if dJ := j - m.prevGTJ; dT > 0 && dJ >= 0 {
					metrics = append(metrics, module.Metric{
						Name: "power", Value: dJ / dT, Unit: "W",
					})
				}
			}
			m.prevGTJ, m.prevGTTime, m.prevGTSet = j, now, true
		}
	}

	// --- engine-busy utilization ---
	if m.perf != nil {
		busy := m.perf.readEngineBusy()
		var total float64
		for label, frac := range busy {
			metrics = append(metrics, module.Metric{
				Name: "engine_busy", Value: frac * 100, Unit: "%",
				Labels: map[string]string{"engine": label},
			})
			total += frac
		}
		if len(busy) > 0 {
			if total > 1 {
				total = 1
			}
			metrics = append(metrics, module.Metric{
				Name: "utilization", Value: total * 100, Unit: "%",
			})
		}
	}

	return metrics, nil
}

func (m *Module) collectFreq() []module.Metric {
	var metrics []module.Metric

	if m.xeFreq != nil {
		if v, ok := m.xeFreq.readFreqMHz("act_freq"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_actual", Value: v, Unit: "MHz",
			})
		}
		if v, ok := m.xeFreq.readFreqMHz("cur_freq"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_requested", Value: v, Unit: "MHz",
			})
		}
		if v, ok := m.xeFreq.readFreqMHz("min_freq"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_min", Value: v, Unit: "MHz",
			})
		}
		if v, ok := m.xeFreq.readFreqMHz("max_freq"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_max", Value: v, Unit: "MHz",
			})
		}
		if v, ok := m.xeFreq.readFreqMHz("rp0_freq"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_rp0", Value: v, Unit: "MHz",
				Labels: map[string]string{"desc": "maximum capable"},
			})
		}
		if v, ok := m.xeFreq.readFreqMHz("rpe_freq"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_rpe", Value: v, Unit: "MHz",
				Labels: map[string]string{"desc": "efficient"},
			})
		}
		if v, ok := m.xeFreq.readFreqMHz("rpn_freq"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_rpn", Value: v, Unit: "MHz",
				Labels: map[string]string{"desc": "minimum capable"},
			})
		}
	} else if m.i915Freq != nil {
		if v, ok := m.i915Freq.readFreqMHz("gt_act_freq_mhz"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_actual", Value: v, Unit: "MHz",
			})
		}
		if v, ok := m.i915Freq.readFreqMHz("gt_cur_freq_mhz"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_requested", Value: v, Unit: "MHz",
			})
		}
		if v, ok := m.i915Freq.readFreqMHz("gt_min_freq_mhz"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_min", Value: v, Unit: "MHz",
			})
		}
		if v, ok := m.i915Freq.readFreqMHz("gt_max_freq_mhz"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_max", Value: v, Unit: "MHz",
			})
		}
		if v, ok := m.i915Freq.readFreqMHz("gt_boost_freq_mhz"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_rp0", Value: v, Unit: "MHz",
				Labels: map[string]string{"desc": "boost"},
			})
		}
		if v, ok := m.i915Freq.readFreqMHz("gt_RPn_freq_mhz"); ok {
			metrics = append(metrics, module.Metric{
				Name: "frequency_rpn", Value: v, Unit: "MHz",
				Labels: map[string]string{"desc": "minimum capable"},
			})
		}
	}

	return metrics
}

func (m *Module) collectHwmonTemp() []module.Metric {
	var metrics []module.Metric
	for _, attr := range []string{"temp1_input", "temp2_input"} {
		if v, ok := m.hwmon.readTemperatureC(attr); ok {
			label := m.hwmon.readLabel(attr[:5] + "_label")
			if label == "" {
				label = "package"
			}
			metrics = append(metrics, module.Metric{
				Name: "temperature", Value: v, Unit: "C",
				Labels: map[string]string{"sensor": label},
			})
			break
		}
	}
	return metrics
}

func (m *Module) Close() error {
	if m.perf != nil {
		m.perf.Close()
	}
	return nil
}
