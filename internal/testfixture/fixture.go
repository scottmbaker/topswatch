// Package testfixture builds deterministic samples for tests. The values are
// pure functions of the sample index so golden outputs are stable across
// runs and platforms.
package testfixture

import (
	"fmt"
	"math"
	"time"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/module"
)

// Epoch is the timestamp of sample 0. Later samples are one second apart.
var Epoch = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// Devices returns static device info for all three modules.
func Devices() map[string]module.DeviceInfo {
	return map[string]module.DeviceInfo{
		"cpu": {
			Name: "Intel(R) Core(TM) Ultra 5 335",
			Extra: map[string]string{
				"vendor": "GenuineIntel", "cores": "8", "threads": "8", "arch": "x86_64",
			},
		},
		"gpu": {
			Name:      "Intel Arc Graphics",
			PCIDevice: "8086:b0a0",
			Extra:     map[string]string{"driver": "xe", "card": "card0", "pci_slot": "0000:00:02.0"},
		},
		"npu": {
			Name:            "Intel NPU 5720",
			PCIDevice:       "8086:b03e",
			DriverVersion:   "intel_vpu 1.19.0",
			FirmwareVersion: "20260701*MTL_CLIENT_SILICON-release",
			Extra:           map[string]string{"generation": "5"},
		},
	}
}

// wave returns base + amp*sin(i/period), rounded to 3 decimals.
func wave(i int, base, amp, period float64) float64 {
	v := base + amp*math.Sin(float64(i)/period)
	return math.Round(v*1000) / 1000
}

func m(name string, v float64, unit string) module.Metric {
	return module.Metric{Name: name, Value: v, Unit: unit}
}

func ml(name string, v float64, unit string, labels map[string]string) module.Metric {
	return module.Metric{Name: name, Value: v, Unit: unit, Labels: labels}
}

// Sample returns the i-th deterministic sample.
func Sample(i int) collector.Sample {
	s := collector.Sample{
		Timestamp: Epoch.Add(time.Duration(i) * time.Second),
		Metrics:   map[string][]module.Metric{},
		Devices:   Devices(),
	}

	cpu := []module.Metric{
		m("utilization", wave(i, 35, 25, 9), "%"),
		m("cores_used", wave(i, 2.8, 2, 9), "cores"),
		m("frequency", wave(i, 2600, 500, 7), "MHz"),
		m("power", wave(i, 12, 4, 5), "W"),
		m("temperature", wave(i, 58, 6, 13), "C"),
	}
	types := []string{"performance", "performance", "performance", "performance",
		"efficient", "efficient", "low_power", "low_power"}
	for c := 0; c < 8; c++ {
		lbl := map[string]string{"core": fmt.Sprint(c), "core_type": types[c]}
		cpu = append(cpu,
			ml("utilization", wave(i+c*3, 30, 30, 6), "%", lbl),
			ml("frequency", wave(i+c*3, 2400, 600, 8), "MHz", lbl),
		)
	}
	s.Metrics["cpu"] = cpu

	gpu := []module.Metric{
		m("utilization", wave(i, 40, 40, 11), "%"),
		m("frequency_actual", wave(i, 1500, 600, 10), "MHz"),
		m("frequency_requested", wave(i, 1600, 600, 10), "MHz"),
		m("frequency_min", 300, "MHz"),
		m("frequency_max", 2350, "MHz"),
		m("frequency_rp0", 2350, "MHz"),
		m("frequency_rpe", 900, "MHz"),
		m("frequency_rpn", 300, "MHz"),
		m("power", wave(i, 8, 5, 6), "W"),
		m("temperature", wave(i, 52, 8, 12), "C"),
	}
	for _, eng := range []string{"rcs0", "ccs0", "vcs0", "vecs0"} {
		gpu = append(gpu, ml("busy", wave(i+len(eng), 30, 30, 7), "%",
			map[string]string{"engine": eng}))
	}
	const gib = 1024 * 1024 * 1024
	classMem := map[string]float64{
		"compute": 1.25 * gib, "graphics": 0.5 * gib, "compute+graphics": 0,
		"video": 0.125 * gib, "idle": 0.0625 * gib,
	}
	for _, cls := range []string{"compute", "graphics", "compute+graphics", "video", "idle"} {
		gpu = append(gpu, ml("memory_used", classMem[cls]+float64(i%7)*1e6, "bytes",
			map[string]string{"class": cls}))
	}
	s.Metrics["gpu"] = gpu

	s.Metrics["npu"] = []module.Metric{
		m("utilization", wave(i, 50, 50, 8), "%"),
		m("frequency", wave(i, 1200, 700, 9), "MHz"),
		m("power", wave(i, 3, 2.5, 4), "W"),
		m("temperature", wave(i, 48, 7, 15), "C"),
		m("ddr_bandwidth", wave(i, 6000, 5000, 8), "MB/s"),
		m("tile_config", 6, ""),
		m("memory_used", 512*1024*1024+float64(i%5)*1e6, "bytes"),
		m("memory_total", 2*gib, "bytes"),
		m("memory_used_percent", wave(i, 25, 3, 5), "%"),
	}
	return s
}

// History returns n consecutive samples starting at sample 0.
func History(n int) []collector.Sample {
	out := make([]collector.Sample, n)
	for i := range out {
		out[i] = Sample(i)
	}
	return out
}
