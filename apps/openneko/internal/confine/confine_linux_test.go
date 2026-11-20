package confine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestRunnerFilterRefusesTheListedCalls(t *testing.T) {
	prog, err := runnerFilter(4242)
	if err != nil {
		t.Fatal(err)
	}
	for _, nr := range append(append([]uintptr{}, refusedCalls...), unix.SYS_SOCKET, unix.SYS_CONNECT, unix.SYS_TRUNCATE, unix.SYS_KILL, unix.SYS_PTRACE) {
		found := false
		for i, ins := range prog {
			if ins.Code == jeq && ins.K == uint32(nr) && i+1 < len(prog) && prog[i+1].Code == ret && prog[i+1].K == retEPERM {
				found = true
			}
		}
		if !found {
			t.Errorf("the filter does not refuse call %d", nr)
		}
	}
	if len(prog) > 255 {
		t.Errorf("the filter has %d instructions; a jump cannot pass more than 255", len(prog))
	}
}
