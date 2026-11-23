package serve

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

const hiddenTasksEnv = "SERVE_TEST_HIDDEN_TASKS_PROC"

// TestRunnerTaskCapCountsTasksProcDoesNotShow: in a container /proc shows
// only the container's tasks while the kernel counts every task of the uid.
// The runner's cap must leave its room above the kernel's count.
func TestRunnerTaskCapCountsTasksProcDoesNotShow(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the kernel does not apply RLIMIT_NPROC to root")
	}
	release := make(chan struct{})
	started := make(chan struct{})
	const held = 200
	for range held {
		go func() {
			runtime.LockOSThread()
			started <- struct{}{}
			<-release
		}()
	}
	for range held {
		<-started
	}
	defer close(release)

	proc := t.TempDir()
	if err := os.Mkdir(filepath.Join(proc, "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	status := fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d\nThreads:\t1\n", os.Getuid(), os.Getuid(), os.Getuid(), os.Getuid())
	if err := os.WriteFile(filepath.Join(proc, "1", "status"), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunnerTaskCapChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), hiddenTasksEnv+"="+proc)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "started 16 of 16") {
		t.Fatalf("with %d tasks of this account hidden from /proc, the capped runner could not start a process: %v\n%s", held, err, out)
	}
}

func TestRunnerTaskCapChild(t *testing.T) {
	proc := os.Getenv(hiddenTasksEnv)
	if proc == "" {
		t.Skip("runs as the child of TestRunnerTaskCapCountsTasksProcDoesNotShow")
	}
	procRoot = proc
	all := ^uint64(0)
	if err := limitRunner(RunnerLimits{CPUSeconds: all, AddressSpace: all, FileSize: all, OpenFiles: all, Threads: 256, AccountTasks: 64}); err != nil {
		t.Fatal(err)
	}
	started := 0
	for range 16 {
		if _, err := syscall.ForkExec("", nil, nil); !errors.Is(err, syscall.EAGAIN) {
			started++
		}
	}
	t.Logf("started %d of 16", started)
}

func TestKernelTasksIsAtLeastWhatProcShows(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the kernel does not apply RLIMIT_NPROC to root")
	}
	before, err := accountTasks(os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	got, err := kernelTasks(1)
	if err != nil {
		t.Fatal(err)
	}
	after, err := accountTasks(os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	seen := min(before, after)
	if got+8 < seen {
		t.Fatalf("the kernel's count %d is below the %d tasks /proc shows", got, seen)
	}
}
