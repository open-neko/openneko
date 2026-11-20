package cli

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestHelperRunsUntilKilled(t *testing.T) {
	if os.Getenv("OPENNEKO_TEST_RUN_UNTIL_KILLED") != "1" {
		t.Skip("helper process for TestReplaceFileOverARunningExecutable")
	}
	os.Stdout.WriteString("ready\n")
	time.Sleep(2 * time.Minute)
}

func TestReplaceFileOverARunningExecutable(t *testing.T) {
	defer func(v bool) { moveExecutableAside = v }(moveExecutableAside)
	moveExecutableAside = true
	dir := t.TempDir()
	exe := filepath.Join(dir, "openneko.exe")
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, self, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestHelperRunsUntilKilled$")
	cmd.Env = append(os.Environ(), "OPENNEKO_TEST_RUN_UNTIL_KILLED=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		bufio.NewReader(stdout).ReadString('\n')
		exited <- struct{}{}
		_ = cmd.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the running copy did not start")
	}
	defer func() { _ = cmd.Process.Kill(); <-exited }()

	if runtime.GOOS == "windows" {
		other := filepath.Join(dir, "other")
		if err := os.WriteFile(other, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(other, exe); err == nil {
			t.Fatal("control: Windows let a rename replace the image of a running process, so this test proves nothing")
		}
	}
	next := filepath.Join(dir, "next")
	if err := os.WriteFile(next, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(next, exe); err != nil {
		t.Fatalf("replace the running executable: %v", err)
	}
	if got := readFileOr(exe, ""); got != "v2" {
		t.Fatalf("the executable holds %q, want the new release", got)
	}
	select {
	case <-exited:
		t.Fatal("the running process ended during the upgrade")
	default:
	}
	if left, _ := filepath.Glob(exe + ".old*"); runtime.GOOS == "windows" && len(left) != 1 {
		t.Fatalf("want the running image moved aside, found %v", left)
	}

	_ = cmd.Process.Kill()
	<-exited
	removeAsideExecutables(exe)
	if left, _ := filepath.Glob(exe + ".old*"); len(left) != 0 {
		t.Fatalf("aside files left after the old process ended: %v", left)
	}
	if got := readFileOr(exe, ""); got != "v2" {
		t.Fatalf("the clean-up touched the new executable: %q", got)
	}
}
