package gpu

import (
	"os"
	"path/filepath"

	"github.com/scottmbaker/topswatch/internal/sysfile"
)

// freqReader reads GT frequency attributes from the Xe driver sysfs.
// Path: /sys/class/drm/card*/device/tile*/gt*/freq0/
// Each attribute is opened on first use and then held (see sysfile).
type freqReader struct {
	freqDir string // path to freq0 directory
	attrs   heldAttrs
}

// heldAttrs caches attribute files by name. A name whose file does not
// exist is remembered as nil so it is not retried every sample.
type heldAttrs map[string]*sysfile.File

func (h heldAttrs) get(dir, attr string) *sysfile.File {
	f, ok := h[attr]
	if !ok {
		f, _ = sysfile.Open(filepath.Join(dir, attr))
		h[attr] = f
	}
	return f
}

func (h heldAttrs) close() {
	for _, f := range h {
		f.Close() //nolint:errcheck
	}
}

// discoverFreqDir finds the GT frequency sysfs directory.
// Xe driver: device/tile0/gt0/freq0/
// i915 fallback: device/gt/gt0/ (has gt_act_freq_mhz etc.)
func discoverFreqDir(deviceDir string) *freqReader {
	// Try Xe path first: tile*/gt*/freq0/
	tileGlob := filepath.Join(deviceDir, "tile*", "gt*", "freq0")
	matches, _ := filepath.Glob(tileGlob)
	if len(matches) > 0 {
		return &freqReader{freqDir: matches[0], attrs: heldAttrs{}}
	}

	// Try alternate Xe path without tile: gt/gt0/freq0
	altGlob := filepath.Join(deviceDir, "gt", "gt*", "freq0")
	matches, _ = filepath.Glob(altGlob)
	if len(matches) > 0 {
		return &freqReader{freqDir: matches[0], attrs: heldAttrs{}}
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
				return &freqReader{freqDir: freqDir, attrs: heldAttrs{}}
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
	return readHeldFloat(f.attrs.get(f.freqDir, attr))
}

func (f *freqReader) close() {
	if f != nil {
		f.attrs.close()
	}
}

func readHeldFloat(f *sysfile.File) (float64, bool) {
	if f == nil {
		return 0, false
	}
	v, err := f.Float()
	if err != nil {
		return 0, false
	}
	return v, true
}

// i915FreqReader reads frequency from the i915 driver's sysfs attributes.
// Path: /sys/class/drm/card*/gt_act_freq_mhz etc.
type i915FreqReader struct {
	cardDir string // e.g. /sys/class/drm/card0
	attrs   heldAttrs
}

func discoverI915Freq(cardName string) *i915FreqReader {
	cardDir := filepath.Join(drmBasePath, cardName)
	// Check if i915 frequency files exist
	if _, err := os.Stat(filepath.Join(cardDir, "gt_act_freq_mhz")); err == nil {
		return &i915FreqReader{cardDir: cardDir, attrs: heldAttrs{}}
	}
	return nil
}

func (f *i915FreqReader) readFreqMHz(attr string) (float64, bool) {
	return readHeldFloat(f.attrs.get(f.cardDir, attr))
}

func (f *i915FreqReader) close() {
	if f != nil {
		f.attrs.close()
	}
}
