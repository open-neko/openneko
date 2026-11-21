package serve

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// limitRunner sets each limit as both soft and hard. RLIMIT_NPROC counts
// every task of the account, so the cap is the count now plus
// l.AccountTasks: room for this runner and the runners beside it, each held
// to l.Threads by limitThreads.
func limitRunner(l RunnerLimits) error {
	tasks, err := accountTasks(os.Getuid())
	if err != nil {
		return fmt.Errorf("count the tasks of this account: %w", err)
	}
	for _, r := range []struct {
		name     string
		resource int
		value    uint64
	}{
		{"CPU time", unix.RLIMIT_CPU, l.CPUSeconds},
		{"address space", unix.RLIMIT_AS, l.AddressSpace},
		{"file size", unix.RLIMIT_FSIZE, l.FileSize},
		{"open files", unix.RLIMIT_NOFILE, l.OpenFiles},
		{"threads", unix.RLIMIT_NPROC, tasks + l.AccountTasks},
	} {
		if err := setHardLimit(r.resource, r.value); err != nil {
			return fmt.Errorf("limit %s: %w", r.name, err)
		}
	}
	return nil
}

func setHardLimit(resource int, value uint64) error {
	var now unix.Rlimit
	if err := unix.Getrlimit(resource, &now); err != nil {
		return err
	}
	value = min(value, now.Max)
	return unix.Setrlimit(resource, &unix.Rlimit{Cur: value, Max: value})
}

// accountTasks counts the threads of every process whose real user is uid,
// as the kernel does for RLIMIT_NPROC.
func accountTasks(uid int) (uint64, error) {
	dirs, err := filepath.Glob("/proc/[0-9]*/status")
	if err != nil {
		return 0, err
	}
	var total uint64
	want := []byte(strconv.Itoa(uid))
	for _, p := range dirs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var real []byte
		var threads uint64
		for line := range bytes.Lines(b) {
			if v, ok := bytes.CutPrefix(line, []byte("Uid:")); ok {
				if f := bytes.Fields(v); len(f) > 0 {
					real = f[0]
				}
			}
			if v, ok := bytes.CutPrefix(line, []byte("Threads:")); ok {
				threads, _ = strconv.ParseUint(string(bytes.TrimSpace(v)), 10, 64)
			}
		}
		if bytes.Equal(real, want) {
			total += threads
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("no task of user %d in /proc", uid)
	}
	return total, nil
}
