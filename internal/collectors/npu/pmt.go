package npu

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const pmtBasePath = "/sys/class/intel_pmt"

// pmtDevice represents a discovered PMT telemetry device.
type pmtDevice struct {
	path     string // e.g. /sys/class/intel_pmt/telem3
	telemBuf []byte // raw telemetry buffer contents
}

// discoverPMT scans /sys/class/intel_pmt/telem* for a telemetry device
// whose GUID matches the expected NPU telemetry GUID for the given CPU generation.
func discoverPMT(gen CPUGen) (*pmtDevice, error) {
	guids, ok := pmtGUIDs[gen]
	if !ok {
		return nil, fmt.Errorf("no PMT GUIDs known for %s", gen)
	}

	guidSet := map[string]bool{}
	for _, g := range guids {
		guidSet[g] = true
	}

	entries, err := os.ReadDir(pmtBasePath)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", pmtBasePath, err)
	}

	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "telem") {
			continue
		}

		telemDir := filepath.Join(pmtBasePath, e.Name())
		guidBytes, err := os.ReadFile(filepath.Join(telemDir, "guid"))
		if err != nil {
			continue
		}

		guid := strings.TrimSpace(string(guidBytes))
		if guidSet[guid] {
			return &pmtDevice{path: telemDir}, nil
		}
	}

	return nil, fmt.Errorf("no PMT telemetry device found for %s (tried GUIDs: %v)", gen, guids)
}

// readBuffer reads the raw telemetry buffer from the PMT device.
func (p *pmtDevice) readBuffer() error {
	data, err := os.ReadFile(filepath.Join(p.path, "telem"))
	if err != nil {
		return fmt.Errorf("cannot read PMT telem buffer: %w", err)
	}
	p.telemBuf = data
	return nil
}

// readU32 reads a 32-bit value at the given byte offset.
func (p *pmtDevice) readU32(offset int) (uint32, error) {
	if offset+4 > len(p.telemBuf) {
		return 0, fmt.Errorf("offset 0x%x out of range (buffer size %d)", offset, len(p.telemBuf))
	}
	return binary.LittleEndian.Uint32(p.telemBuf[offset : offset+4]), nil
}

// readU64 reads a 64-bit value at the given byte offset.
func (p *pmtDevice) readU64(offset int) (uint64, error) {
	if offset+8 > len(p.telemBuf) {
		return 0, fmt.Errorf("offset 0x%x out of range (buffer size %d)", offset, len(p.telemBuf))
	}
	return binary.LittleEndian.Uint64(p.telemBuf[offset : offset+8]), nil
}

// extractBits reads the register value and extracts the specified bit range.
func (p *pmtDevice) extractBits(reg registerDef) (uint64, error) {
	var raw uint64
	var err error

	if reg.size == 8 {
		raw, err = p.readU64(reg.offset)
	} else {
		var v32 uint32
		v32, err = p.readU32(reg.offset)
		raw = uint64(v32)
	}
	if err != nil {
		return 0, err
	}

	// Extract bit range [bitLo:bitHi]
	mask := (uint64(1) << (uint(reg.bitHi-reg.bitLo) + 1)) - 1
	return (raw >> uint(reg.bitLo)) & mask, nil
}
