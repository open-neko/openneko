//go:build unix

package native

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
)

const roleEnv = "OPENNEKO_NATIVE_TEST_ROLE"

func TestHelperProcess(t *testing.T) {
	switch os.Getenv(roleEnv) {
	case "child":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		FollowSupervisor()
		if _, ok := os.LookupEnv(SupervisorFDEnv); ok {
			os.Exit(4)
		}
		<-ctx.Done()
		_ = os.WriteFile(os.Getenv("MARKER"), []byte("term"), 0o600)
		os.Exit(0)
	case "supervisor":
		if os.Getenv("FORCE_PIPE") == "1" {
			parentWatch = true
		}
		l := Layout{Root: os.Getenv("ROOT")}
		env := append(os.Environ(), roleEnv+"=child")
		s := &Supervisor{Layout: l, Log: os.Stderr, Procs: []Proc{
			{Name: "serve", Args: []string{os.Args[0], "-test.run=^TestHelperProcess$"}, Env: env},
		}}
		_ = s.Run(context.Background())
		os.Exit(0)
	}
}

func TestChildrenStopWhenTheSupervisorDies(t *testing.T) {
	modes := map[string]string{"pipe": "1", "default": "0"}
	for name, force := range modes {
		t.Run(name, func(t *testing.T) {
			l := Layout{Root: testsock.Dir(t)}
			if err := l.Prepare(); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(l.Root, "marker")
			sup := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
			sup.Env = append(os.Environ(), roleEnv+"=supervisor", "ROOT="+l.Root, "MARKER="+marker, "FORCE_PIPE="+force)
			if err := sup.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sup.Process.Kill(); _ = sup.Wait() })
			var child int
			deadline := time.Now().Add(10 * time.Second)
			for child == 0 {
				if st, err := ReadState(l); err == nil && st.Procs["serve"] != nil {
					child = st.Procs["serve"].PID
				}
				if time.Now().After(deadline) {
					t.Fatal("the child did not start")
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
			if err := sup.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			_ = sup.Wait()
			deadline = time.Now().Add(10 * time.Second)
			for {
				raw, _ := os.ReadFile(marker)
				if strings.TrimSpace(string(raw)) == "term" && !Alive(child) {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("the child (pid %d) did not stop after the supervisor died: marker %q, alive %v", child, raw, Alive(child))
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}
