package native

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// Alive reports whether pid runs under the current account. A process of
// another account that has the same number is not ours.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	if ev, err := windows.WaitForSingleObject(h, 0); err != nil || ev != uint32(windows.WAIT_TIMEOUT) {
		return false
	}
	return ownProcess(h)
}

func ownProcess(h windows.Handle) bool {
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return false
	}
	defer tok.Close()
	them, err := tok.GetTokenUser()
	if err != nil {
		return false
	}
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && them.User.Sid.Equals(me.User.Sid)
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
// process asks this one to stop. Without its stop event a process still ends
// on Ctrl-C, and a supervisor stop kills it after the grace time.
func NotifyStop(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt)
	ev, err := stopEvent(os.Getpid())
	if err != nil {
		fmt.Fprintf(stopLog, "openneko: no stop event, so a stop request kills this process: %v\n", err)
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

var stopLog io.Writer = os.Stderr

// stopEvent makes the stop event of pid with an access list of the current
// account and SYSTEM. A name that exists already belongs to another process,
// which could set or hold it, so it is refused.
func stopEvent(pid int) (windows.Handle, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return 0, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return 0, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	ev, err := windows.CreateEvent(&sa, 1, 0, stopEventName(pid))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(ev)
		return 0, fmt.Errorf("the name of the stop event of pid %d is taken", pid)
	}
	if err != nil {
		return 0, err
	}
	return ev, nil
}

// A child starts suspended, so it joins the job object before it can start
// a process of its own; job.add resumes it.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED}
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
	if err := windows.AssignProcessToJobObject(j.h, ph); err != nil {
		return err
	}
	return resume(p.Pid)
}

func resume(pid int) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	resumed := 0
	e := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snap, &e); err == nil; err = windows.Thread32Next(snap, &e) {
		if e.OwnerProcessID != uint32(pid) {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, e.ThreadID)
		if err != nil {
			return fmt.Errorf("resume pid %d: %w", pid, err)
		}
		_, err = windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if err != nil {
			return fmt.Errorf("resume pid %d: %w", pid, err)
		}
		resumed++
	}
	if resumed == 0 {
		return fmt.Errorf("resume pid %d: it has no thread", pid)
	}
	return nil
}

func (j *job) close() {
	if j.h != 0 {
		_ = windows.CloseHandle(j.h)
		j.h = 0
	}
}

// The job object already ends the children with the supervisor.
var parentWatch = false

func FollowSupervisor() {}
