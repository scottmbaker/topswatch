package gpu

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	drmBasePath    = "/sys/class/drm"
	intelVendorID  = "0x8086"
)

// cardDevice holds paths to a discovered Intel GPU DRM card.
type cardDevice struct {
	cardName  string // e.g. "card0"
	deviceDir string // e.g. /sys/class/drm/card0/device
	pciID     string // e.g. "0xb0a0"
	pciSlot   string // e.g. "0000:00:02.0"
}

// discoverIntelGPU scans /sys/class/drm/card* for an Intel GPU.
func discoverIntelGPU() (*cardDevice, error) {
	entries, err := os.ReadDir(drmBasePath)
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "card") || strings.Contains(name, "-") {
			continue
		}

		deviceDir := filepath.Join(drmBasePath, name, "device")

		// Check vendor
		vendor, err := readTrimmed(filepath.Join(deviceDir, "vendor"))
		if err != nil {
			continue
		}
		if vendor != intelVendorID {
			continue
		}

		// Check it's a GPU (class 0x030000 = VGA compatible controller)
		class, _ := readTrimmed(filepath.Join(deviceDir, "class"))
		if class != "" && !strings.HasPrefix(class, "0x03") {
			continue
		}

		pciID, _ := readTrimmed(filepath.Join(deviceDir, "device"))

		// Get PCI slot from symlink
		pciSlot := ""
		if link, err := os.Readlink(deviceDir); err == nil {
			pciSlot = filepath.Base(link)
		}

		return &cardDevice{
			cardName:  name,
			deviceDir: deviceDir,
			pciID:     pciID,
			pciSlot:   pciSlot,
		}, nil
	}

	return nil, os.ErrNotExist
}

// detectDriver returns the kernel driver name bound to the GPU.
func detectDriver(deviceDir string) string {
	driverLink := filepath.Join(deviceDir, "driver")
	target, err := os.Readlink(driverLink)
	if err != nil {
		return "unknown"
	}
	return filepath.Base(target)
}

func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
