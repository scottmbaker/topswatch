//go:build linux

package gpu

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// --- Common types ---

// engineCounter is a pair of fds for one engine.
//
// On xe each engine has two counters: an "active ticks" counter and a
// "total ticks" counter. The busy fraction is dAct/dTot, which is
// inherently normalized.
//
// On i915 each engine has a single "busy nanoseconds" counter. The
// busy fraction is dAct / dWall_ns, computed against host wall time.
type engineCounter struct {
	label    string
	activeFd int
	totalFd  int // -1 on i915

	prevAct   uint64
	prevTot   uint64    // unused on i915
	prevWall  time.Time // unused on xe
	prevValid bool
}

type perfReader struct {
	driver   string // "xe" or "i915"
	cpu      int
	counters []*engineCounter
}

// --- Entry point ---

func discoverPerfReader(driver, pciSlot string) (*perfReader, error) {
	pmuDir, err := findPMUDir(driver, pciSlot)
	if err != nil {
		return nil, err
	}

	typeStr, err := readTrimmed(filepath.Join(pmuDir, "type"))
	if err != nil {
		return nil, fmt.Errorf("read pmu type: %w", err)
	}
	pmuType64, err := strconv.ParseUint(typeStr, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("parse pmu type %q: %w", typeStr, err)
	}
	pmuType := uint32(pmuType64)

	cpu := 0
	if maskStr, err := readTrimmed(filepath.Join(pmuDir, "cpumask")); err == nil && maskStr != "" {
		cpu = firstCPUInMask(maskStr)
	}

	format, err := readPMUFormat(filepath.Join(pmuDir, "format"))
	if err != nil {
		return nil, fmt.Errorf("read PMU format dir: %w", err)
	}

	r := &perfReader{driver: driver, cpu: cpu}

	if driver == "xe" {
		if err := r.initXe(pmuDir, pmuType, format); err != nil {
			r.Close()
			return nil, err
		}
	} else {
		if err := r.initI915(pmuDir, pmuType, format); err != nil {
			r.Close()
			return nil, err
		}
	}

	if len(r.counters) == 0 {
		return nil, fmt.Errorf("no usable engine counters under %s", pmuDir)
	}
	return r, nil
}

// --- Format / event-file parsing (shared) ---

type bitRange struct{ lo, hi uint }
type pmuFormat map[string]bitRange

// readPMUFormat parses every file under <pmu>/format/ as "config:LO[-HI]".
func readPMUFormat(formatDir string) (pmuFormat, error) {
	entries, err := os.ReadDir(formatDir)
	if err != nil {
		return nil, err
	}
	out := pmuFormat{}
	for _, e := range entries {
		raw, err := readTrimmed(filepath.Join(formatDir, e.Name()))
		if err != nil {
			continue
		}
		br, err := parseConfigBitRange(raw)
		if err != nil {
			continue
		}
		out[e.Name()] = br
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no parseable format files in %s", formatDir)
	}
	return out, nil
}

// parseConfigBitRange parses a PMU format file value like "config:20-27" or
// "config:5". Higher-order config words (config1, config2) are not used here.
func parseConfigBitRange(s string) (bitRange, error) {
	const prefix = "config:"
	if !strings.HasPrefix(s, prefix) {
		return bitRange{}, fmt.Errorf("expected %q prefix, got %q", prefix, s)
	}
	rest := s[len(prefix):]
	var lo, hi uint64
	if i := strings.Index(rest, "-"); i >= 0 {
		var err error
		lo, err = strconv.ParseUint(rest[:i], 10, 32)
		if err != nil {
			return bitRange{}, err
		}
		hi, err = strconv.ParseUint(rest[i+1:], 10, 32)
		if err != nil {
			return bitRange{}, err
		}
	} else {
		n, err := strconv.ParseUint(rest, 10, 32)
		if err != nil {
			return bitRange{}, err
		}
		lo, hi = n, n
	}
	return bitRange{lo: uint(lo), hi: uint(hi)}, nil
}

// parseEventFile reads a "k1=v1,k2=v2" event file and packs each value into
// the corresponding bit range from the PMU format dir. The special key
// "config" is taken as the entire 64-bit config word.
func parseEventFile(path string, format pmuFormat) (uint64, error) {
	raw, err := readTrimmed(path)
	if err != nil {
		return 0, err
	}
	var cfg uint64
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		v = strings.TrimPrefix(v, "0x")
		n, err := strconv.ParseUint(v, 16, 64)
		if err != nil {
			return 0, fmt.Errorf("%s: parse %s=%s: %w", path, k, v, err)
		}
		if k == "config" {
			cfg |= n
			continue
		}
		br, ok := format[k]
		if !ok {
			return 0, fmt.Errorf("%s: unknown PMU format field %q", path, k)
		}
		cfg |= setBits(n, br)
	}
	return cfg, nil
}

func setBits(value uint64, br bitRange) uint64 {
	width := br.hi - br.lo + 1
	mask := (uint64(1) << width) - 1
	return (value & mask) << br.lo
}

// --- xe driver ---

// xeEngineClassID maps engine class names to drm_xe_engine_class enum values.
var xeEngineClassID = map[string]uint64{
	"rcs":  0, // RENDER
	"bcs":  1, // COPY
	"vcs":  2, // VIDEO_DECODE
	"vecs": 3, // VIDEO_ENHANCE
	"ccs":  4, // COMPUTE
}

func (r *perfReader) initXe(pmuDir string, pmuType uint32, format pmuFormat) error {
	for _, f := range []string{"engine_class", "engine_instance", "gt"} {
		if _, ok := format[f]; !ok {
			return fmt.Errorf("xe PMU missing format field %q", f)
		}
	}
	classBR := format["engine_class"]
	instBR := format["engine_instance"]
	gtBR := format["gt"]

	activeBase, err := parseEventFile(filepath.Join(pmuDir, "events", "engine-active-ticks"), format)
	if err != nil {
		return fmt.Errorf("engine-active-ticks: %w", err)
	}
	totalBase, err := parseEventFile(filepath.Join(pmuDir, "events", "engine-total-ticks"), format)
	if err != nil {
		return fmt.Errorf("engine-total-ticks: %w", err)
	}

	type engineSlot struct {
		label    string
		classID  uint64
		instance uint64
		gtID     uint64
	}
	var slots []engineSlot
	gtDirs, _ := filepath.Glob("/sys/class/drm/card*/device/tile*/gt*/engines")
	for _, engDir := range gtDirs {
		gtName := filepath.Base(filepath.Dir(engDir))
		var gtID uint64
		if strings.HasPrefix(gtName, "gt") {
			if n, err := strconv.ParseUint(gtName[2:], 10, 64); err == nil {
				gtID = n
			}
		}
		entries, err := os.ReadDir(engDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			classID, ok := xeEngineClassID[e.Name()]
			if !ok {
				continue
			}
			slots = append(slots, engineSlot{
				label:   e.Name() + "0",
				classID: classID,
				gtID:    gtID,
			})
		}
	}
	if len(slots) == 0 {
		return fmt.Errorf("no engines found under /sys/class/drm/card*/device/tile*/gt*/engines/")
	}

	attrSize := uint32(unsafe.Sizeof(unix.PerfEventAttr{}))
	for _, sl := range slots {
		extra := setBits(sl.classID, classBR) | setBits(sl.instance, instBR) | setBits(sl.gtID, gtBR)
		actCfg := activeBase | extra
		totCfg := totalBase | extra

		actAttr := unix.PerfEventAttr{Type: pmuType, Size: attrSize, Config: actCfg}
		actFd, err := unix.PerfEventOpen(&actAttr, -1, r.cpu, -1, unix.PERF_FLAG_FD_CLOEXEC)
		if err != nil {
			continue // engine not present on this SKU
		}
		totAttr := unix.PerfEventAttr{Type: pmuType, Size: attrSize, Config: totCfg}
		totFd, err := unix.PerfEventOpen(&totAttr, -1, r.cpu, -1, unix.PERF_FLAG_FD_CLOEXEC)
		if err != nil {
			_ = unix.Close(actFd)
			continue
		}
		r.counters = append(r.counters, &engineCounter{
			label:    sl.label,
			activeFd: actFd,
			totalFd:  totFd,
		})
	}
	return nil
}

// --- i915 driver ---

// On i915 the kernel pre-bakes one event file per engine (e.g. "rcs0-busy",
// "bcs0-busy"), so discovery is just enumerating the events directory.
// The value returned by perf_read on these is busy nanoseconds, which we
// compare against host wall time to compute a fraction.
func (r *perfReader) initI915(pmuDir string, pmuType uint32, format pmuFormat) error {
	entries, err := os.ReadDir(filepath.Join(pmuDir, "events"))
	if err != nil {
		return err
	}
	attrSize := uint32(unsafe.Sizeof(unix.PerfEventAttr{}))
	for _, e := range entries {
		name := e.Name()
		// Skip metadata sidecar files (foo.scale, foo.unit) and any
		// non-busy events (frequency, c-states, interrupts, etc.).
		if strings.Contains(name, ".") || !strings.HasSuffix(name, "-busy") {
			continue
		}
		cfg, err := parseEventFile(filepath.Join(pmuDir, "events", name), format)
		if err != nil {
			continue
		}
		attr := unix.PerfEventAttr{Type: pmuType, Size: attrSize, Config: cfg}
		fd, err := unix.PerfEventOpen(&attr, -1, r.cpu, -1, unix.PERF_FLAG_FD_CLOEXEC)
		if err != nil {
			continue
		}
		r.counters = append(r.counters, &engineCounter{
			label:    strings.TrimSuffix(name, "-busy"),
			activeFd: fd,
			totalFd:  -1,
		})
	}
	return nil
}

// --- PMU directory discovery ---

func findPMUDir(driver, pciSlot string) (string, error) {
	if driver != "xe" {
		p := "/sys/bus/event_source/devices/i915"
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("no i915 PMU at %s", p)
	}
	candidate := "/sys/bus/event_source/devices/xe_" + slotToPMUSuffix(pciSlot)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	matches, _ := filepath.Glob("/sys/bus/event_source/devices/xe_*")
	if len(matches) == 0 {
		return "", fmt.Errorf("no xe PMU under /sys/bus/event_source/devices/")
	}
	return matches[0], nil
}

func slotToPMUSuffix(slot string) string {
	return strings.ReplaceAll(slot, ":", "_")
}

func firstCPUInMask(mask string) int {
	for i := 0; i < len(mask); i++ {
		c := mask[i]
		if c >= '0' && c <= '9' {
			j := i
			for j < len(mask) && mask[j] >= '0' && mask[j] <= '9' {
				j++
			}
			n, _ := strconv.Atoi(mask[i:j])
			return n
		}
	}
	return 0
}

// --- Counter reading ---

// readEngineBusy returns per-engine busy fraction (0..1) since the previous
// call. The first call after open primes baselines and returns an empty map.
func (r *perfReader) readEngineBusy() map[string]float64 {
	out := map[string]float64{}
	var buf [8]byte
	now := time.Now()

	for _, ec := range r.counters {
		actRaw, ok := readPerfUint64(ec.activeFd, buf[:])
		if !ok {
			continue
		}

		var frac float64
		if r.driver == "xe" {
			totRaw, ok := readPerfUint64(ec.totalFd, buf[:])
			if !ok {
				continue
			}
			if ec.prevValid {
				dAct := actRaw - ec.prevAct
				dTot := totRaw - ec.prevTot
				if dTot > 0 {
					frac = float64(dAct) / float64(dTot)
				}
			}
			ec.prevAct = actRaw
			ec.prevTot = totRaw
		} else {
			// i915: dBusy_ns / dWall_ns
			if ec.prevValid {
				var dAct uint64
				if actRaw >= ec.prevAct {
					dAct = actRaw - ec.prevAct
				}
				dWallNs := uint64(now.Sub(ec.prevWall).Nanoseconds())
				if dWallNs > 0 {
					frac = float64(dAct) / float64(dWallNs)
				}
			}
			ec.prevAct = actRaw
			ec.prevWall = now
		}

		// Only emit a value once we have a valid prior sample.
		if ec.prevValid {
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			out[ec.label] = frac
		}
		ec.prevValid = true
	}
	return out
}

func readPerfUint64(fd int, buf []byte) (uint64, bool) {
	if fd <= 0 {
		return 0, false
	}
	n, err := unix.Read(fd, buf)
	if err != nil || n != 8 {
		return 0, false
	}
	return binary.LittleEndian.Uint64(buf), true
}

func (r *perfReader) Close() {
	if r == nil {
		return
	}
	for _, ec := range r.counters {
		if ec.activeFd > 0 {
			_ = unix.Close(ec.activeFd)
			ec.activeFd = 0
		}
		if ec.totalFd > 0 {
			_ = unix.Close(ec.totalFd)
			ec.totalFd = 0
		}
	}
	r.counters = nil
}
