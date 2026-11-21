package sandbox

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestClientKeepsTheWorkspaceInStepWithNoChmodByPath runs the sync under a
// filter that refuses chmod by path, as the agent runner's filter does.
func TestClientKeepsTheWorkspaceInStepWithNoChmodByPath(t *testing.T) {
	if os.Getenv("OPENNEKO_TEST_NO_CHMOD_CHILD") != "" {
		refuseChmodByPath(t)
		keepsTheWorkspaceInStep(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestClientKeepsTheWorkspaceInStepWithNoChmodByPath$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "OPENNEKO_TEST_NO_CHMOD_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS") {
		t.Fatalf("the sync needs chmod by path: %v\n%s", err, out)
	}
}

func refuseChmodByPath(t *testing.T) {
	t.Helper()
	eperm := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	prog := []unix.SockFilter{{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}}
	for _, nr := range []uint32{unix.SYS_FCHMODAT, unix.SYS_FCHMODAT2} {
		prog = append(prog, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: nr, Jf: 1},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: eperm})
	}
	prog = append(prog, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	fp := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	if _, _, e := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&fp))); e != 0 {
		t.Fatalf("install the filter: %v", e)
	}
	if err := os.Chmod(t.TempDir(), 0o700); err == nil {
		t.Fatal("the filter let chmod by path through")
	}
}
