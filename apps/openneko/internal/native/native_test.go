//go:build unix

package native

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := m[k]; dup {
			m[k] = "DUPLICATE"
			continue
		}
		m[k] = v
	}
	return m
}

func TestProcsEnvironment(t *testing.T) {
	l := Layout{Root: "/srv/on"}
	o := Options{Exe: "/opt/openneko/bin/openneko", WebDir: "/opt/openneko/share/openneko/web", WebAddr: "0.0.0.0:3000",
		PublicURL: "http://localhost:3000", Env: []string{"PATH=/bin", "OPENNEKO_WEB_ADDR=1.2.3.4:9", "OPENNEKO_IN_CONTAINER=1", "OPENNEKO_METRICS_REFRESH=on"}}
	procs := Procs(l, o)
	if len(procs) != 2 || procs[0].Name != "serve" || procs[1].Name != "web" {
		t.Fatalf("procs %+v", procs)
	}
	serve, web := envMap(procs[0].Env), envMap(procs[1].Env)
	for k, want := range map[string]string{
		"PATH": "/bin", "OPENNEKO_SERVE_CREDENTIALS": "/srv/on/credentials", "OPENNEKO_SERVE_ADDR": ServeAddr,
		"OPENNEKO_GRAPHJIN_CONFIG": "/srv/on/graphjin/agentic.yml", "OPENNEKO_SERVE_SOCKET": "/srv/on/run/serve.sock",
		"OPENNEKO_IN_CONTAINER": "false", "OPENNEKO_FILES_DIR": "/srv/on/files",
	} {
		if serve[k] != want {
			t.Errorf("serve %s=%q, want %q", k, serve[k], want)
		}
	}
	for k, want := range map[string]string{
		"NODE_ENV": "production", "OPENNEKO_WEB_ADDR": "0.0.0.0:3000", "OPENNEKO_WEB_DIR": o.WebDir,
		"OPENNEKO_SERVE_URL": "http://" + ServeAddr, "OPENNEKO_GRAPHJIN_URL": "http://" + SubscriptionsAddr,
		"WORKER_ADMIN_URL": "unix:///srv/on/run/worker.sock", "OPENNEKO_METRICS_REFRESH": "on", "OPENNEKO_IN_CONTAINER": "false",
		"OPENNEKO_AGENT_RUNS_DIR": "/srv/on/agent-runs",
	} {
		if web[k] != want {
			t.Errorf("web %s=%q, want %q", k, web[k], want)
		}
	}
	if _, ok := envMap(Procs(l, Options{Exe: "x"})[1].Env)["OPENNEKO_METRICS_REFRESH"]; !ok {
		t.Error("the metric refresh must default to off")
	}
	steps := Steps(l, o, false)
	names := []string{}
	for _, s := range steps {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"database check", "serve-init"}) {
		t.Errorf("steps without migrate: %v", names)
	}
	if got := Steps(l, o, true)[1].Args; !slices.Equal(got, []string{o.Exe, "migrate"}) {
		t.Errorf("migrate step %v", got)
	}
}

func TestProbeHost(t *testing.T) {
	for in, want := range map[string]string{"0.0.0.0:3000": "127.0.0.1:3000", "10.0.0.5:80": "10.0.0.5:80", ":3000": "127.0.0.1:3000"} {
		if got, err := ProbeHost(in); err != nil || got != want {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
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

func TestSupervisorRestartsAndStops(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	if err := l.Prepare(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "term")
	s := &Supervisor{Layout: l, WebAddr: "127.0.0.1:1", Log: &syncBuffer{}, Grace: 5 * time.Second, Backoff: 20 * time.Millisecond, Procs: []Proc{
		{Name: "serve", Args: []string{"/bin/sh", "-c", `trap 'echo done > ` + marker + `; exit 0' TERM; echo up; while :; do sleep 0.05; done`}},
		{Name: "web", Args: []string{"/bin/sh", "-c", "echo once; exit 3"}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	var st State
	deadline := time.Now().Add(10 * time.Second)
	for {
		var err error
		st, err = ReadState(l)
		if err == nil && st.Procs["serve"].PID > 0 && st.Procs["web"].Restarts >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %+v %v", st, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.PID != os.Getpid() || !Alive(st.Procs["serve"].PID) || st.Procs["web"].LastExit != "exit status 3" {
		t.Errorf("state %+v, web %+v", st, st.Procs["web"])
	}
	if pid, ok := Running(l); !ok || pid != os.Getpid() {
		t.Errorf("Running = %d %v", pid, ok)
	}
	if _, err := Lock(l); err == nil {
		t.Error("a second supervisor must not take the lock")
	}
	servePID := st.Procs["serve"].PID
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor did not stop")
	}
	if raw, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(raw)) != "done" {
		t.Errorf("serve did not get SIGTERM: %q %v", raw, err)
	}
	if Alive(servePID) {
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
	l := Layout{Root: t.TempDir()}
	if err := l.Prepare(); err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{Layout: l, Log: &syncBuffer{}, Grace: 200 * time.Millisecond, Procs: []Proc{
		{Name: "serve", Args: []string{"/bin/sh", "-c", "trap '' TERM; while :; do sleep 0.05; done"}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, err := ReadState(l); err == nil && st.Procs["serve"].PID > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("serve did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor did not kill serve")
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Errorf("stopped after %s, before the grace", d)
	}
}
