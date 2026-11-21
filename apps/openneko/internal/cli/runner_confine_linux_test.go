package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// probeGram is an unbound datagram socket that the runner holds from before
// it confines itself, as a descriptor it got from outside could be.
var probeGram = -1

const fOwnerPID = 1

func setProbeMode(in runnerProbeIn) {
	confine.SetForTest(in.ABI, in.Mode == "seccomp", in.Mode == "landlock")
	serve.SetRunnerLimitsOff(strings.HasSuffix(in.Mode, "nolimits"))
	probeGram, _ = unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
}

// runnerControlFree lists checks that may fail with no confinement too: they
// need the internet or a right that an unconfined runner lacks as well.
var runnerControlFree = map[string]bool{
	"TCP to 1.1.1.1:443": true, "TCP to example.com:443": true, "UDP to 1.1.1.1:53": true, "resolve a name": true,
	"open a raw, packet or ICMP socket": true, "TCP over IPv6 loopback": true,
	"connect to the Docker socket": true, "connect to the D-Bus session bus": true, "send to /dev/log": true,
}

func runnerChecks(ctx context.Context, in runnerProbeIn, workspace string) probeResult {
	out := probeResult{Refused: map[string]bool{}, Allowed: map[string]bool{}}
	refused := func(what string, err error) {
		out.Refused[what] = err != nil
		if err != nil && os.Getenv("OPENNEKO_TEST_VERBOSE") != "" {
			out.Notes = append(out.Notes, fmt.Sprintf("%s: %v", what, err))
		}
	}
	_, err := os.ReadFile(in.Secret)
	refused("read the planted secret key", err)
	_, err = os.ReadDir(filepath.Dir(in.Secret))
	refused("list the config folder", err)
	_, err = os.ReadDir(in.Home)
	refused("list the home folder", err)
	refused("TCP to web's loopback listener", dial("tcp", in.TCP))
	refused("TCP to 1.1.1.1:443", dial("tcp", "1.1.1.1:443"))
	refused("TCP to example.com:443", dial("tcp", "example.com:443"))
	refused("UDP to loopback", sendUDP(in.UDP))
	refused("UDP to 1.1.1.1:53", sendUDP("1.1.1.1:53"))
	if in.TCP6 != "" {
		refused("TCP over IPv6 loopback", dial("tcp", in.TCP6))
	}
	lctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	_, err = net.DefaultResolver.LookupHost(lctx, "example.com")
	cancel()
	refused("resolve a name", err)
	msg, err := exec.Command("/bin/sh", "-c", "echo ran").Output()
	if err == nil && !strings.Contains(string(msg), "ran") {
		err = fmt.Errorf("no output")
	}
	refused("run another program", err)
	_, err = syscall.ForkExec(filepath.Join(workspace, "no-such-program"), nil, nil)
	if !errors.Is(err, syscall.EPERM) {
		err = nil
	}
	refused("start a child process", err)
	refused("open a raw, packet or ICMP socket", rawSocket())
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", in.Parent))
	if err == nil {
		got := make([]byte, len(webSecret))
		_, err = mem.ReadAt(got, int64(in.ParentAddr))
		mem.Close()
		if err == nil && string(got) != webSecret {
			err = fmt.Errorf("read %q, not the secret", got)
		}
	}
	refused("read web's /proc/<pid>/mem", err)
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", in.Parent))
	if err == nil && !strings.Contains(string(b), "PATH=") {
		err = fmt.Errorf("read no variable")
	}
	refused("read web's /proc/<pid>/environ", err)
	if ptraceRefused(in.Parent) {
		err = fmt.Errorf("refused")
	} else {
		err = nil
	}
	refused("ptrace web", err)
	buf := make([]byte, len(webSecret))
	_, err = unix.ProcessVMReadv(in.Parent, []unix.Iovec{{Base: &buf[0], Len: uint64(len(buf))}},
		[]unix.RemoteIovec{{Base: in.ParentAddr, Len: len(buf)}}, 0)
	refused("process_vm_readv web", err)
	refused("signal web (kill 0)", unix.Kill(in.Parent, 0))
	refused("signal a thread of web (tgkill 0)", unix.Tgkill(in.Parent, in.Parent, 0))
	fd, err := unix.PidfdOpen(in.Parent, 0)
	if err == nil {
		unix.Close(fd)
	}
	refused("open a pidfd of web", err)
	refused("truncate a file outside its folders", unix.Truncate(in.Victim, 0))
	refused("queue a signal to web (rt_sigqueueinfo)", queueSignal(in.Parent, 0))
	refused("queue a signal to a thread of web (rt_tgsigqueueinfo)", queueSignal(in.Parent, in.Parent))
	refused("make web the owner of a descriptor (F_SETOWN)", onWorkspaceFile(workspace, func(fd int) error {
		_, err := unix.FcntlInt(uintptr(fd), unix.F_SETOWN, in.Parent)
		return err
	}))
	refused("make web the owner of a descriptor (F_SETOWN_EX)", onWorkspaceFile(workspace, func(fd int) error {
		owner := struct{ kind, pid int32 }{fOwnerPID, int32(in.Parent)}
		_, _, e := unix.Syscall(unix.SYS_FCNTL, uintptr(fd), unix.F_SETOWN_EX, uintptr(unsafe.Pointer(&owner)))
		return errnoErr(e)
	}))
	refused("choose the signal of a descriptor (F_SETSIG)", onWorkspaceFile(workspace, func(fd int) error {
		_, err := unix.FcntlInt(uintptr(fd), unix.F_SETSIG, int(unix.SIGUSR1))
		return err
	}))
	var limit unix.Rlimit
	refused("read web's limits (prlimit64)", unix.Prlimit(in.Parent, unix.RLIMIT_NOFILE, nil, &limit))
	var cpus unix.CPUSet
	err = unix.SchedGetaffinity(in.Parent, &cpus)
	if err == nil {
		err = unix.SchedSetaffinity(in.Parent, &cpus)
	}
	refused("set web's CPU affinity", err)
	prio, err := unix.Getpriority(unix.PRIO_PROCESS, in.Parent)
	if err == nil {
		err = unix.Setpriority(unix.PRIO_PROCESS, in.Parent, 20-prio)
	}
	refused("set web's priority", err)
	refused("chmod a file outside its folders (fchmodat)", unix.Fchmodat(unix.AT_FDCWD, in.Victim, 0o600, 0))
	refused("chmod a file outside its folders (fchmodat2)", pathCall(unix.SYS_FCHMODAT2, in.Victim, 0o600))
	refused("chown a file outside its folders (fchownat)", unix.Fchownat(unix.AT_FDCWD, in.Victim, os.Getuid(), os.Getgid(), 0))
	var st unix.Stat_t
	err = unix.Stat(in.Victim, &st)
	if err == nil {
		err = unix.UtimesNanoAt(unix.AT_FDCWD, in.Victim, []unix.Timespec{st.Atim, st.Mtim}, 0)
	}
	refused("set the times of a file outside its folders (utimensat)", err)
	refused("set an attribute of a file outside its folders (setxattr)", unix.Setxattr(in.Victim, "user.openneko-probe", []byte("x"), 0))
	refused("set an attribute of a file outside its folders (lsetxattr)", unix.Lsetxattr(in.Victim, "user.openneko-probe", []byte("y"), 0))
	refused("remove an attribute of a file outside its folders (removexattr)", unix.Removexattr(in.Victim, "user.openneko-probe"))
	refused("watch the config folder (inotify)", watchInotify(filepath.Dir(in.Secret)))
	refused("watch the config folder (fanotify)", watchFanotify(filepath.Dir(in.Secret)))
	for what, err := range legacyMetadataCalls(in.Victim) {
		refused(what, err)
	}
	if probeGram < 0 {
		refused("send to a datagram socket outside its folders (sendto)", nil)
		refused("send to a datagram socket outside its folders (sendmsg)", nil)
	} else {
		dest := &unix.SockaddrUnix{Name: in.Datagram}
		refused("send to a datagram socket outside its folders (sendto)", unix.Sendto(probeGram, []byte("x"), 0, dest))
		_, err = unix.SendmsgN(probeGram, []byte("x"), nil, dest, 0)
		refused("send to a datagram socket outside its folders (sendmsg)", err)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.Close(pair[0])
		unix.Close(pair[1])
	}
	refused("make a socket pair", err)
	refused("write outside its folders", os.WriteFile(in.Outside, []byte("x"), 0o600))
	refused("connect to a Unix socket outside its folders", dial("unix", in.Stream))
	refused("send to a datagram socket outside its folders", sendUnixgram(in.Datagram))
	refused("connect to an abstract socket", dial("unix", "@"+in.Abstract))
	if in.Docker != "" {
		refused("connect to the Docker socket", dial("unix", in.Docker))
	}
	if in.DBus != "" {
		refused("connect to the D-Bus session bus", dial("unix", in.DBus))
	}
	if _, err := os.Stat("/dev/log"); err == nil {
		refused("send to /dev/log", sendUnixgram("/dev/log"))
	}
	if probeGram < 0 {
		for _, what := range []string{"make web the owner of a socket (FIOSETOWN)", "make web the owner of a socket (SIOCSPGRP)",
			"turn on signal-driven I/O (F_SETFL O_ASYNC)", "turn on signal-driven I/O (FIOASYNC)"} {
			refused(what, nil)
		}
	} else {
		parent := int32(in.Parent)
		refused("make web the owner of a socket (FIOSETOWN)", ioctlPtr(probeGram, 0x8901, unsafe.Pointer(&parent)))
		refused("make web the owner of a socket (SIOCSPGRP)", ioctlPtr(probeGram, unix.SIOCSPGRP, unsafe.Pointer(&parent)))
		_, err = unix.FcntlInt(uintptr(probeGram), unix.F_SETFL, unix.O_ASYNC|unix.O_NONBLOCK)
		refused("turn on signal-driven I/O (F_SETFL O_ASYNC)", err)
		on := int32(1)
		refused("turn on signal-driven I/O (FIOASYNC)", ioctlPtr(probeGram, 0x5452, unsafe.Pointer(&on)))
		_, _ = unix.FcntlInt(uintptr(probeGram), unix.F_SETFL, unix.O_NONBLOCK)
	}
	_, err = unix.FcntlInt(uintptr(probeGram), unix.F_SETFL, unix.O_NONBLOCK)
	out.Allowed["set O_NONBLOCK (F_SETFL)"] = probeGram < 0 || err == nil
	shm, err := unix.SysvShmGet(unix.IPC_PRIVATE, 4096, unix.IPC_CREAT|0o600)
	if err == nil {
		_, _ = unix.SysvShmCtl(shm, unix.IPC_RMID, nil)
	}
	refused("create shared memory (shmget)", err)
	seg, err := unix.SysvShmAttach(in.Shm, 0, 0)
	if err == nil {
		_ = unix.SysvShmDetach(seg)
	}
	refused("attach web's shared memory (shmat)", err)
	refused("create a message queue (msgget)", sysvCreate(unix.SYS_MSGGET, unix.SYS_MSGCTL, 0))
	refused("create a semaphore set (semget)", sysvCreate(unix.SYS_SEMGET, unix.SYS_SEMCTL, 1))
	refused("open a POSIX message queue (mq_open)", posixQueue(probeQueue(in.Parent)))
	refused("change its parent-death signal (prctl)", unix.Prctl(unix.PR_SET_PDEATHSIG, 0, 0, 0, 0))
	_ = unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0)
	webGroup, _ := unix.Getpgid(in.Parent)
	err = unix.Setpgid(0, webGroup)
	if err == nil {
		_ = unix.Setpgid(0, 0)
	}
	refused("leave its process group (setpgid)", err)
	err = unix.Setpgid(0, webGroup)
	if err == nil {
		_, err = unix.Setsid()
	}
	refused("start a session of its own (setsid)", err)
	var files unix.Rlimit
	_ = unix.Getrlimit(unix.RLIMIT_NOFILE, &files)
	refused("set a limit (prlimit64)", unix.Prlimit(0, unix.RLIMIT_NOFILE, &files, nil))
	_, _, e := unix.Syscall(unix.SYS_SETRLIMIT, unix.RLIMIT_NOFILE, uintptr(unsafe.Pointer(&files)), 0)
	refused("set a limit (setrlimit)", errnoErr(e))

	out.Limited = runnerLimits(workspace, in.NProc)
	tool := filepath.Join(workspace, "tool")
	err = copyFile("/bin/true", tool)
	if err == nil {
		err = exec.Command(tool).Run()
	}
	refused("run a file it wrote", err)

	err = os.WriteFile(filepath.Join(workspace, "work.txt"), []byte("kept"), 0o600)
	if err == nil {
		_, err = os.ReadFile(filepath.Join(workspace, "work.txt"))
	}
	out.Allowed["write and read its workspace"] = err == nil
	f, err := os.OpenFile(filepath.Join(workspace, "work.txt"), os.O_RDWR, 0)
	if err == nil {
		err = f.Truncate(1)
		f.Close()
	}
	out.Allowed["truncate a workspace file through its descriptor"] = err == nil
	out.Allowed["reach the key door through the broker"] = keyDoorAnswers(&out)
	return out
}

func ioctlPtr(fd int, req uint, arg unsafe.Pointer) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	return errnoErr(e)
}

func sysvCreate(get, ctl uintptr, nsems uintptr) error {
	args := []uintptr{unix.IPC_PRIVATE, unix.IPC_CREAT | 0o600}
	if nsems > 0 {
		args = []uintptr{unix.IPC_PRIVATE, nsems, unix.IPC_CREAT | 0o600}
	}
	var id uintptr
	var e syscall.Errno
	if len(args) == 2 {
		id, _, e = unix.Syscall(get, args[0], args[1], 0)
	} else {
		id, _, e = unix.Syscall(get, args[0], args[1], args[2])
	}
	if e != 0 {
		return e
	}
	if nsems > 0 {
		_, _, _ = unix.Syscall6(ctl, id, 0, unix.IPC_RMID, 0, 0, 0)
	} else {
		_, _, _ = unix.Syscall(ctl, id, unix.IPC_RMID, 0)
	}
	return nil
}

// probeQueue is the queue name of a probe. Landlock refuses the open after
// the kernel made the queue, so the test removes it after each probe.
func probeQueue(parent int) string { return fmt.Sprintf("openneko-probe-%d", parent) }

func removeProbeLeftovers(in runnerProbeIn) { removeQueue(probeQueue(in.Parent)) }

func removeQueue(name string) {
	p, _ := unix.BytePtrFromString(name)
	_, _, _ = unix.Syscall(unix.SYS_MQ_UNLINK, uintptr(unsafe.Pointer(p)), 0, 0)
}

func posixQueue(queue string) error {
	name, _ := unix.BytePtrFromString(queue)
	fd, _, e := unix.Syscall6(unix.SYS_MQ_OPEN, uintptr(unsafe.Pointer(name)), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC, 0o600, 0, 0, 0)
	if e != 0 {
		return e
	}
	unix.Close(int(fd))
	_, _, _ = unix.Syscall(unix.SYS_MQ_UNLINK, uintptr(unsafe.Pointer(name)), 0, 0)
	return nil
}

// runnerLimits tells, for each limit, whether it holds in this runner.
func runnerLimits(workspace string, nproc uint64) map[string]bool {
	out := map[string]bool{}
	big := filepath.Join(workspace, "big")
	f, err := os.Create(big)
	if err == nil {
		_, err = f.WriteAt([]byte("x"), sandbox.MaxTransfer)
		f.Close()
		os.Remove(big)
	}
	out["write past the file size limit"] = errors.Is(err, syscall.EFBIG)
	m, err := unix.Mmap(-1, 0, 9<<30, unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS|unix.MAP_NORESERVE)
	if err == nil {
		_ = unix.Munmap(m)
	}
	out["map more than the address space limit"] = errors.Is(err, syscall.ENOMEM)
	capped := func(resource int, most uint64) bool {
		var l unix.Rlimit
		return unix.Getrlimit(resource, &l) == nil && l.Cur == l.Max && l.Max <= most
	}
	out["open files capped at 1024"] = capped(unix.RLIMIT_NOFILE, 1024)
	out["CPU time capped at the turn's deadline"] = capped(unix.RLIMIT_CPU, 31*60)
	out["threads capped below web's limit"] = capped(unix.RLIMIT_NPROC, nproc-1)
	return out
}

// floodThreads pins goroutines to threads until the process has n threads,
// as native code that floods threads would. It returns the count reached.
// Each goroutine holds its own thread once it counts itself.
func floodThreads(n int) int {
	block := make(chan struct{})
	var pinned atomic.Int64
	for range n {
		go func() {
			runtime.LockOSThread()
			pinned.Add(1)
			<-block
		}()
	}
	deadline := time.Now().Add(20 * time.Second)
	for int(pinned.Load()) < n && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	return int(pinned.Load())
}

func errnoErr(e syscall.Errno) error {
	if e != 0 {
		return e
	}
	return nil
}

func queueSignal(pid, tid int) error {
	var info [128]byte
	*(*int32)(unsafe.Pointer(&info[8])) = -1
	var e syscall.Errno
	if tid == 0 {
		_, _, e = unix.Syscall(unix.SYS_RT_SIGQUEUEINFO, uintptr(pid), 0, uintptr(unsafe.Pointer(&info[0])))
	} else {
		_, _, e = unix.Syscall6(unix.SYS_RT_TGSIGQUEUEINFO, uintptr(pid), uintptr(tid), 0, uintptr(unsafe.Pointer(&info[0])), 0, 0)
	}
	return errnoErr(e)
}

func onWorkspaceFile(workspace string, do func(fd int) error) error {
	fd, err := unix.Open(filepath.Join(workspace, "owned.txt"), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("the check cannot open its own file: %w", err)
	}
	defer unix.Close(fd)
	return do(fd)
}

func pathCall(nr uintptr, path string, mode uint32) error {
	p, err := unix.BytePtrFromString(path)
	if err != nil {
		return err
	}
	cwd := unix.AT_FDCWD
	_, _, e := unix.Syscall6(nr, uintptr(cwd), uintptr(unsafe.Pointer(p)), uintptr(mode), 0, 0, 0)
	return errnoErr(e)
}

func watchInotify(dir string) error {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	_, err = unix.InotifyAddWatch(fd, dir, unix.IN_ALL_EVENTS)
	return err
}

func watchFanotify(dir string) error {
	fd, err := unix.FanotifyInit(unix.FAN_CLASS_NOTIF|unix.FAN_REPORT_FID|unix.FAN_CLOEXEC, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.FanotifyMark(fd, unix.FAN_MARK_ADD, unix.FAN_MODIFY|unix.FAN_EVENT_ON_CHILD, unix.AT_FDCWD, dir)
}

func rawSocket() error {
	for _, f := range [][3]int{{unix.AF_PACKET, unix.SOCK_RAW, 0}, {unix.AF_INET, unix.SOCK_RAW, unix.IPPROTO_ICMP}, {unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_ICMP}} {
		if fd, err := unix.Socket(f[0], f[1], f[2]); err == nil {
			unix.Close(fd)
			return nil
		}
	}
	return fmt.Errorf("refused")
}

func newRunnerTargets(t *testing.T) runnerTargets {
	t.Helper()
	base := t.TempDir()
	other := filepath.Join(base, "other")
	config := filepath.Join(base, "config", "openneko")
	home := filepath.Join(base, "home")
	workspace := filepath.Join(base, "agent-runs", "turn-1")
	tmp := filepath.Join(base, "tmp")
	for _, d := range []string{other, config, home, workspace, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	in := runnerProbeIn{Secret: filepath.Join(config, "secret-key"), Home: home, Outside: filepath.Join(other, "planted-by-runner"),
		Victim: filepath.Join(other, "victim"), Parent: os.Getpid(), ParentAddr: uintptr(unsafe.Pointer(unsafe.StringData(webSecret)))}
	for p, body := range map[string]string{in.Secret: "planted-secret-key", filepath.Join(home, ".bash_history"): "planted", in.Victim: "keep me"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	in.TCP = acceptAll(t, "tcp", "127.0.0.1:0").Addr().String()
	if ln := acceptAll(t, "tcp", "[::1]:0"); ln != nil {
		in.TCP6 = ln.Addr().String()
	}
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { udp.Close() })
	in.UDP = udp.LocalAddr().String()
	in.Stream = filepath.Join(other, "docker.sock")
	if acceptAll(t, "unix", in.Stream) == nil {
		t.Fatal("cannot listen on", in.Stream)
	}
	in.Datagram = filepath.Join(other, "log.sock")
	gram, err := net.ListenPacket("unixgram", in.Datagram)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gram.Close() })
	in.Abstract = fmt.Sprintf("openneko-runner-probe-%d", os.Getpid())
	if acceptAll(t, "unix", "@"+in.Abstract) == nil {
		t.Fatal("cannot listen on an abstract socket")
	}
	if host, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok {
		in.Docker = host
	} else if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		in.Docker = "/var/run/docker.sock"
	}
	if bus := fmt.Sprintf("/run/user/%d/bus", os.Getuid()); fileExists(bus) {
		in.DBus = bus
	}
	shm, err := unix.SysvShmGet(unix.IPC_PRIVATE, 4096, unix.IPC_CREAT|0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = unix.SysvShmCtl(shm, unix.IPC_RMID, nil) })
	in.Shm = shm
	t.Cleanup(func() { removeQueue(probeQueue(in.Parent)) })
	var nproc unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &nproc); err != nil {
		t.Fatal(err)
	}
	in.NProc = nproc.Max
	return runnerTargets{in: in, workspace: workspace, tmp: tmp}
}

// TestConfinedRunner runs the checks of the CONF0 probe, plus truncate,
// signal and pidfd checks, inside a real agent runner in a one-account
// layout: the test process stands for serve and web, with the same uid.
func TestConfinedRunner(t *testing.T) {
	st := requireRunnerConfinement(t)
	tg := newRunnerTargets(t)
	serveFakeDoor(t)
	_ = unix.Prctl(unix.PR_SET_PTRACER, unix.PR_SET_PTRACER_ANY, 0, 0, 0)
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_PTRACER, 0, 0, 0, 0) })

	control, msg := runRunnerProbe(t, tg, "control")
	if control.Refused == nil {
		t.Fatalf("control: the runner gave no result: %s", msg)
	}
	for what, refused := range control.Refused {
		if refused && !runnerControlFree[what] {
			t.Errorf("control: with no confinement the runner could not %s, so the check proves nothing", what)
		}
	}
	legacy := legacyMetadataCalls(filepath.Join(t.TempDir(), "none"))
	abis := []int{0}
	if st.ABI > 1 {
		abis = append(abis, 1)
	}
	for _, abi := range abis {
		capped := tg
		capped.in.ABI = abi
		got, msg := runRunnerProbe(t, capped, "full")
		name := fmt.Sprintf("ABI %d", st.ABI)
		if abi > 0 {
			name = fmt.Sprintf("ABI capped at %d", abi)
		}
		if got.Refused == nil {
			if strings.Contains(msg, "this build uses cgo") && os.Getenv("OPENNEKO_TEST_REQUIRE_CONFINE") == "" {
				t.Logf("%s needs a CGO_ENABLED=0 build, as the release is: %s", name, msg)
				continue
			}
			t.Fatalf("%s: the confined runner gave no result: %s", name, msg)
		}
		if len(got.Refused) != len(control.Refused) || len(got.Refused) < 46+len(legacy) {
			t.Fatalf("%s: the runner ran %d checks confined and %d unconfined", name, len(got.Refused), len(control.Refused))
		}
		for what, refused := range got.Refused {
			if !refused {
				t.Errorf("%s: the confined runner could %s", name, what)
			}
		}
		for what, ok := range got.Allowed {
			if !ok {
				t.Errorf("%s: the confined runner could not %s %v", name, what, got.Notes)
			}
		}
		if fileExists(tg.in.Outside) {
			t.Errorf("%s: the confined runner wrote outside its folders", name)
		}
		if len(got.Limited) < 5 {
			t.Errorf("%s: the runner checked %d limits", name, len(got.Limited))
		}
		for what, held := range got.Limited {
			if !held {
				t.Errorf("%s: the runner's limit did not hold: %s", name, what)
			}
		}
	}

	open, _ := runRunnerProbe(t, tg, "nolimits")
	if len(open.Limited) != len(control.Limited) {
		t.Fatalf("with no limits the runner checked %d limits, with them %d", len(open.Limited), len(control.Limited))
	}
	for what, held := range open.Limited {
		if held {
			t.Errorf("with no limits set, this still holds: %s: the check does not prove the limit", what)
		}
	}

	noSeccomp, _ := runRunnerProbe(t, tg, "landlock")
	seccompOnly := []string{"connect to a Unix socket outside its folders", "send to a datagram socket outside its folders", "UDP to loopback", "start a child process",
		"make web the owner of a descriptor (F_SETOWN)", "make web the owner of a descriptor (F_SETOWN_EX)", "choose the signal of a descriptor (F_SETSIG)",
		"read web's limits (prlimit64)", "set web's CPU affinity", "set web's priority",
		"chmod a file outside its folders (fchmodat)", "chmod a file outside its folders (fchmodat2)", "chown a file outside its folders (fchownat)",
		"set the times of a file outside its folders (utimensat)", "set an attribute of a file outside its folders (setxattr)",
		"set an attribute of a file outside its folders (lsetxattr)", "remove an attribute of a file outside its folders (removexattr)",
		"watch the config folder (inotify)", "watch the config folder (fanotify)",
		"send to a datagram socket outside its folders (sendto)", "send to a datagram socket outside its folders (sendmsg)", "make a socket pair",
		"make web the owner of a socket (FIOSETOWN)", "make web the owner of a socket (SIOCSPGRP)",
		"turn on signal-driven I/O (F_SETFL O_ASYNC)", "turn on signal-driven I/O (FIOASYNC)",
		"create shared memory (shmget)", "attach web's shared memory (shmat)", "create a message queue (msgget)",
		"create a semaphore set (semget)", "change its parent-death signal (prctl)",
		"leave its process group (setpgid)", "start a session of its own (setsid)", "set a limit (prlimit64)", "set a limit (setrlimit)"}
	for what := range legacy {
		seccompOnly = append(seccompOnly, what)
	}
	for _, what := range seccompOnly {
		if noSeccomp.Refused[what] {
			t.Errorf("with Landlock only, the runner still could not %s: the check does not prove the seccomp filter", what)
		}
	}
	noLandlock, _ := runRunnerProbe(t, tg, "seccomp")
	for _, what := range []string{"read the planted secret key", "write outside its folders", "read web's /proc/<pid>/mem"} {
		if noLandlock.Refused[what] {
			t.Errorf("with seccomp only, the runner still could not %s: the check does not prove Landlock", what)
		}
	}
}

// TestRunnerThreadFloodStaysInTheRunner floods threads in a confined
// runner: the runner stops at its cap, and the account can still start
// threads.
func TestRunnerThreadFloodStaysInTheRunner(t *testing.T) {
	requireRunnerConfinement(t)
	tg := newRunnerTargets(t)
	open, msg := runRunnerProbe(t, tg, "flood-nolimits")
	if open.Threads < floodTarget {
		t.Fatalf("with no limits the runner reached %d threads of %d (%s), so the check proves nothing", open.Threads, floodTarget, msg)
	}
	capped, msg := runRunnerProbe(t, tg, "flood")
	if capped.Threads != 0 || !strings.Contains(msg, "stopped before the turn ended") {
		t.Fatalf("the confined runner reached %d threads, turn error %q", capped.Threads, msg)
	}
	if got := floodThreads(64); got < 64 {
		t.Fatalf("after the flood this account started too few threads: %d", got)
	}
}

func TestRunnerRefusesATurnWhenItCannotConfine(t *testing.T) {
	tg := newRunnerTargets(t)
	tg.workspace = ""
	_, msg := runRunnerProbe(t, tg, "full")
	if !strings.Contains(msg, "could not confine itself, so the turn did not run") {
		t.Fatalf("a runner with no folder to keep ran the turn: %q", msg)
	}
}

func TestTurnsAreOffWhenRunnersCannotBeConfined(t *testing.T) {
	t.Setenv(serveConfineEnv, "")
	kernelLacks := confine.State{Mechanism: "landlock+seccomp", Reason: "Linux 4.18.0 has no Landlock: use kernel 5.13 or later with landlock in the lsm= boot list"}
	defer func(old func() confine.State) { runnerAvailable = old }(runnerAvailable)
	runnerAvailable = func() confine.State { return kernelLacks }

	var log strings.Builder
	st, confineIt, off := runnerConfinement(&log, false)
	want := "agent confinement: off. Linux 4.18.0 has no Landlock: use kernel 5.13 or later with landlock in the lsm= boot list, so agent turns are off"
	if off != want || confineIt || st.Confined || !strings.Contains(log.String(), want) {
		t.Fatalf("production: off=%q confine=%v log=%q", off, confineIt, log.String())
	}
	if _, _, off := runnerConfinement(io.Discard, true); off != "" {
		t.Fatalf("development mode turned turns off: %q", off)
	}
	runnerAvailable = func() confine.State { return confine.State{Reason: "confinement on darwin is not built yet"} }
	if _, confineIt, off := runnerConfinement(io.Discard, false); off != confine.NotAvailable+" (confinement on darwin is not built yet), so agent turns are off" || confineIt {
		t.Fatalf("a system with no runner confinement built yet: off=%q confine=%v", off, confineIt)
	}

	runnerAvailable = func() confine.State { return kernelLacks }
	sock := filepath.Join(t.TempDir(), "serve.sock")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	t.Setenv("OPENNEKO_ENV", "production")
	cleanServeEnv(t)
	done, err := serveAgentTurns(ctx, io.Discard, sock, 1, sandbox.Config{}, confine.State{Mechanism: "landlock", ABI: 8, Confined: true}, confine.State{Mechanism: "landlock", ABI: 8, Confined: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { stop(); <-done }()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := serve.Frame{Type: "start", Spec: json.RawMessage(`{}`), Environ: []string{"OPENNEKO_HARNESS_WORKSPACE_DIR=" + t.TempDir()}}
	if err := json.NewEncoder(conn).Encode(start); err != nil {
		t.Fatal(err)
	}
	var exit serve.Frame
	if err := json.NewDecoder(conn).Decode(&exit); err != nil || exit.Type != "exit" || exit.Error != want {
		t.Fatalf("a turn on a host that cannot confine its runner: %+v, %v", exit, err)
	}
	if _, _, _, err := checkServeSocket(context.Background(), sock); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("release-check passed a serve that refuses turns: %v", err)
	}
}

func TestDoctorNamesTheConfinementAndTheModules(t *testing.T) {
	defer func(old func() confine.State) { runnerAvailable = old }(runnerAvailable)
	runnerAvailable = func() confine.State {
		return confine.State{Mechanism: "landlock+seccomp", Reason: "the container engine blocks Landlock (Linux 6.8.0 has it): use an engine whose default seccomp profile allows the landlock calls (INSTALL.md, \"Agent confinement\")"}
	}
	var out strings.Builder
	if !confinementDoctorLines(context.Background(), &out) {
		t.Fatal("doctor passed a host that cannot confine its agent runners")
	}
	for _, want := range []string{"kernel security modules: ", "agent confinement: off. the container engine blocks Landlock", "so agent turns are off"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor does not say %q:\n%s", want, out.String())
		}
	}
	runnerAvailable = confine.RunnerAvailable
	out.Reset()
	if st := confine.RunnerAvailable(); st.Confined && (confinementDoctorLines(context.Background(), &out) || !strings.Contains(out.String(), "agent confinement: landlock+seccomp ABI")) {
		t.Errorf("doctor on this host:\n%s", out.String())
	}
}
