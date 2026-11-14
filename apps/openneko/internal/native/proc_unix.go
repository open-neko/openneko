//go:build unix

package native

import (
	"fmt"
	"os"
	"syscall"
)

func Lock(l Layout) (func(), error) {
	f, err := os.OpenFile(l.Lock(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another OpenNeko supervisor runs from %s", l.Root)
	}
	return func() { _ = f.Close() }, nil
}

func Locked(l Layout) bool {
	f, err := os.OpenFile(l.Lock(), os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

func Alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// Detach puts a background supervisor in its own session.
func Detach() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
