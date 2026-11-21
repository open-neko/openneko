package confine

import (
	"fmt"
	"runtime"
	"slices"
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

// refusedCalls open a socket, reach another process, signal it, change
// the metadata of a file by path, watch a path, or widen the kernel surface.
// Landlock below ABI 9 cannot refuse a connect to a named socket, so the
// runner opens no socket at all; no Landlock ABI covers metadata.
var refusedCalls = []uintptr{
	unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN,
	unix.SYS_SENDMSG, unix.SYS_SENDMMSG,
	unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_KCMP,
	unix.SYS_PIDFD_GETFD, unix.SYS_PROCESS_MADVISE, unix.SYS_GET_ROBUST_LIST,
	unix.SYS_MIGRATE_PAGES, unix.SYS_MOVE_PAGES,
	unix.SYS_KILL, unix.SYS_TKILL, unix.SYS_PIDFD_SEND_SIGNAL,
	unix.SYS_RT_SIGQUEUEINFO, unix.SYS_RT_TGSIGQUEUEINFO,
	unix.SYS_SCHED_SETSCHEDULER, unix.SYS_SCHED_SETPARAM, unix.SYS_SCHED_SETATTR, unix.SYS_IOPRIO_SET,
	unix.SYS_TRUNCATE,
	unix.SYS_FCHMODAT, unix.SYS_FCHMODAT2, unix.SYS_FCHOWN, unix.SYS_FCHOWNAT,
	unix.SYS_SETXATTR, unix.SYS_LSETXATTR, unix.SYS_FSETXATTR, unix.SYS_SETXATTRAT,
	unix.SYS_REMOVEXATTR, unix.SYS_LREMOVEXATTR, unix.SYS_FREMOVEXATTR, unix.SYS_REMOVEXATTRAT,
	unix.SYS_FILE_SETATTR,
	unix.SYS_INOTIFY_INIT1, unix.SYS_INOTIFY_ADD_WATCH, unix.SYS_FANOTIFY_INIT, unix.SYS_FANOTIFY_MARK,
	unix.SYS_UNSHARE, unix.SYS_SETNS,
	unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_USERFAULTFD,
	unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
}

// newestKnownCall is the highest call number this build knows. A newer
// call gets ENOSYS, as on a kernel that lacks it.
const newestKnownCall = unix.SYS_RSEQ_SLICE_YIELD

// cond holds when argument arg is one of values, both halves compared. With
// except, it holds when the low half is none of values.
type cond struct {
	arg    int
	values []uint32
	except bool
}

// argRules allow a call only when every cond holds; else EPERM.
func argRules(pid uint32) map[uintptr][]cond {
	return map[uintptr][]cond{
		unix.SYS_TGKILL:            {{arg: 0, values: []uint32{pid}}},
		unix.SYS_PIDFD_OPEN:        {{arg: 0, values: []uint32{pid}}},
		unix.SYS_PRLIMIT64:         {{arg: 0, values: []uint32{0, pid}}},
		unix.SYS_SCHED_SETAFFINITY: {{arg: 0, values: []uint32{0, pid}}},
		unix.SYS_SETPRIORITY:       {{arg: 0, values: []uint32{unix.PRIO_PROCESS}}, {arg: 1, values: []uint32{0, pid}}},
		unix.SYS_UTIMENSAT:         {{arg: 1, values: []uint32{0}}},
		unix.SYS_SENDTO:            {{arg: 4, values: []uint32{0}}},
		unix.SYS_FCNTL: {{arg: 1, except: true, values: []uint32{
			unix.F_SETOWN, unix.F_SETOWN_EX, unix.F_SETSIG, unix.F_SETLEASE, unix.F_NOTIFY}}},
		unix.SYS_IOCTL: {{arg: 1, except: true, values: []uint32{unix.TIOCSTI, unix.TIOCLINUX}}},
	}
}

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

// ruleBlock checks call nr against conds; another call falls through.
func ruleBlock(nr uintptr, conds []cond) []unix.SockFilter {
	type fixup struct {
		at     int
		onTrue bool
	}
	b := []unix.SockFilter{jump(jeq, uint32(nr), 0, 0)}
	add := func(ins unix.SockFilter) int { b = append(b, ins); return len(b) - 1 }
	setJump := func(at int, onTrue bool, target int) {
		off := uint8(target - at - 1)
		if onTrue {
			b[at].Jt = off
		} else {
			b[at].Jf = off
		}
	}
	var final []fixup
	for _, c := range conds {
		if c.except {
			add(stmt(ldAbs, uint32(argLow+8*c.arg)))
			for _, v := range c.values {
				final = append(final, fixup{add(jump(jeq, v, 0, 0)), true})
			}
			continue
		}
		add(stmt(ldAbs, uint32(argHigh+8*c.arg)))
		final = append(final, fixup{add(jump(jeq, 0, 0, 0)), false})
		add(stmt(ldAbs, uint32(argLow+8*c.arg)))
		var hits []int
		for _, v := range c.values {
			hits = append(hits, add(jump(jeq, v, 0, 0)))
		}
		final = append(final, fixup{hits[len(hits)-1], false})
		for _, at := range hits {
			setJump(at, true, len(b))
		}
	}
	add(stmt(ret, retAllow))
	deny := add(stmt(ret, retEPERM))
	for _, f := range final {
		setJump(f.at, f.onTrue, deny)
	}
	b[0].Jf = uint8(len(b) - 1)
	return b
}

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
	p = append(p, jump(jgt, newestKnownCall, 0, 1), stmt(ret, retENOSYS))
	for _, nr := range append(append(append([]uintptr{}, refusedCalls...), legacyRefusedCalls...), forkCalls...) {
		p = append(p, jump(jeq, uint32(nr), 0, 1), stmt(ret, retEPERM))
	}
	p = append(p, jump(jeq, unix.SYS_CLONE3, 0, 1), stmt(ret, retENOSYS))
	// A runner starts threads only: CLONE_THREAD needs CLONE_VM and
	// CLONE_SIGHAND, so the new task stays in the runner's process.
	p = append(p,
		jump(jeq, unix.SYS_CLONE, 0, 5),
		stmt(ldAbs, argLow),
		jump(jset, namespaceFlags, 2, 0),
		jump(jset, unix.CLONE_THREAD, 0, 1),
		stmt(ret, retAllow),
		stmt(ret, retEPERM),
	)
	rules := argRules(uint32(pid))
	nrs := make([]uintptr, 0, len(rules))
	for nr := range rules {
		nrs = append(nrs, nr)
	}
	slices.Sort(nrs)
	for _, nr := range nrs {
		p = append(p, ruleBlock(nr, rules[nr])...)
	}
	return append(p, stmt(ret, retAllow)), nil
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
