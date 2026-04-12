package module

// Metric represents a single measured value from a collector.
type Metric struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Unit   string            `json:"unit"`
	Labels map[string]string `json:"labels,omitempty"`
}

// DeviceInfo holds static identification for a device.
type DeviceInfo struct {
	Name            string `json:"name"`
	PCIDevice       string `json:"pci_device,omitempty"`
	DriverVersion   string `json:"driver_version,omitempty"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	Extra           map[string]string `json:"extra,omitempty"`
}

// Module is the interface that all collectors implement.
type Module interface {
	// Name returns the module identifier (e.g. "npu", "cpu").
	Name() string

	// Init performs one-time setup (discover devices, map regions, etc.).
	Init() error

	// DeviceInfo returns static device information.
	DeviceInfo() DeviceInfo

	// Collect reads current metrics. Called on every poll interval.
	Collect() ([]Metric, error)

	// Close releases any resources.
	Close() error
}
