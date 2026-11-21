package confine

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const childEnv = "OPENNEKO_TEST_CONFINE_CHILD"

// TestConfineChild confines itself and then reads the secret from threads that
// existed before the call. It prints one line per thread.
func TestConfineChild(t *testing.T) {
	dir := os.Getenv(childEnv)
	if dir == "" {
		t.Skip("helper process")
	}
	if v, err := strconv.Atoi(os.Getenv("OPENNEKO_TEST_CONFINE_ABI")); err == nil {
		abiCap = v
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		ready := make(chan struct{})
		go func() {
			defer wg.Done()
			runtime.LockOSThread()
			close(ready)
			<-start
			_, err := os.ReadFile(filepath.Join(dir, "secret"))
			results[i] = fmt.Sprintf("thread %d read the secret: %v", i, err == nil)
		}()
		<-ready
	}
	if os.Getenv("OPENNEKO_TEST_CONFINE_OFF") == "" {
		if st, err := Apply(Spec{ReadWrite: []string{filepath.Join(dir, "run")}}); err != nil {
			fmt.Println("apply:", st, err)
			return
		}
	}
	close(start)
	wg.Wait()
	for _, r := range results {
		fmt.Println(r)
	}
	tasks, _ := filepath.Glob("/proc/self/task/*/status")
	for _, task := range tasks {
		b, _ := os.ReadFile(task)
		if !strings.Contains(string(b), "NoNewPrivs:\t1") {
			fmt.Println("a thread has no no_new_privs:", task)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "run", "ok"), []byte("x"), 0o600); err != nil {
		fmt.Println("cannot write its folder:", err)
	}
}

func TestEveryThreadIsConfined(t *testing.T) {
	st := Available()
	if st.ABI < 1 {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s has no Landlock: %s", Kernel(), st.Reason)
		}
		t.Skip(st.Reason)
	}
	t.Logf("%s, Landlock ABI %d, gaps: %v", Kernel(), st.ABI, st.Gaps)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(off bool, abi int) string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConfineChild$", "-test.count=1")
		cmd.Env = append(os.Environ(), childEnv+"="+dir, fmt.Sprintf("OPENNEKO_TEST_CONFINE_ABI=%d", abi))
		if off {
			cmd.Env = append(cmd.Env, "OPENNEKO_TEST_CONFINE_OFF=1")
		}
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	if out := run(true, 0); strings.Count(out, "read the secret: true") != 8 {
		t.Fatalf("control: with no confinement every thread must read the secret:\n%s", out)
	}
	abis := []int{st.ABI}
	if st.ABI >= 8 {
		abis = append(abis, 7, 3)
	}
	for _, abi := range abis {
		out := run(false, abi)
		if strings.Contains(out, "this build uses cgo") {
			t.Logf("ABI %d needs a CGO_ENABLED=0 build; this test binary uses cgo", abi)
			continue
		}
		if strings.Contains(out, "read the secret: true") || strings.Count(out, "read the secret: false") != 8 {
			t.Errorf("ABI %d: a thread that existed before Apply read the secret:\n%s", abi, out)
		}
		for _, bad := range []string{"apply:", "no no_new_privs", "cannot write its folder"} {
			if strings.Contains(out, bad) {
				t.Errorf("ABI %d: %s\n%s", abi, bad, out)
			}
		}
	}
}

func TestAnOldKernelNamesWhatItLacks(t *testing.T) {
	g := strings.Join(gaps(1), "; ")
	for _, want := range []string{"ABI 2", "ABI 3", "ABI 5", "ABI 6", "ABI 9"} {
		if !strings.Contains(g, want) {
			t.Errorf("ABI 1 gaps %q do not name %s", g, want)
		}
	}
	if len(gaps(9)) != 0 {
		t.Errorf("ABI 9 has gaps %v", gaps(9))
	}
	if handled(1)&(1<<13) != 0 || handled(2)&(1<<13) == 0 || handled(8)&fsResolveUnix != 0 || handled(9)&fsResolveUnix == 0 {
		t.Error("the handled rights do not follow the ABI")
	}
}

func TestNoLandlockNamesTheCause(t *testing.T) {
	for _, c := range []struct {
		errno     syscall.Errno
		release   string
		container bool
		want      string
	}{
		{unix.ENOSYS, "4.18.0-553.el8", false, "Linux 4.18.0-553.el8 has no Landlock"},
		{unix.EOPNOTSUPP, "5.15.0-25", false, "has Landlock but it is off: add landlock to the lsm= boot list"},
		{unix.EPERM, "6.8.0-45", true, "the container engine blocks Landlock (Linux 6.8.0-45 has it)"},
		{unix.ENOSYS, "6.8.0-45", true, "the container engine blocks Landlock"},
		{unix.ENOSYS, "5.10.0", true, "Linux 5.10.0 has no Landlock"},
	} {
		if got := noLandlock(c.errno, c.release, c.container); !strings.Contains(got, c.want) {
			t.Errorf("%v %s container=%v: %q, want %q", c.errno, c.release, c.container, got, c.want)
		}
	}
}

// runFilter runs prog on one call, as the kernel would.
func runFilter(t *testing.T, prog []unix.SockFilter, arch uint32, nr uintptr, args ...uint64) uint32 {
	t.Helper()
	var data [64]byte
	binary.LittleEndian.PutUint32(data[0:], uint32(nr))
	binary.LittleEndian.PutUint32(data[4:], arch)
	for i, a := range args {
		binary.LittleEndian.PutUint64(data[16+8*i:], a)
	}
	var acc uint32
	for pc := 0; pc < len(prog); pc++ {
		ins := prog[pc]
		switch ins.Code {
		case ldAbs:
			acc = binary.LittleEndian.Uint32(data[ins.K:])
		case ret:
			return ins.K
		case jeq, jgt, jset:
			hit := acc == ins.K
			if ins.Code == jgt {
				hit = acc > ins.K
			} else if ins.Code == jset {
				hit = acc&ins.K != 0
			}
			if hit {
				pc += int(ins.Jt)
			} else {
				pc += int(ins.Jf)
			}
		default:
			t.Fatalf("instruction %d has code %#x", pc, ins.Code)
		}
	}
	t.Fatal("the filter ran off its end")
	return 0
}

func TestRunnerFilterRefusesTheListedCalls(t *testing.T) {
	const pid, other = 4242, 4343
	prog, err := runnerFilter(pid)
	if err != nil {
		t.Fatal(err)
	}
	arch, _ := auditArch()
	threadFlags := uint64(unix.CLONE_VM | unix.CLONE_FS | unix.CLONE_FILES | unix.CLONE_SIGHAND | unix.CLONE_THREAD | unix.CLONE_SYSVSEM)
	refused := append(append(append([]uintptr{}, refusedCalls...), legacyRefusedCalls...), forkCalls...)
	for _, nr := range refused {
		if got := runFilter(t, prog, arch, nr); got != retEPERM {
			t.Errorf("call %d: %#x, want EPERM", nr, got)
		}
	}
	for _, nr := range []uintptr{unix.SYS_SOCKET, unix.SYS_CONNECT, unix.SYS_TRUNCATE, unix.SYS_KILL, unix.SYS_PTRACE,
		unix.SYS_RT_SIGQUEUEINFO, unix.SYS_RT_TGSIGQUEUEINFO, unix.SYS_FCHMODAT, unix.SYS_FCHOWNAT, unix.SYS_SETXATTR,
		unix.SYS_INOTIFY_ADD_WATCH, unix.SYS_FANOTIFY_INIT, unix.SYS_SOCKETPAIR, unix.SYS_SENDMSG} {
		if !slices.Contains(refused, nr) {
			t.Errorf("call %d is not in the refused list", nr)
		}
	}
	for _, c := range []struct {
		what string
		nr   uintptr
		args []uint64
		want uint32
	}{
		{"read", unix.SYS_READ, nil, retAllow},
		{"fchmod on a descriptor", unix.SYS_FCHMOD, []uint64{5, 0o600}, retAllow},
		{"a thread", unix.SYS_CLONE, []uint64{threadFlags}, retAllow},
		{"a process", unix.SYS_CLONE, []uint64{uint64(unix.SIGCHLD)}, retEPERM},
		{"a thread in a new namespace", unix.SYS_CLONE, []uint64{threadFlags | unix.CLONE_NEWUSER}, retEPERM},
		{"clone3", unix.SYS_CLONE3, nil, retENOSYS},
		{"a call newer than this build", newestKnownCall + 1, nil, retENOSYS},
		{"tgkill to its own thread", unix.SYS_TGKILL, []uint64{pid, pid + 1, uint64(unix.SIGURG)}, retAllow},
		{"tgkill to another process", unix.SYS_TGKILL, []uint64{other, other, 0}, retEPERM},
		{"tgkill with a pid of 2^32 + its own", unix.SYS_TGKILL, []uint64{1<<32 | pid, pid, 0}, retEPERM},
		{"pidfd_open of itself", unix.SYS_PIDFD_OPEN, []uint64{pid}, retAllow},
		{"pidfd_open of another process", unix.SYS_PIDFD_OPEN, []uint64{other}, retEPERM},
		{"prlimit64 of itself (0)", unix.SYS_PRLIMIT64, []uint64{0, unix.RLIMIT_NOFILE}, retAllow},
		{"prlimit64 of itself (pid)", unix.SYS_PRLIMIT64, []uint64{pid, unix.RLIMIT_NOFILE}, retAllow},
		{"prlimit64 of another process", unix.SYS_PRLIMIT64, []uint64{other, unix.RLIMIT_NOFILE}, retEPERM},
		{"sched_setaffinity of itself", unix.SYS_SCHED_SETAFFINITY, []uint64{0}, retAllow},
		{"sched_setaffinity of another process", unix.SYS_SCHED_SETAFFINITY, []uint64{other}, retEPERM},
		{"setpriority of itself", unix.SYS_SETPRIORITY, []uint64{unix.PRIO_PROCESS, 0, 10}, retAllow},
		{"setpriority of another process", unix.SYS_SETPRIORITY, []uint64{unix.PRIO_PROCESS, other, 10}, retEPERM},
		{"setpriority of every process of the account", unix.SYS_SETPRIORITY, []uint64{unix.PRIO_USER, 0, 10}, retEPERM},
		{"setpriority of its group", unix.SYS_SETPRIORITY, []uint64{unix.PRIO_PGRP, 0, 10}, retEPERM},
		{"utimensat on a descriptor", unix.SYS_UTIMENSAT, []uint64{5, 0, 0x7000, 0}, retAllow},
		{"utimensat on a path", unix.SYS_UTIMENSAT, []uint64{0xffffff9c, 0x7000, 0, 0}, retEPERM},
		{"utimensat on a path above 4 GiB", unix.SYS_UTIMENSAT, []uint64{5, 1 << 32, 0, 0}, retEPERM},
		{"sendto with no address", unix.SYS_SENDTO, []uint64{3, 0x7000, 10, 0, 0, 0}, retAllow},
		{"sendto with an address", unix.SYS_SENDTO, []uint64{3, 0x7000, 10, 0, 0x8000, 110}, retEPERM},
		{"fcntl F_GETFL", unix.SYS_FCNTL, []uint64{3, unix.F_GETFL}, retAllow},
		{"fcntl F_SETFL", unix.SYS_FCNTL, []uint64{3, unix.F_SETFL, unix.O_NONBLOCK}, retAllow},
		{"fcntl F_DUPFD_CLOEXEC", unix.SYS_FCNTL, []uint64{3, unix.F_DUPFD_CLOEXEC}, retAllow},
		{"fcntl F_SETOWN", unix.SYS_FCNTL, []uint64{3, unix.F_SETOWN, other}, retEPERM},
		{"fcntl F_SETOWN_EX", unix.SYS_FCNTL, []uint64{3, unix.F_SETOWN_EX, 0x7000}, retEPERM},
		{"fcntl F_SETSIG", unix.SYS_FCNTL, []uint64{3, unix.F_SETSIG, uint64(unix.SIGUSR1)}, retEPERM},
		{"fcntl F_SETLEASE", unix.SYS_FCNTL, []uint64{3, unix.F_SETLEASE}, retEPERM},
		{"fcntl F_NOTIFY", unix.SYS_FCNTL, []uint64{3, unix.F_NOTIFY}, retEPERM},
		{"ioctl FIONREAD", unix.SYS_IOCTL, []uint64{3, unix.TIOCINQ}, retAllow},
		{"ioctl TIOCSTI", unix.SYS_IOCTL, []uint64{0, unix.TIOCSTI}, retEPERM},
		{"ioctl TIOCLINUX", unix.SYS_IOCTL, []uint64{0, unix.TIOCLINUX}, retEPERM},
	} {
		if got := runFilter(t, prog, arch, c.nr, c.args...); got != c.want {
			t.Errorf("%s: %#x, want %#x", c.what, got, c.want)
		}
	}
	if got := runFilter(t, prog, arch^1, unix.SYS_READ); got != unix.SECCOMP_RET_KILL_PROCESS {
		t.Errorf("a call from another architecture: %#x", got)
	}
	if len(prog) > 4096 {
		t.Errorf("the filter has %d instructions; the kernel takes 4096", len(prog))
	}
}

func TestRunnerGapsNameWhatTheFilterLeaves(t *testing.T) {
	for _, abi := range []int{1, 8, 9} {
		g := strings.Join(runnerGaps(abi), "; ")
		if !strings.Contains(g, "metadata") {
			t.Errorf("ABI %d: the gaps %q do not name the metadata read", abi, g)
		}
		for _, closed := range []string{"signal", "socket", "truncate"} {
			if strings.Contains(g, closed) {
				t.Errorf("ABI %d: the gaps %q name %s, which the filter closes", abi, g, closed)
			}
		}
	}
	if g := strings.Join(runnerGaps(1), "; "); !strings.Contains(g, "ABI 2") || !strings.Contains(g, "ABI 5") {
		t.Errorf("ABI 1 gaps %q", g)
	}
}
