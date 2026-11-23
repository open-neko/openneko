package serve

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// limitRunner sets each limit as both soft and hard. RLIMIT_NPROC counts
// every task of the account, so the cap is the kernel's count now plus
// l.AccountTasks.
func limitRunner(l RunnerLimits) error {
	seen, err := accountTasks(os.Getuid())
	if err != nil {
		return fmt.Errorf("count the tasks of this account: %w", err)
	}
	tasks, err := kernelTasks(seen)
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

// kernelTasks returns the count that the kernel holds against RLIMIT_NPROC,
// at least seen. In a container /proc shows only the container's tasks, but
// the kernel counts every task of the uid in the user namespace. A fork fails
// with EAGAIN when that count has reached the soft limit, so a search over
// the soft limit finds it.
func kernelTasks(seen uint64) (uint64, error) {
	var now unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &now); err != nil {
		return 0, err
	}
	if seen >= now.Cur {
		return seen, nil
	}
	below := func(limit uint64) (bool, error) {
		if err := unix.Setrlimit(unix.RLIMIT_NPROC, &unix.Rlimit{Cur: limit, Max: now.Max}); err != nil {
			return false, err
		}
		_, err := syscall.ForkExec("", nil, nil)
		if err := unix.Setrlimit(unix.RLIMIT_NPROC, &now); err != nil {
			return false, err
		}
		return !errors.Is(err, syscall.EAGAIN), nil
	}
	lo, step := seen, uint64(1)
	for {
		hi := lo + step
		if hi >= now.Cur {
			hi = now.Cur
		}
		ok, err := below(hi)
		if err != nil {
			return 0, err
		}
		if ok {
			if hi == seen+1 {
				return seen, nil
			}
			for hi-lo > 1 {
				mid := lo + (hi-lo)/2
				if ok, err = below(mid); err != nil {
					return 0, err
				}
				if ok {
					hi = mid
				} else {
					lo = mid
				}
			}
			return lo, nil
		}
		if hi == now.Cur {
			return now.Cur, nil
		}
		lo, step = hi, step*2
	}
}

var procRoot = "/proc"

// accountTasks counts the threads of every process in procRoot whose real
// user is uid.
func accountTasks(uid int) (uint64, error) {
	dirs, err := filepath.Glob(filepath.Join(procRoot, "[0-9]*", "status"))
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
		return 0, fmt.Errorf("no task of user %d in %s", uid, procRoot)
	}
	return total, nil
}
