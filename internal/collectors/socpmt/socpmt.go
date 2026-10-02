// Package socpmt reads the SoC-level Intel PMT telemetry aggregator: the
// region that carries the package, core, GT (integrated GPU) and NPU
// energy counters. It exists for one reason today: on some boards the
// kernel does not register the RAPL "uncore" zone, so GPU power has no
// sysfs source, while the GT voltage-regulator energy in this region is
// documented by Intel as "the same as SECONDARY_PLANE_ENERGY_STATUS" and
// was measured identical to that MSR. Reading it needs only the telem
// file the NPU collector already uses.
//
// Field offsets come from Intel's published definitions
// (github.com/intel/Intel-PMT, xml/<gen>/0/*_aggregator.xml), keyed by
// the region's GUID so a board with an unknown layout is simply
// unsupported rather than misread.
package socpmt

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const basePath = "/sys/class/intel_pmt"

// layout is what we know about one aggregator GUID.
type layout struct {
	gen      string
	gtEnergy int // byte offset of VCCGT_ENERGY (64-bit, joules in U32.18.14)
}

// layouts: VCCGT_ENERGY offsets per aggregator GUID.
//
//	MTL 0x130670b2: 0x620   LNL 0x3072005: 0x5c8   PTL 0x3086000: 0x668
var layouts = map[string]layout{
	"0x130670b2": {gen: "Meteor Lake", gtEnergy: 0x620},
	"0x3072005":  {gen: "Lunar Lake", gtEnergy: 0x5c8},
	"0x3086000":  {gen: "Panther Lake", gtEnergy: 0x668},
}

// Device is an open SoC aggregator region.
type Device struct {
	path   string
	guid   string
	layout layout
}

// Open finds the SoC aggregator among the PMT regions. It returns an
// error when none has a known layout.
func Open() (*Device, error) {
	return openUnder(basePath)
}

func openUnder(base string) (*Device, error) {
	dirs, _ := filepath.Glob(filepath.Join(base, "telem*"))
	for _, d := range dirs {
		g, err := os.ReadFile(filepath.Join(d, "guid"))
		if err != nil {
			continue
		}
		guid := strings.ToLower(strings.TrimSpace(string(g)))
		l, ok := layouts[guid]
		if !ok {
			continue
		}
		dev := &Device{path: filepath.Join(d, "telem"), guid: guid, layout: l}
		if _, err := dev.GTEnergyJoules(); err != nil {
			return nil, fmt.Errorf("%s (%s): %w", d, l.gen, err)
		}
		return dev, nil
	}
	return nil, fmt.Errorf("no SoC PMT aggregator with a known layout under %s", base)
}

// Generation names the SoC family the layout belongs to.
func (d *Device) Generation() string { return d.layout.gen }

// GTEnergyJoules returns the cumulative GT (integrated GPU) voltage
// regulator energy in joules. The counter is 64 bits in U32.18.14
// fixed point and does not wrap in practice.
func (d *Device) GTEnergyJoules() (float64, error) {
	raw, err := d.readU64(d.layout.gtEnergy)
	if err != nil {
		return 0, err
	}
	return float64(raw) / 16384.0, nil
}

func (d *Device) readU64(offset int) (uint64, error) {
	f, err := os.Open(d.path)
	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck
	var b [8]byte
	if _, err := f.ReadAt(b[:], int64(offset)); err != nil {
		return 0, fmt.Errorf("read %s @0x%x: %w", d.path, offset, err)
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}
