package npu

import (
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/scottmbaker/topswatch/internal/module"
)

const defaultDriverPath = "/sys/bus/pci/drivers/intel_vpu"

type Module struct {
	driverPath    string
	sysfsDir      string
	pciSlot       string
	pciID         string
	gen           CPUGen
	memTotalBytes uint64
	info          module.DeviceInfo

	// PMT state (nil if PMT unavailable)
	pmt  *pmtDevice
	regs *genRegisters

	// Previous sample for delta calculations
	prevBusyUs     uint64
	prevBusyTime   time.Time
	prevEnergy     float64
	prevEnergyTime time.Time
	prevEnergySet  bool
	energyTotalJ   float64 // joules accumulated since start
	prevMemBW      uint64
	prevMemBWTime  time.Time
	prevMemBWSet   bool
}

func New(driverPath string) *Module {
	if driverPath == "" {
		driverPath = defaultDriverPath
	}
	return &Module{driverPath: driverPath}
}

func (m *Module) Name() string { return "npu" }

func (m *Module) Init() error {
	// Discover NPU device
	slot, sysfsDir, err := discoverNPUDevice(m.driverPath)
	if err != nil {
		return fmt.Errorf("no NPU device found at %s: %w", m.driverPath, err)
	}
	m.pciSlot = slot
	m.sysfsDir = sysfsDir

	// Detect CPU generation
	gen, pciID, err := detectGeneration(sysfsDir)
	if err != nil {
		return fmt.Errorf("cannot detect NPU generation: %w", err)
	}
	m.gen = gen
	m.pciID = pciID

	if gen == GenUnknown {
		log.Printf("[npu] unknown PCI device %s — PMT metrics will be unavailable", pciID)
	}

	// Build device info
	driverVer := readDriverVersion()
	fwVer := readFirmwareVersion(slot)
	m.memTotalBytes = readMemTotalBytes()
	extra := map[string]string{
		"generation": gen.ShortName(),
		"pci_slot":   slot,
	}
	if m.memTotalBytes > 0 {
		extra["memory_capacity"] = strconv.FormatUint(m.memTotalBytes, 10)
	}
	m.info = module.DeviceInfo{
		Name:            fmt.Sprintf("Intel NPU (%s)", gen),
		PCIDevice:       pciID,
		DriverVersion:   driverVer,
		FirmwareVersion: fwVer,
		Extra:           extra,
	}

	// Try to discover PMT
	if gen != GenUnknown {
		pmt, err := discoverPMT(gen)
		if err != nil {
			log.Printf("[npu] PMT unavailable: %v (sysfs metrics still active)", err)
		} else {
			regs, ok := registerTable[gen]
			if ok {
				m.pmt = pmt
				m.regs = &regs
				log.Printf("[npu] PMT telemetry found at %s", pmt.path)
			}
		}
	}

	// Prime the busy time baseline
	if busyUs, err := readBusyTimeUs(m.sysfsDir); err == nil {
		m.prevBusyUs = busyUs
		m.prevBusyTime = time.Now()
	}

	log.Printf("[npu] initialized: %s (%s), driver %s, firmware %s",
		gen, pciID, driverVer, fwVer)
	return nil
}

func (m *Module) DeviceInfo() module.DeviceInfo { return m.info }

func (m *Module) Collect() ([]module.Metric, error) {
	var metrics []module.Metric

	// --- Sysfs metrics ---

	// Utilization (delta-based)
	if busyUs, err := readBusyTimeUs(m.sysfsDir); err == nil {
		now := time.Now()
		if !m.prevBusyTime.IsZero() {
			deltaUs := busyUs - m.prevBusyUs
			intervalUs := uint64(now.Sub(m.prevBusyTime).Microseconds())
			if intervalUs > 0 {
				util := float64(deltaUs) / float64(intervalUs) * 100.0
				if util > 100 {
					util = 100
				}
				if util < 0 {
					util = 0
				}
				metrics = append(metrics, module.Metric{
					Name: "utilization", Value: util, Unit: "%",
				})
			}
		}
		m.prevBusyUs = busyUs
		m.prevBusyTime = now
	}

	// Memory (PTL+ only). On integrated NPUs the "capacity" is system RAM
	// since there's no dedicated VRAM, so we also emit a percent and a
	// total alongside the absolute used bytes.
	if memStr, err := readMemoryUtilization(m.sysfsDir); err == nil && memStr != "" {
		if bytes, ok := parseMemoryBytes(memStr); ok {
			metrics = append(metrics, module.Metric{
				Name: "memory_used", Value: float64(bytes), Unit: "bytes",
			})
			if m.memTotalBytes > 0 {
				metrics = append(metrics, module.Metric{
					Name: "memory_total", Value: float64(m.memTotalBytes), Unit: "bytes",
				})
				pct := float64(bytes) / float64(m.memTotalBytes) * 100
				if pct > 100 {
					pct = 100
				}
				metrics = append(metrics, module.Metric{
					Name: "memory_used_percent", Value: pct, Unit: "%",
				})
			}
		}
	}

	// --- PMT metrics ---

	if m.pmt != nil && m.regs != nil {
		if err := m.pmt.readBuffer(); err != nil {
			log.Printf("[npu] PMT read failed: %v", err)
		} else {
			metrics = append(metrics, m.collectPMT()...)
		}
	}

	return metrics, nil
}

func (m *Module) collectPMT() []module.Metric {
	var metrics []module.Metric

	// Temperature
	if raw, err := m.pmt.extractBits(m.regs.temperature); err == nil {
		metrics = append(metrics, module.Metric{
			Name: "temperature", Value: float64(raw), Unit: "C",
		})
	}

	// Frequency from workpoint bits [7:0]
	wpReg := m.regs.workpoint
	freqReg := registerDef{offset: wpReg.offset, bitLo: 0, bitHi: 7, size: wpReg.size}
	if raw, err := m.pmt.extractBits(freqReg); err == nil {
		mhz := freqRawToMHz(m.gen, raw)
		metrics = append(metrics, module.Metric{
			Name: "frequency", Value: mhz, Unit: "MHz",
		})
	}

	// Tile config from workpoint bits [23:16]
	tileReg := registerDef{offset: wpReg.offset, bitLo: 16, bitHi: 23, size: wpReg.size}
	if raw, err := m.pmt.extractBits(tileReg); err == nil {
		metrics = append(metrics, module.Metric{
			Name: "tile_config", Value: float64(raw), Unit: "",
		})
	}

	// DDR bandwidth (cumulative counter → MB/s via delta). One count is
	// memoryBWUnit bytes (Intel PMT XML: tbw_KB on MTL/ARL, tbw_1024B on
	// LNL/PTL). Verified on PTL against a synthetic NPU workload's own
	// weight traffic to within 1%.
	if raw, err := m.pmt.extractBits(m.regs.memoryBW); err == nil {
		now := time.Now()
		if m.prevMemBWSet {
			var dRaw uint64
			if raw >= m.prevMemBW {
				dRaw = raw - m.prevMemBW
			}
			dT := now.Sub(m.prevMemBWTime).Seconds()
			if dT > 0 {
				mbps := float64(dRaw) * m.regs.memoryBWUnit / 1e6 / dT
				metrics = append(metrics, module.Metric{
					Name: "ddr_bandwidth", Value: mbps, Unit: "MB/s",
				})
			}
		}
		m.prevMemBW = raw
		m.prevMemBWTime = now
		m.prevMemBWSet = true
	}

	// Power (energy delta)
	if raw, err := m.pmt.extractBits(m.regs.energy); err == nil {
		joules := energyToJoules(raw)
		now := time.Now()
		if m.prevEnergySet {
			deltaJ := joules - m.prevEnergy
			if deltaJ < 0 {
				deltaJ = 0
			}
			deltaT := now.Sub(m.prevEnergyTime).Seconds()
			if deltaT > 0 {
				watts := deltaJ / deltaT
				metrics = append(metrics, module.Metric{
					Name: "power", Value: watts, Unit: "W",
				})
			}
			// Cumulative energy, so clients can bracket a workload by
			// subtracting two readings. Labelled like the power module's
			// domains so it stays out of the headline (unlabelled) set.
			m.energyTotalJ += deltaJ
			metrics = append(metrics, module.Metric{
				Name: "energy", Value: m.energyTotalJ, Unit: "J",
				Labels: map[string]string{"domain": "npu"},
			})
		}
		m.prevEnergy = joules
		m.prevEnergyTime = now
		m.prevEnergySet = true
	}

	return metrics
}

func (m *Module) Close() error {
	return nil
}
