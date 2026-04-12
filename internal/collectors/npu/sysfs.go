package npu

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// readSysfsString reads a sysfs file and returns its trimmed contents.
func readSysfsString(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// readSysfsUint64 reads a sysfs file containing a decimal integer.
func readSysfsUint64(path string) (uint64, error) {
	s, err := readSysfsString(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(s, 10, 64)
}

// readBusyTimeUs reads the NPU busy time counter in microseconds.
func readBusyTimeUs(sysfsDir string) (uint64, error) {
	return readSysfsUint64(filepath.Join(sysfsDir, "npu_busy_time_us"))
}

// readMemoryUtilization reads the NPU memory usage (PTL+ only).
// Returns the raw string value. Returns empty string if not available.
func readMemoryUtilization(sysfsDir string) (string, error) {
	path := filepath.Join(sysfsDir, "npu_memory_utilization")
	s, err := readSysfsString(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return s, nil
}

// readFirmwareVersion reads the firmware version from debugfs and returns
// it pretty-printed. Walks /sys/kernel/debug/accel/*/ directories looking
// for one whose "device" symlink resolves to the given PCI slot. Returns
// "unknown" if debugfs isn't mounted, isn't readable (typical without
// CAP_SYS_ADMIN), or no fw_version file is found.
func readFirmwareVersion(pciSlot string) string {
	accelDirs, err := filepath.Glob("/sys/kernel/debug/accel/*")
	if err != nil {
		return "unknown"
	}

	// Preferred path: match the entry whose device symlink points at our PCI slot.
	for _, dir := range accelDirs {
		target, err := os.Readlink(filepath.Join(dir, "device"))
		if err != nil || !strings.Contains(target, pciSlot) {
			continue
		}
		if v, err := readSysfsString(filepath.Join(dir, "fw_version")); err == nil {
			return formatFirmwareVersion(v)
		}
	}

	// Fallback: any fw_version under /sys/kernel/debug/accel/*. Useful when
	// the debug device symlink isn't readable for some reason but the
	// fw_version file is.
	for _, dir := range accelDirs {
		if v, err := readSysfsString(filepath.Join(dir, "fw_version")); err == nil {
			return formatFirmwareVersion(v)
		}
	}

	return "unknown"
}

// formatFirmwareVersion converts the raw asterisk-delimited fw_version string
// (date*chip*ci_tag*githash) into a compact display form like
// "NPU50xx g72f907f (Nov 13 2025)".
func formatFirmwareVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	parts := strings.Split(raw, "*")
	if len(parts) < 2 {
		return raw
	}
	date := strings.TrimSpace(parts[0])
	chip := strings.TrimSpace(parts[1])
	short := ""
	if len(parts) >= 4 {
		h := strings.TrimSpace(parts[3])
		if len(h) >= 7 {
			short = "g" + h[:7]
		}
	}
	out := chip
	if short != "" {
		out += " " + short
	}
	if date != "" {
		out += " (" + date + ")"
	}
	return out
}

// readMemTotalBytes returns the system MemTotal from /proc/meminfo, in
// bytes. Used as the denominator for NPU memory utilization on integrated
// NPUs (Panther Lake and earlier) where the NPU has no dedicated memory
// of its own and shares system RAM. Returns 0 if /proc/meminfo cannot
// be read or parsed.
func readMemTotalBytes() uint64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kib, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		// /proc/meminfo reports in kiB.
		return kib * 1024
	}
	return 0
}

// readDriverVersion gets the intel_vpu driver version via modinfo.
func readDriverVersion() string {
	out, err := exec.Command("modinfo", "-F", "version", "intel_vpu").Output()
	if err != nil {
		return "unknown"
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "unknown"
	}
	return v
}

// parseMemoryBytes parses the memory utilization string into bytes.
// The format varies by kernel version; try common formats.
func parseMemoryBytes(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}

	// Try plain number (bytes)
	if v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64); err == nil {
		return v, true
	}

	// Try "123 kB" or "123 MB" format
	parts := strings.Fields(s)
	if len(parts) == 2 {
		v, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			return 0, false
		}
		switch strings.ToUpper(parts[1]) {
		case "KB", "KIB":
			return v * 1024, true
		case "MB", "MIB":
			return v * 1024 * 1024, true
		case "GB", "GIB":
			return v * 1024 * 1024 * 1024, true
		}
	}

	return 0, false
}
