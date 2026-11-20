package confine

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	retAllow = unix.SECCOMP_RET_ALLOW
	retEPERM = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	// clone3 passes its flags in memory, which a filter cannot read; ENOSYS
	// makes callers fall back to clone.
	retENOSYS = unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)

	argLow  = 16
	argHigh = 20

	namespaceFlags = unix.CLONE_NEWNS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWUTS | unix.CLONE_NEWIPC |
		unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNET | unix.CLONE_NEWTIME
)

// refusedCalls open a socket, reach another process, signal it, truncate a
// file by path, or widen the kernel surface. Landlock below ABI 9 cannot
// refuse a connect to a named socket, so the runner opens no socket at all.
var refusedCalls = []uintptr{
	unix.SYS_SOCKET, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN,
	unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_KCMP,
	unix.SYS_PIDFD_GETFD, unix.SYS_PROCESS_MADVISE,
	unix.SYS_KILL, unix.SYS_TKILL, unix.SYS_PIDFD_SEND_SIGNAL,
	unix.SYS_TRUNCATE,
	unix.SYS_UNSHARE, unix.SYS_SETNS,
	unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_USERFAULTFD,
	unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
}

// ownProcessCalls are allowed only with the runner's own process id as the
// first argument: the Go runtime signals its own threads with tgkill.
var ownProcessCalls = []uintptr{unix.SYS_TGKILL, unix.SYS_PIDFD_OPEN}

func auditArch() (uint32, error) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, nil
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, nil
	}
	return 0, fmt.Errorf("seccomp: no filter for %s", runtime.GOARCH)
}

func stmt(code uint16, k uint32) unix.SockFilter { return unix.SockFilter{Code: code, K: k} }

func jump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

const (
	ldAbs = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
	jeq   = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
	jgt   = unix.BPF_JMP | unix.BPF_JGT | unix.BPF_K
	jset  = unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K
	ret   = unix.BPF_RET | unix.BPF_K
)

// runnerFilter builds the seccomp program of the agent runner. A call from
// another architecture ends the process.
func runnerFilter(pid int) ([]unix.SockFilter, error) {
	arch, err := auditArch()
	if err != nil {
		return nil, err
	}
	p := []unix.SockFilter{
		stmt(ldAbs, 4),
		jump(jeq, arch, 1, 0),
		stmt(ret, unix.SECCOMP_RET_KILL_PROCESS),
		stmt(ldAbs, 0),
	}
	if runtime.GOARCH == "amd64" {
		p = append(p, jump(jgt, 0x3fffffff, 0, 1), stmt(ret, retEPERM))
	}
	for _, nr := range refusedCalls {
		p = append(p, jump(jeq, uint32(nr), 0, 1), stmt(ret, retEPERM))
	}
	p = append(p, jump(jeq, unix.SYS_CLONE3, 0, 1), stmt(ret, retENOSYS))
	p = append(p,
		jump(jeq, unix.SYS_CLONE, 0, 4),
		stmt(ldAbs, argLow),
		jump(jset, namespaceFlags, 0, 1),
		stmt(ret, retEPERM),
		stmt(ret, retAllow),
	)
	for _, nr := range ownProcessCalls {
		p = append(p,
			jump(jeq, uint32(nr), 0, 6),
			stmt(ldAbs, argHigh),
			jump(jeq, 0, 0, 3),
			stmt(ldAbs, argLow),
			jump(jeq, uint32(pid), 0, 1),
			stmt(ret, retAllow),
			stmt(ret, retEPERM),
		)
	}
	p = append(p,
		jump(jeq, unix.SYS_IOCTL, 0, 5),
		stmt(ldAbs, argLow+8),
		jump(jeq, unix.TIOCSTI, 2, 0),
		jump(jeq, unix.TIOCLINUX, 1, 0),
		stmt(ret, retAllow),
		stmt(ret, retEPERM),
		stmt(ret, retAllow),
	)
	return p, nil
}

// seccompAvailable tells whether the kernel takes a filter that returns an
// errno. A container engine may refuse the seccomp call itself.
func seccompAvailable() error {
	action := uint32(unix.SECCOMP_RET_ERRNO)
	if _, _, e := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_GET_ACTION_AVAIL, 0, uintptr(unsafe.Pointer(&action))); e != 0 {
		return fmt.Errorf("seccomp filters: %w", e)
	}
	return nil
}

// refuseCalls installs the runner's filter on every thread. TSYNC also gives
// every thread the caller's no_new_privs.
func refuseCalls() error {
	prog, err := runnerFilter(unix.Getpid())
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	fp := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	r, _, e := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&fp)))
	if e != 0 {
		return fmt.Errorf("install the seccomp filter: %w", e)
	}
	if r != 0 {
		return fmt.Errorf("install the seccomp filter: thread %d could not take it", r)
	}
	return nil
}
