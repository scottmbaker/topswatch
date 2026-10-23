//go:build !linux

package affinity

import "fmt"

// Pin is Linux-only; the daemon's collectors are too.
func Pin(cpus []int) error {
	return fmt.Errorf("cpu pinning is not supported on this platform")
}
