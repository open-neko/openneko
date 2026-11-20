package native

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
)

const helperEnv = "OPENNEKO_NATIVE_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(runHelper(mode, os.Getenv(helperEnv+"_ARG")))
	}
	os.Exit(m.Run())
}

func runHelper(mode, arg string) int {
	switch mode {
	case "graceful":
		ctx, stop := NotifyStop(context.Background())
		defer stop()
		fmt.Println("up")
		<-ctx.Done()
		_ = os.WriteFile(arg, []byte("done"), 0o600)
		return 0
	case "once":
		fmt.Println("once")
		return 3
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
		for {
			time.Sleep(50 * time.Millisecond)
		}
	case "supervisor":
		ctx, stop := NotifyStop(context.Background())
		defer stop()
		child := helperProc("serve", os.Getenv(helperEnv+"_CHILD"), os.Getenv(helperEnv+"_MARKER"))
		s := &Supervisor{Layout: Layout{Root: arg}, Log: os.Stdout, Grace: 10 * time.Second, Procs: []Proc{child}}
		if err := s.Run(ctx); err != nil {
			fmt.Println(err)
			return 1
		}
		return 0
	case "detached":
		_ = os.WriteFile(arg, []byte(detachedState()), 0o600)
		return 0
	}
	return 2
}

func helperEnviron(mode, arg string, extra ...string) []string {
	return append(append(os.Environ(), helperEnv+"="+mode, helperEnv+"_ARG="+arg), extra...)
}

func helperProc(name, mode, arg string) Proc {
	return Proc{Name: name, Args: []string{os.Args[0], "-test.run=^$"}, Env: helperEnviron(mode, arg)}
}

func preparedLayout(t *testing.T) Layout {
	t.Helper()
	l := Layout{Root: testsock.Dir(t)}
	if err := l.Prepare(); err != nil {
		t.Fatal(err)
	}
	return l
}

func waitFor(t *testing.T, what string, wait time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not after %s", what, wait)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func servePID(l Layout) int {
	st, err := ReadState(l)
	if err != nil || st.Procs["serve"] == nil {
		return 0
	}
	return st.Procs["serve"].PID
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func startSupervisor(t *testing.T, l Layout, child, marker string, attr *syscall.SysProcAttr) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = helperEnviron("supervisor", l.Root, helperEnv+"_CHILD="+child, helperEnv+"_MARKER="+marker)
	cmd.Stdout, cmd.Stderr = &syncBuffer{}, &syncBuffer{}
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestSupervisorRestartsAndStops(t *testing.T) {
	l := preparedLayout(t)
	marker := filepath.Join(t.TempDir(), "term")
	s := &Supervisor{Layout: l, WebAddr: "127.0.0.1:1", Log: &syncBuffer{}, Grace: 5 * time.Second, Backoff: 20 * time.Millisecond, Procs: []Proc{
		helperProc("serve", "graceful", marker), helperProc("web", "once", ""),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	var st State
	waitFor(t, "serve runs and web starts again twice", 20*time.Second, func() bool {
		var err error
		st, err = ReadState(l)
		return err == nil && st.Procs["serve"].PID > 0 && st.Procs["web"].Restarts >= 2
	})
	if st.PID != os.Getpid() || !Alive(st.Procs["serve"].PID) || st.Procs["web"].LastExit != "exit status 3" {
		t.Errorf("state %+v, web %+v", st, st.Procs["web"])
	}
	if pid, ok := Running(l); !ok || pid != os.Getpid() {
		t.Errorf("Running = %d %v", pid, ok)
	}
	if _, err := Lock(l); err == nil {
		t.Error("a second supervisor must not take the lock")
	}
	waitFor(t, "serve is up", 10*time.Second, func() bool {
		raw, _ := os.ReadFile(l.Log("serve"))
		return strings.Contains(string(raw), "up")
	})
	serve := st.Procs["serve"].PID
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor did not stop")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "done" {
		t.Errorf("serve did not get the stop request: %q %v", raw, err)
	}
	if Alive(serve) {
		t.Error("serve still runs")
	}
	if Locked(l) {
		t.Error("the lock is still held")
	}
	if _, err := os.Stat(l.State()); !os.IsNotExist(err) {
		t.Errorf("the state file stays: %v", err)
	}
	logs, _ := os.ReadFile(l.Log("web"))
	if strings.Count(string(logs), "once") < 2 {
		t.Errorf("web log %q", logs)
	}
	if err := Stop(l, time.Second); err != ErrNotRunning {
		t.Errorf("Stop with no supervisor: %v", err)
	}
}

func TestSupervisorKillsAfterGrace(t *testing.T) {
	l := preparedLayout(t)
	s := &Supervisor{Layout: l, Log: &syncBuffer{}, Grace: 200 * time.Millisecond, Procs: []Proc{helperProc("serve", "stubborn", "")}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	waitFor(t, "serve starts", 10*time.Second, func() bool { return servePID(l) > 0 })
	serve := servePID(l)
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor did not kill serve")
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Errorf("stopped after %s, before the grace", d)
	}
	if Alive(serve) {
		t.Error("serve still runs")
	}
}

func TestStopEndsADetachedSupervisorAndItsChildrenGracefully(t *testing.T) {
	l := preparedLayout(t)
	marker := filepath.Join(t.TempDir(), "term")
	cmd := startSupervisor(t, l, "graceful", marker, Detach())
	waitFor(t, "serve starts", 20*time.Second, func() bool { return servePID(l) > 0 })
	serve := servePID(l)
	waitFor(t, "serve is up", 10*time.Second, func() bool {
		raw, _ := os.ReadFile(l.Log("serve"))
		return strings.Contains(string(raw), "up")
	})
	if pid, ok := Running(l); !ok || pid != cmd.Process.Pid {
		t.Fatalf("Running = %d %v, want %d", pid, ok, cmd.Process.Pid)
	}
	if err := Stop(l, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("the supervisor did not end cleanly: %v; %s", err, cmd.Stdout.(*syncBuffer).b.String())
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "done" {
		t.Errorf("serve did not stop gracefully: %q %v", raw, err)
	}
	if Alive(serve) {
		t.Error("serve still runs")
	}
}

func TestChildrenDieWhenTheSupervisorIsKilled(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS has no parent-death signal; WIN0 item A6 adds a watch")
	}
	l := preparedLayout(t)
	cmd := startSupervisor(t, l, "graceful", filepath.Join(t.TempDir(), "term"), nil)
	waitFor(t, "serve starts", 20*time.Second, func() bool { return servePID(l) > 0 })
	serve := servePID(l)
	waitFor(t, "serve runs", 5*time.Second, func() bool { return Alive(serve) })
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waitFor(t, "serve ends with its supervisor", 10*time.Second, func() bool { return !Alive(serve) })
}

func TestAliveSeesAnEndedProcess(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("this process is not alive")
	}
	if Alive(0) || Alive(-1) {
		t.Error("pid 0 or -1 is alive")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = helperEnviron("once", "")
	_ = cmd.Run()
	if Alive(cmd.Process.Pid) {
		t.Errorf("pid %d ended but is alive", cmd.Process.Pid)
	}
}

func TestDetachStartsOutsideTheCallersSession(t *testing.T) {
	withConsole(t)
	out := filepath.Join(t.TempDir(), "state")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = helperEnviron("detached", out)
	cmd.SysProcAttr = Detach()
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(out); err != nil || string(raw) != "detached" {
		t.Errorf("the child is %q %v, want detached", raw, err)
	}
}
