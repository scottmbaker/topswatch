package gpu

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// freqReader reads GT frequency attributes from the Xe driver sysfs.
// Path: /sys/class/drm/card*/device/tile*/gt*/freq0/
type freqReader struct {
	freqDir string // path to freq0 directory
}

// discoverFreqDir finds the GT frequency sysfs directory.
// Xe driver: device/tile0/gt0/freq0/
// i915 fallback: device/gt/gt0/ (has gt_act_freq_mhz etc.)
func discoverFreqDir(deviceDir string) *freqReader {
	// Try Xe path first: tile*/gt*/freq0/
	tileGlob := filepath.Join(deviceDir, "tile*", "gt*", "freq0")
	matches, _ := filepath.Glob(tileGlob)
	if len(matches) > 0 {
		return &freqReader{freqDir: matches[0]}
	}

	// Try alternate Xe path without tile: gt/gt0/freq0
	altGlob := filepath.Join(deviceDir, "gt", "gt*", "freq0")
	matches, _ = filepath.Glob(altGlob)
	if len(matches) > 0 {
		return &freqReader{freqDir: matches[0]}
	}

	return nil
}

// readFreqMHz reads a frequency attribute file and returns the value in MHz.
// The Xe driver writes plain integers (MHz) to these files.
func (f *freqReader) readFreqMHz(attr string) (float64, bool) {
	path := filepath.Join(f.freqDir, attr)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// i915FreqReader reads frequency from the i915 driver's sysfs attributes.
// Path: /sys/class/drm/card*/gt_act_freq_mhz etc.
type i915FreqReader struct {
	cardDir string // e.g. /sys/class/drm/card0
}

func discoverI915Freq(cardName string) *i915FreqReader {
	cardDir := filepath.Join(drmBasePath, cardName)
	// Check if i915 frequency files exist
	if _, err := os.Stat(filepath.Join(cardDir, "gt_act_freq_mhz")); err == nil {
		return &i915FreqReader{cardDir: cardDir}
	}
	return nil
}

func (f *i915FreqReader) readFreqMHz(attr string) (float64, bool) {
	path := filepath.Join(f.cardDir, attr)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
