package cli

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
	"github.com/open-neko/neko/apps/openneko/internal/instance"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
)

const treeEnv = "OPENNEKO_TEST_TREE"

func treeHelper(mode string) int {
	root := os.Getenv(treeEnv + "_ROOT")
	l := native.Layout{Root: root}
	writePID := func(name string) {
		_ = os.WriteFile(filepath.Join(root, name+".pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	switch mode {
	case "supervisor":
		ctx, stop := native.NotifyStop(context.Background())
		defer stop()
		proc := func(name string) native.Proc {
			return native.Proc{Name: name, Args: []string{os.Args[0], "-test.run=^$"}, Env: append(os.Environ(), treeEnv+"="+name)}
		}
		s := &native.Supervisor{Layout: l, WebAddr: os.Getenv(treeEnv + "_WEB"), Log: os.Stdout, Grace: 10 * time.Second,
			Backoff: 100 * time.Millisecond, Procs: []native.Proc{proc("serve"), proc("web")}}
		if err := s.Run(ctx); err != nil {
			os.Stdout.WriteString(err.Error() + "\n")
			return 1
		}
		return 0
	case "serve":
		ctx, stop := native.NotifyStop(context.Background())
		defer stop()
		native.FollowSupervisor()
		writePID("serve")
		if err := os.MkdirAll(filepath.Dir(l.GraphJin()), 0o700); err != nil {
			return 1
		}
		gj := &gjhost.Supervisor{ConfigFile: l.GraphJin(), Addr: "127.0.0.1:0", Log: io.Discard, Poll: 50 * time.Millisecond,
			MinBackoff: 100 * time.Millisecond, Command: func(sock string) *exec.Cmd {
				cmd := exec.Command(os.Args[0], "-test.run=^$")
				cmd.Env = append(os.Environ(), treeEnv+"=graphjin", treeEnv+"_SOCK="+sock)
				return cmd
			}}
		if err := gj.Run(ctx); err != nil {
			return 1
		}
		return 0
	case "graphjin":
		writePID("graphjin")
		ln, err := net.Listen("unix", os.Getenv(treeEnv+"_SOCK"))
		if err != nil {
			return 1
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	case "web":
		ctx, stop := native.NotifyStop(context.Background())
		defer stop()
		native.FollowSupervisor()
		writePID("web")
		<-ctx.Done()
		return 0
	}
	return 2
}

var statusPID = regexp.MustCompile(`(serve|web|graphjin): pid (\d+), up [^,]+, (\d+) restarts`)

type treeStatus struct {
	pid, restarts map[string]int
	out           string
}

func readTreeStatus(t *testing.T) treeStatus {
	t.Helper()
	var out bytes.Buffer
	_ = statusNative(context.Background(), &out)
	st := treeStatus{pid: map[string]int{}, restarts: map[string]int{}, out: out.String()}
	for _, m := range statusPID.FindAllStringSubmatch(st.out, -1) {
		st.pid[m[1]], _ = strconv.Atoi(m[2])
		st.restarts[m[1]], _ = strconv.Atoi(m[3])
	}
	return st
}

func waitTree(t *testing.T, what string, ok func(treeStatus) bool) treeStatus {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		st := readTreeStatus(t)
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not after 30s; status:\n%s", what, st.out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func pidFile(t *testing.T, root, name string) int {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(root, name+".pid"))
	pid, _ := strconv.Atoi(string(raw))
	return pid
}

func allRunning(st treeStatus) bool {
	return st.pid["serve"] > 0 && st.pid["web"] > 0 && st.pid["graphjin"] > 0
}

func startTree(t *testing.T, attr func() *exec.Cmd) (*exec.Cmd, native.Layout) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", testsock.Dir(t))
	t.Setenv(instance.EnvName, "")
	activeServer = nil
	l := nativeLayout()
	if err := l.Prepare(); err != nil {
		t.Fatal(err)
	}
	cmd := attr()
	cmd.Env = append(os.Environ(), treeEnv+"=supervisor", treeEnv+"_ROOT="+l.Root, treeEnv+"_WEB=127.0.0.1:1")
	cmd.Stdout, cmd.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd, l
}

func gone(t *testing.T, what string, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for _, pid := range pids {
		for native.Alive(pid) {
			if time.Now().After(deadline) {
				for _, p := range pids {
					if proc, err := os.FindProcess(p); err == nil {
						_ = proc.Kill()
					}
				}
				t.Fatalf("%s: pid %d still runs", what, pid)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestStatusRestartAndStopFollowTheWholeTree(t *testing.T) {
	sup, l := startTree(t, func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.SysProcAttr = native.Detach()
		return cmd
	})
	first := waitTree(t, "status shows serve, web and graphjin running", allRunning)
	for _, name := range []string{"serve", "web", "graphjin"} {
		if pid := pidFile(t, l.Root, name); first.pid[name] != pid || !native.Alive(pid) {
			t.Fatalf("status gives %s pid %d, the process is pid %d (alive %v)\n%s", name, first.pid[name], pid, native.Alive(pid), first.out)
		}
	}
	if !strings.Contains(first.out, "supervisor: pid "+strconv.Itoa(sup.Process.Pid)) {
		t.Errorf("status names another supervisor:\n%s", first.out)
	}

	serve, err := os.FindProcess(first.pid["serve"])
	if err != nil {
		t.Fatal(err)
	}
	if err := serve.Kill(); err != nil {
		t.Fatal(err)
	}
	second := waitTree(t, "the supervisor starts serve and serve starts graphjin again", func(st treeStatus) bool {
		return allRunning(st) && st.pid["serve"] != first.pid["serve"] && st.pid["graphjin"] != first.pid["graphjin"] && st.restarts["serve"] == 1
	})
	if second.pid["web"] != first.pid["web"] || second.restarts["web"] != 0 {
		t.Errorf("web changed when serve was killed:\n%s", second.out)
	}
	if pid := pidFile(t, l.Root, "serve"); second.pid["serve"] != pid {
		t.Errorf("status gives serve pid %d, the new serve is pid %d", second.pid["serve"], pid)
	}
	gone(t, "the killed serve and its graphjin", first.pid["serve"], first.pid["graphjin"])

	var out bytes.Buffer
	if err := stopNative(context.Background(), &out); err != nil || !strings.Contains(out.String(), "OpenNeko stopped.") {
		t.Fatalf("stop: %v %s", err, out.String())
	}
	_ = sup.Wait()
	gone(t, "after stop", sup.Process.Pid, second.pid["serve"], second.pid["web"], second.pid["graphjin"])
	if st := readTreeStatus(t); !strings.Contains(st.out, "supervisor: not running") {
		t.Errorf("status after stop:\n%s", st.out)
	}
}

func TestKillingTheSupervisorEndsTheWholeTree(t *testing.T) {
	sup, _ := startTree(t, func() *exec.Cmd { return exec.Command(os.Args[0], "-test.run=^$") })
	st := waitTree(t, "status shows serve, web and graphjin running", allRunning)
	if err := sup.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = sup.Wait()
	gone(t, "after the supervisor was killed", st.pid["serve"], st.pid["web"], st.pid["graphjin"])
}
