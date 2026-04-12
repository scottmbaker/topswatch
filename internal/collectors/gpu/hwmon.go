package gpu

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// hwmonReader reads GPU hardware monitoring data from the Linux hwmon subsystem.
type hwmonReader struct {
	hwmonDir string // e.g. /sys/class/hwmon/hwmon3
}

// discoverHwmon finds the hwmon device associated with the GPU's PCI device.
func discoverHwmon(deviceDir string) *hwmonReader {
	hwmonParent := filepath.Join(deviceDir, "hwmon")
	entries, err := os.ReadDir(hwmonParent)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "hwmon") {
			return &hwmonReader{
				hwmonDir: filepath.Join(hwmonParent, e.Name()),
			}
		}
	}

	return nil
}

// readTemperatureC reads a temperature sensor and returns degrees Celsius.
// hwmon temperature files contain millidegrees C.
func (h *hwmonReader) readTemperatureC(attr string) (float64, bool) {
	raw, ok := h.readInt(attr)
	if !ok {
		return 0, false
	}
	return float64(raw) / 1000.0, true
}

// readLabel reads a hwmon label file.
func (h *hwmonReader) readLabel(attr string) string {
	path := filepath.Join(h.hwmonDir, attr)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readInt reads a hwmon attribute as an int64.
func (h *hwmonReader) readInt(attr string) (int64, bool) {
	path := filepath.Join(h.hwmonDir, attr)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

