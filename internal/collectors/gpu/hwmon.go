package gpu

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/scottmbaker/topswatch/internal/sysfile"
)

// hwmonReader reads GPU hardware monitoring data from the Linux hwmon subsystem.
type hwmonReader struct {
	hwmonDir string // e.g. /sys/class/hwmon/hwmon3
	attrs    heldAttrs
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
				attrs:    heldAttrs{},
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

// readLabel reads a hwmon label file. Labels are static, so this is a
// one-shot read.
func (h *hwmonReader) readLabel(attr string) string {
	s, err := sysfile.ReadString(filepath.Join(h.hwmonDir, attr))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// readInt reads a held hwmon attribute as an int64.
func (h *hwmonReader) readInt(attr string) (int64, bool) {
	f := h.attrs.get(h.hwmonDir, attr)
	if f == nil {
		return 0, false
	}
	v, err := f.Int()
	if err != nil {
		return 0, false
	}
	return v, true
}

func (h *hwmonReader) close() {
	if h != nil {
		h.attrs.close()
	}
}
