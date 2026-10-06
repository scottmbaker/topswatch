package web

import (
	"testing"

	"github.com/scottmbaker/topswatch/internal/module"
)

// TestMetricToPromName pins the exported Prometheus metric names. These
// are a public contract consumed by the Grafana dashboard in grafana/.
func TestMetricToPromName(t *testing.T) {
	cases := []struct {
		mod  string
		m    module.Metric
		want string
	}{
		{"cpu", module.Metric{Name: "utilization", Unit: "%"}, "topswatch_cpu_utilization_percent"},
		{"cpu", module.Metric{Name: "frequency", Unit: "MHz"}, "topswatch_cpu_frequency_mhz"},
		{"cpu", module.Metric{Name: "power", Unit: "W"}, "topswatch_cpu_power_watts"},
		{"cpu", module.Metric{Name: "temperature", Unit: "C"}, "topswatch_cpu_temperature_celsius"},
		{"cpu", module.Metric{Name: "cores_used", Unit: "cores"}, "topswatch_cpu_cores_used"},
		{"gpu", module.Metric{Name: "memory_used", Unit: "bytes"}, "topswatch_gpu_memory_used_bytes"},
		{"gpu", module.Metric{Name: "frequency_actual", Unit: "MHz"}, "topswatch_gpu_frequency_actual_mhz"},
		{"npu", module.Metric{Name: "ddr_bandwidth", Unit: "MB/s"}, "topswatch_npu_ddr_bandwidth_mbps"},
		{"npu", module.Metric{Name: "memory_used_percent", Unit: "%"}, "topswatch_npu_memory_used_percent"},
		{"npu", module.Metric{Name: "tile_config", Unit: ""}, "topswatch_npu_tile_config"},
		// Energy counters (added with the power collector).
		{"power", module.Metric{Name: "energy", Unit: "J"}, "topswatch_power_energy_joules_total"},
		{"npu", module.Metric{Name: "energy", Unit: "J"}, "topswatch_npu_energy_joules_total"},
		{"power", module.Metric{Name: "measured_seconds", Unit: "s"}, "topswatch_power_measured_seconds_total"},
		{"power", module.Metric{Name: "power", Unit: "W"}, "topswatch_power_power_watts"},
		{"power", module.Metric{Name: "battery_voltage", Unit: "V"}, "topswatch_power_battery_voltage_volts"},
		{"power", module.Metric{Name: "battery_capacity", Unit: "%"}, "topswatch_power_battery_capacity_percent"},
	}
	for _, c := range cases {
		if got := metricToPromName(c.mod, c.m); got != c.want {
			t.Errorf("%s/%s (%q): got %s, want %s", c.mod, c.m.Name, c.m.Unit, got, c.want)
		}
	}
}
