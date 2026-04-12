package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// raplUncore reads the Intel RAPL "uncore" power domain, which on Intel
// client SoCs is dominated by the integrated GPU. Returns power in watts
// over the interval between samples.
type raplUncore struct {
	energyPath string // .../intel-rapl:N:M/energy_uj
	maxRange   uint64
}

func discoverGPURapl() (*raplUncore, error) {
	// Look for any sub-zone whose name is "uncore" under a top-level
	// intel-rapl:N package zone (skip intel-rapl-mmio variants which
	// duplicate the package counters but typically lack the uncore
	// breakdown).
	zones, err := filepath.Glob("/sys/class/powercap/intel-rapl:*:*")
	if err != nil || len(zones) == 0 {
		return nil, fmt.Errorf("no intel-rapl sub-zones found")
	}
	for _, z := range zones {
		nameBytes, err := os.ReadFile(filepath.Join(z, "name"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(nameBytes)) != "uncore" {
			continue
		}
		energyPath := filepath.Join(z, "energy_uj")
		if _, err := readRaplUint(energyPath); err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", energyPath, err)
		}
		maxRange, _ := readRaplUint(filepath.Join(z, "max_energy_range_uj"))
		return &raplUncore{energyPath: energyPath, maxRange: maxRange}, nil
	}
	return nil, fmt.Errorf("no uncore RAPL zone found")
}

func (r *raplUncore) readEnergyUj() (uint64, error) {
	return readRaplUint(r.energyPath)
}

func readRaplUint(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}
