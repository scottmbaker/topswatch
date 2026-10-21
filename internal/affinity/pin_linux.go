package affinity

import (
	"fmt"
	"os"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

// Pin confines every thread of this process to cpus and caps the Go
// scheduler at that many threads. Threads the runtime creates later
// inherit the mask from the thread that creates them, so pinning what
// exists now covers the future; two passes close the window in which a
// thread appears between listing and pinning.
func Pin(cpus []int) error {
	if len(cpus) == 0 {
		return fmt.Errorf("no cpus to pin to")
	}
	var set unix.CPUSet
	for _, c := range cpus {
		set.Set(c)
	}
	if err := unix.SchedSetaffinity(0, &set); err != nil {
		return fmt.Errorf("sched_setaffinity: %w", err)
	}
	for pass := 0; pass < 2; pass++ {
		entries, err := os.ReadDir("/proc/self/task")
		if err != nil {
			return err
		}
		for _, e := range entries {
			tid, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			// A thread that exited between the listing and the call is
			// not an error worth failing over.
			_ = unix.SchedSetaffinity(tid, &set)
		}
	}
	runtime.GOMAXPROCS(len(cpus))
	return nil
}
