//go:build unix

package native

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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

func TestPrepareRefusesASocketPathTooLongToListenOn(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	longest := ""
	for _, p := range (Layout{Root: "R"}).sockets() {
		if rel := strings.TrimPrefix(p, "R"+string(filepath.Separator)); len(rel) > len(longest) {
			longest = rel
		}
	}
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
	want := fmt.Sprintf("%s has %d bytes", filepath.Base(longest), MaxSocketPath()+1)
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "shorter instance name") {
		t.Fatalf("a path one byte over the limit: %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("not one line: %q", err)
	}
	path := filepath.Join(over.Root, longest)
	if ln, lerr := net.Listen("unix", path); lerr == nil {
		_ = ln.Close()
		t.Fatalf("listen on %s (%d bytes) worked; the limit is too low", path, len(path))
	}
}
