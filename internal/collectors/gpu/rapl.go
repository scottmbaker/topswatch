package gpu

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/scottmbaker/topswatch/internal/sysfile"
)

// raplUncore reads the Intel RAPL "uncore" power domain, which on Intel
// client SoCs is dominated by the integrated GPU. Returns power in watts
// over the interval between samples.
type raplUncore struct {
	energy   *sysfile.File // .../intel-rapl:N:M/energy_uj, held open
	maxRange uint64
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
		name, err := sysfile.ReadString(filepath.Join(z, "name"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(name) != "uncore" {
			continue
		}
		energyPath := filepath.Join(z, "energy_uj")
		f, err := sysfile.OpenUint(energyPath)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", energyPath, err)
		}
		maxRange, _ := sysfile.ReadUint(filepath.Join(z, "max_energy_range_uj"))
		return &raplUncore{energy: f, maxRange: maxRange}, nil
	}
	return nil, fmt.Errorf("no uncore RAPL zone found")
}

func (r *raplUncore) readEnergyUj() (uint64, error) {
	return r.energy.Uint()
}

func (r *raplUncore) close() {
	if r != nil {
		r.energy.Close() //nolint:errcheck
	}
}
