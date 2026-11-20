package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
}

func Lock(l Layout) (func(), error) {
	f, err := os.OpenFile(l.Lock(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
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
	if err := lockFile(f); err != nil {
		return true
	}
	var ol windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
	return false
}

func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// Windows has no SIGTERM for a process without a console, so each OpenNeko
// process waits on a named event that a stop sets.
func stopEventName(pid int) *uint16 {
	name, _ := windows.UTF16PtrFromString(`Local\OpenNeko-stop-` + strconv.Itoa(pid))
	return name
}

func terminate(p *os.Process) error {
	ev, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, stopEventName(p.Pid))
	if err != nil {
		return fmt.Errorf("pid %d takes no stop request: %w", p.Pid, err)
	}
	defer windows.CloseHandle(ev)
	return windows.SetEvent(ev)
}

// NotifyStop ends the context on Ctrl-C or Ctrl-Break, or when another
// process asks this one to stop.
func NotifyStop(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt)
	ev, err := windows.CreateEvent(nil, 1, 0, stopEventName(os.Getpid()))
	if err != nil {
		return ctx, stop
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		_, _ = windows.WaitForSingleObject(ev, windows.INFINITE)
		cancel()
	}()
	return ctx, func() {
		cancel()
		stop()
		_ = windows.SetEvent(ev)
	}
}

func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
}

// Detach starts a background supervisor with no console.
func Detach() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
}

// A job object kills every child when the supervisor ends, also when the
// supervisor is killed.
type job struct{ h windows.Handle }

func newJob() (*job, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return &job{h: h}, nil
}

func (j *job) add(p *os.Process) error {
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(j.h, ph)
}

func (j *job) close() { _ = windows.CloseHandle(j.h) }

// The job object already ends the children with the supervisor.
var parentWatch = false

func FollowSupervisor() {}
