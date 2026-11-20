//go:build unix

package native

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
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
		"OPENNEKO_ENV": "production", "OPENNEKO_WEB_ADDR": "0.0.0.0:3000", "OPENNEKO_WEB_DIR": o.WebDir,
		"OPENNEKO_SERVE_URL": "http://" + ServeAddr, "OPENNEKO_GRAPHJIN_URL": "http://" + SubscriptionsAddr,
		"OPENNEKO_METRICS_REFRESH": "on", "OPENNEKO_IN_CONTAINER": "false",
		"OPENNEKO_AGENT_RUNS_DIR": "/srv/on/agent-runs", "OPENNEKO_WEB_ADMIN_SOCKET": "/srv/on/web-admin.sock",
	} {
		if web[k] != want {
			t.Errorf("web %s=%q, want %q", k, web[k], want)
		}
	}
	if v, ok := web["WORKER_ADMIN_URL"]; ok {
		t.Errorf("web WORKER_ADMIN_URL=%q, want unset", v)
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
	l := Layout{Root: testsock.Dir(t)}
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
	l := Layout{Root: testsock.Dir(t)}
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

func TestServeGetsNoControlZoneSecret(t *testing.T) {
	secrets := []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "MODEL_API_KEY", "NEKO_EMBEDDING_API_KEY", "AZURE_OPENAI_KEY",
		"HF_TOKEN", "VERTEX_ACCESS_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"DATABASE_URL", "OPENNEKO_DATABASE_URL", "OPENNEKO_DATABASE_URL_FILE", "OPENNEKO_TEST_DATABASE_URL", "OPENNEKO_DATABASE_KEY",
		"PGPASSWORD", "PGPASSFILE", "NEKO_PG_PASSWORD", "NEKO_SECRET_KEY", "OPENNEKO_SESSION_SECRET", "OPENNEKO_BACKUP_KEY_FILE",
		"OPENNEKO_JOBS_WAKE_TOKEN", "OPENNEKO_GITHUB_TOKEN", "OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN", "openai_api_key"}
	env := []string{"PATH=/bin", "HOME=/home/u", "HTTPS_PROXY=http://proxy:3128", "OPENNEKO_SANDBOX_URL=unix:///s/sandbox.sock",
		"OPENNEKO_SANDBOX_KEY=box", "OPENNEKO_FILES_BUCKET=s3://b", "OPENNEKO_FILES_BUCKET_KEY_ID=id", "OPENNEKO_FILES_BUCKET_SECRET=bs",
		"GJ_DATABASE_PASSWORD=gj"}
	for _, name := range secrets {
		env = append(env, name+"=planted")
	}
	l := Layout{Root: "/srv/on"}
	procs := Procs(l, Options{Exe: "x", Env: env})
	serve, web := envMap(procs[0].Env), envMap(procs[1].Env)
	for _, name := range secrets {
		if _, ok := serve[name]; ok {
			t.Errorf("serve holds %s", name)
		}
		if web[name] != "planted" {
			t.Errorf("web lost %s", name)
		}
	}
	for _, kv := range procs[0].Env {
		if strings.Contains(kv, "planted") {
			t.Errorf("serve holds %q", kv)
		}
	}
	for _, kv := range env[:9] {
		k, v, _ := strings.Cut(kv, "=")
		if serve[k] != v {
			t.Errorf("serve %s=%q, want %q", k, serve[k], v)
		}
	}
	if serve["OPENNEKO_KEY_DOOR_SOCKET"] != "/srv/on/door/keydoor.sock" || serve["OPENNEKO_SERVE_CREDENTIALS"] != "/srv/on/credentials" {
		t.Errorf("serve lost its own paths: %v", serve)
	}
	for _, step := range Steps(l, Options{Exe: "x", Env: env}, true) {
		if envMap(step.Env)["OPENNEKO_DATABASE_URL"] != "planted" {
			t.Errorf("step %s lost the database URL", step.Name)
		}
	}
}

func TestPrepareRefusesASocketPathTooLongToListenOn(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	longest := filepath.Join("run", "config-check.sock")
	rootAt := func(n int) Layout {
		pad := n - len(base) - len(longest) - 2
		return Layout{Root: filepath.Join(base, strings.Repeat("r", pad))}
	}

	at := rootAt(MaxSocketPath())
	if err := at.Prepare(); err != nil {
		t.Fatalf("a path at the limit: %v", err)
	}
	for _, path := range at.sockets() {
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatalf("listen on %s (%d bytes): %v", path, len(path), err)
		}
		_ = ln.Close()
	}

	over := rootAt(MaxSocketPath() + 1)
	err = over.Prepare()
	want := fmt.Sprintf("config-check.sock has %d bytes", MaxSocketPath()+1)
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "shorter instance name") {
		t.Fatalf("a path one byte over the limit: %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("not one line: %q", err)
	}
	path := filepath.Join(over.Run(), "config-check.sock")
	if ln, lerr := net.Listen("unix", path); lerr == nil {
		_ = ln.Close()
		t.Fatalf("listen on %s (%d bytes) worked; the limit is too low", path, len(path))
	}
}
