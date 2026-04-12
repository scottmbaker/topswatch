//nolint:errcheck // text output to io.Writer; errors are not actionable
package textout

import (
	"fmt"
	"io"
	"sort"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/module"
)

// PrintSample writes a one-shot text summary to w.
func PrintSample(w io.Writer, s collector.Sample) {
	fmt.Fprintln(w, "TopsWatch")
	fmt.Fprintln(w)

	names := sortedKeys(s.Devices)

	for _, name := range names {
		dev := s.Devices[name]

		switch name {
		case "cpu":
			printCPU(w, dev, s.Metrics[name])
		case "gpu":
			printGPU(w, dev, s.Metrics[name])
		case "npu":
			printNPU(w, dev, s.Metrics[name])
		default:
			fmt.Fprintf(w, "%s\n", name)
			printMetricList(w, s.Metrics[name])
		}

		fmt.Fprintln(w)
	}
}

func printCPU(w io.Writer, dev module.DeviceInfo, metrics []module.Metric) {
	fmt.Fprintln(w, "CPU")
	field(w, "Model", dev.Name)
	if v := dev.Extra["vendor"]; v != "" {
		field(w, "Vendor", v)
	}
	cores := dev.Extra["cores"]
	threads := dev.Extra["threads"]
	if cores != "" || threads != "" {
		field(w, "Cores", fmt.Sprintf("%s physical, %s threads", cores, threads))
	}
	if v := dev.Extra["arch"]; v != "" {
		field(w, "Arch", v)
	}
	if len(metrics) > 0 {
		fmt.Fprintln(w)
		printMetricList(w, metrics)
	}
}

func printGPU(w io.Writer, dev module.DeviceInfo, metrics []module.Metric) {
	fmt.Fprintln(w, "GPU")
	field(w, "Device", fmt.Sprintf("%s (%s)", dev.Name, dev.PCIDevice))
	if v := dev.Extra["driver"]; v != "" {
		field(w, "Driver", v)
	}
	if len(metrics) > 0 {
		fmt.Fprintln(w)
		printMetricList(w, metrics)
	}
}

func printNPU(w io.Writer, dev module.DeviceInfo, metrics []module.Metric) {
	fmt.Fprintln(w, "NPU")
	field(w, "Device", fmt.Sprintf("%s (%s)", dev.Name, dev.PCIDevice))
	if dev.DriverVersion != "" {
		field(w, "Driver", dev.DriverVersion)
	}
	if dev.FirmwareVersion != "" {
		field(w, "Firmware", dev.FirmwareVersion)
	}
	if len(metrics) > 0 {
		fmt.Fprintln(w)
		printMetricList(w, metrics)
	}
}

func printMetricList(w io.Writer, metrics []module.Metric) {
	for _, m := range metrics {
		// Skip labeled metrics in text mode. Per-core CPU utilization,
		// per-engine GPU busy, and similar would otherwise produce dozens
		// of indistinguishable rows. Aggregate (unlabeled) metrics carry
		// the headline numbers; the web UI handles labeled detail.
		if len(m.Labels) > 0 {
			continue
		}
		field(w, metricLabel(m.Name), formatMetricValue(m))
	}
}

func formatMetricValue(m module.Metric) string {
	if m.Unit == "bytes" {
		return formatBytes(m.Value)
	}
	if m.Unit != "" {
		return fmt.Sprintf("%.4g %s", m.Value, m.Unit)
	}
	return fmt.Sprintf("%.4g", m.Value)
}

func formatBytes(v float64) string {
	const (
		kib = 1024.0
		mib = 1024.0 * 1024.0
		gib = 1024.0 * 1024.0 * 1024.0
		tib = 1024.0 * 1024.0 * 1024.0 * 1024.0
	)
	switch {
	case v >= tib:
		return fmt.Sprintf("%.2f TiB", v/tib)
	case v >= gib:
		return fmt.Sprintf("%.2f GiB", v/gib)
	case v >= mib:
		return fmt.Sprintf("%.1f MiB", v/mib)
	case v >= kib:
		return fmt.Sprintf("%.0f KiB", v/kib)
	default:
		return fmt.Sprintf("%.0f B", v)
	}
}

func field(w io.Writer, label, value string) {
	fmt.Fprintf(w, "  %-16s %s\n", label+":", value)
}

var labelMap = map[string]string{
	"utilization":         "Utilization",
	"cores_used":          "Cores Busy",
	"frequency":           "Frequency",
	"frequency_actual":    "Freq (actual)",
	"frequency_requested": "Freq (requested)",
	"frequency_min":       "Freq (min)",
	"frequency_max":       "Freq (max)",
	"frequency_rp0":       "Freq (RP0 max)",
	"frequency_rpe":       "Freq (RPe eff)",
	"frequency_rpn":       "Freq (RPn min)",
	"power":               "Power",
	"temperature":         "Temperature",
	"ddr_bandwidth":       "DDR Bandwidth",
	"tile_config":         "Tile Config",
	"memory_used":         "Memory Used",
	"memory_total":        "Memory Total",
	"memory_used_percent": "Memory %",
}

func metricLabel(name string) string {
	if l, ok := labelMap[name]; ok {
		return l
	}
	return name
}

func sortedKeys(m map[string]module.DeviceInfo) []string {
	// Force "cpu" first, then alphabetical
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i] == "cpu" {
			return true
		}
		if keys[j] == "cpu" {
			return false
		}
		return keys[i] < keys[j]
	})
	return keys
}
