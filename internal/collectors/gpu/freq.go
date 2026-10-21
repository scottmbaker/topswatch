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

// discoverMediaFreqDir finds the frequency directory of the media GT.
// Since Meteor Lake the video engines (vcs, vecs) sit on their own GT
// with its own clock domain, exposed by xe as a second gt*/freq0/. A GT
// is recognised as the media one by its engines/ directory holding only
// video engine classes; a GPU with a single GT (or a kernel without the
// engines directory) reports none.
func discoverMediaFreqDir(deviceDir string) *freqReader {
	for _, pattern := range []string{
		filepath.Join(deviceDir, "tile*", "gt*"),
		filepath.Join(deviceDir, "gt", "gt*"),
	} {
		gts, _ := filepath.Glob(pattern)
		for _, gt := range gts {
			if !isMediaGT(gt) {
				continue
			}
			freqDir := filepath.Join(gt, "freq0")
			if _, err := os.Stat(filepath.Join(freqDir, "act_freq")); err == nil {
				return &freqReader{freqDir: freqDir}
			}
		}
	}
	return nil
}

// isMediaGT reports whether a gt*/ directory holds video engines and no
// render, compute or copy engine.
func isMediaGT(gtDir string) bool {
	entries, err := os.ReadDir(filepath.Join(gtDir, "engines"))
	if err != nil {
		return false
	}
	video := false
	for _, e := range entries {
		switch e.Name() {
		case "vcs", "vecs":
			video = true
		case "rcs", "ccs", "bcs":
			return false
		}
	}
	return video
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
