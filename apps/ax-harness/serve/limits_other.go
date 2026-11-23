//go:build !linux && !windows

package serve

import (
	"fmt"
	"syscall"
)

// limitRunner sets each limit as both soft and hard. This system has no
// per-process thread cap and does not enforce RLIMIT_AS.
func limitRunner(l RunnerLimits) error {
	for _, r := range []struct {
		name     string
		resource int
		value    uint64
	}{
		{"CPU time", syscall.RLIMIT_CPU, l.CPUSeconds},
		{"file size", syscall.RLIMIT_FSIZE, l.FileSize},
		{"open files", syscall.RLIMIT_NOFILE, l.OpenFiles},
	} {
		var now syscall.Rlimit
		if err := syscall.Getrlimit(r.resource, &now); err != nil {
			return fmt.Errorf("limit %s: %w", r.name, err)
		}
		value := min(r.value, now.Max)
		if err := syscall.Setrlimit(r.resource, &syscall.Rlimit{Cur: value, Max: value}); err != nil {
			return fmt.Errorf("limit %s: %w", r.name, err)
		}
	}
	return nil
}
