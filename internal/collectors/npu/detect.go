package npu

import (
	"os"
	"path/filepath"
	"strings"
)

type CPUGen int

const (
	GenUnknown CPUGen = iota
	GenMeteorLake
	GenArrowLake
	GenLunarLake
	GenPantherLake
)

func (g CPUGen) String() string {
	switch g {
	case GenMeteorLake:
		return "Meteor Lake"
	case GenArrowLake:
		return "Arrow Lake"
	case GenLunarLake:
		return "Lunar Lake"
	case GenPantherLake:
		return "Panther Lake"
	default:
		return "Unknown"
	}
}

func (g CPUGen) ShortName() string {
	switch g {
	case GenMeteorLake:
		return "mtl"
	case GenArrowLake:
		return "arl"
	case GenLunarLake:
		return "lnl"
	case GenPantherLake:
		return "ptl"
	default:
		return "unknown"
	}
}

// pciIDToGen maps NPU PCI device IDs to CPU generation.
var pciIDToGen = map[string]CPUGen{
	"0x7d1d": GenMeteorLake,
	"0xad1d": GenArrowLake,
	"0x643e": GenLunarLake,
	"0xb03e": GenPantherLake,
}

// discoverNPUDevice finds the NPU PCI device path under the intel_vpu driver.
// Returns the PCI slot (e.g. "0000:00:0b.0") and the sysfs directory path.
func discoverNPUDevice(driverPath string) (slot string, sysfsDir string, err error) {
	entries, err := os.ReadDir(driverPath)
	if err != nil {
		return "", "", err
	}

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "0000:") {
			return name, filepath.Join(driverPath, name), nil
		}
	}

	return "", "", os.ErrNotExist
}

// detectGeneration reads the PCI device ID and returns the CPU generation.
func detectGeneration(sysfsDir string) (CPUGen, string, error) {
	data, err := os.ReadFile(filepath.Join(sysfsDir, "device"))
	if err != nil {
		return GenUnknown, "", err
	}

	pciID := strings.TrimSpace(string(data))
	gen, ok := pciIDToGen[pciID]
	if !ok {
		return GenUnknown, pciID, nil
	}
	return gen, pciID, nil
}
