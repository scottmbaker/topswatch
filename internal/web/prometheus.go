package web

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/module"
)

// promCollector implements prometheus.Collector, translating our metrics to Prometheus.
type promCollector struct {
	coll *collector.Collector
}

func (p *promCollector) Describe(ch chan<- *prometheus.Desc) {
	// We use unchecked collectors — describe nothing, collect everything.
}

func (p *promCollector) Collect(ch chan<- prometheus.Metric) {
	sample, ok := p.coll.Latest()
	if !ok {
		return
	}

	// Emit info metrics for each device
	for name, dev := range sample.Devices {
		labels := prometheus.Labels{}
		switch name {
		case "cpu":
			labels["model"] = dev.Name
			labels["vendor"] = dev.Extra["vendor"]
			labels["cores"] = dev.Extra["cores"]
			labels["threads"] = dev.Extra["threads"]
			labels["arch"] = dev.Extra["arch"]
		case "gpu":
			labels["pci_id"] = dev.PCIDevice
			labels["driver"] = dev.Extra["driver"]
			labels["pci_slot"] = dev.Extra["pci_slot"]
			labels["card"] = dev.Extra["card"]
		case "npu":
			labels["pci_id"] = dev.PCIDevice
			labels["generation"] = dev.Extra["generation"]
			labels["driver"] = dev.DriverVersion
			labels["firmware"] = dev.FirmwareVersion
		}

		desc := prometheus.NewDesc(
			"topswatch_"+name+"_info",
			name+" device information",
			nil, labels,
		)
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, 1)
	}

	// Emit gauge metrics
	for name, metrics := range sample.Metrics {
		for _, m := range metrics {
			promName := metricToPromName(name, m)
			desc := prometheus.NewDesc(promName, promHelp(name, m), nil, promLabels(m))
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, m.Value)
		}
	}
}

func metricToPromName(moduleName string, m module.Metric) string {
	base := "topswatch_" + moduleName + "_" + m.Name
	suffix := unitSuffix(m.Unit)
	if suffix == "" || strings.HasSuffix(base, suffix) {
		// No suffix, or the metric name already ends in the unit suffix
		// (e.g. "memory_used_percent"). Avoid double-suffixing like
		// "memory_used_percent_percent".
		return base
	}
	return base + suffix
}

// unitSuffix returns the Prometheus naming convention suffix for a unit
// string, or "" if the unit is unknown / not naming-relevant.
func unitSuffix(unit string) string {
	switch unit {
	case "%":
		return "_percent"
	case "MHz":
		return "_mhz"
	case "W":
		return "_watts"
	case "C":
		return "_celsius"
	case "MB/s":
		return "_mbps"
	case "bytes":
		return "_bytes"
	}
	return ""
}

func promHelp(moduleName string, m module.Metric) string {
	if m.Unit != "" {
		return moduleName + " " + m.Name + " in " + m.Unit
	}
	return moduleName + " " + m.Name
}

func promLabels(m module.Metric) prometheus.Labels {
	if len(m.Labels) == 0 {
		return nil
	}
	return prometheus.Labels(m.Labels)
}
