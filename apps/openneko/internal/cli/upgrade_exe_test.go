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
	if _, err := os.Lstat(exe + ".new"); err == nil {
		t.Fatal("the staged executable stayed beside the new one")
	}
	if left, _ := filepath.Glob(exe + ".old*"); runtime.GOOS == "windows" && len(left) != 1 {
		t.Fatalf("want the running image moved aside while it runs, found %v", left)
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

func TestAnUpgradeCutBetweenTheTwoRenamesIsFinished(t *testing.T) {
	write := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return filepath.Join(dir, "openneko.exe")
	}
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"cut after the running image moved aside", map[string]string{"openneko.exe.new": "v2", "openneko.exe.old": "v1"}, "v2"},
		{"cut while the swap rolled back", map[string]string{"openneko.exe.old": "v0", "openneko.exe.old-1700000000000000000": "v1"}, "v1"},
		{"cut while the new image was copied", map[string]string{"openneko.exe": "v1", "openneko.exe.new": "v2 half"}, "v1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			exe := write(t, c.files)
			if err := finishExecutableSwap(exe); err != nil {
				t.Fatal(err)
			}
			if got := readFileOr(exe, ""); got != c.want {
				t.Fatalf("openneko.exe holds %q, want %q", got, c.want)
			}
			if _, err := os.Lstat(exe + ".new"); err == nil {
				t.Fatal("openneko.exe.new stayed")
			}
		})
	}
}
