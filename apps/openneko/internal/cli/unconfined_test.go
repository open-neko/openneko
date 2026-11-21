package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

const unconfinedOnLine = "agent turns run WITHOUT OS confinement: host user neko allowed it on box-1 (Linux 4.18.0, 2026-11-21T10:00:00Z): no Landlock. The agent can read the files and reach the network of the OpenNeko account"

func fakeUnconfinedSetting(t *testing.T, on *bool) {
	t.Helper()
	socket := filepath.Join(testsock.Dir(t), "admin.sock")
	stop, err := serveWebAdmin(socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/agent-unconfined" {
			http.NotFound(w, r)
			return
		}
		if *on {
			_, _ = w.Write([]byte(`{"on":true,"line":"` + unconfinedOnLine + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"on":false}`))
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	t.Setenv(webAdminSocketEnv, socket)
}

func TestReleaseCheckPassesUnconfinedTurnsOnlyWithTheFlag(t *testing.T) {
	t.Setenv(serveConfineEnv, "")
	notBuilt := confine.State{Reason: "confinement on windows is not built yet"}
	stubConfinement(t, notBuilt, notBuilt)
	t.Setenv("OPENNEKO_ENV", "production")
	cleanServeEnv(t)
	on := false
	fakeUnconfinedSetting(t, &on)
	sock := filepath.Join(testsock.Dir(t), "serve.sock")
	t.Setenv("OPENNEKO_SERVE_SOCKET", sock)
	ctx, stop := context.WithCancel(context.Background())
	done, err := serveAgentTurns(ctx, io.Discard, sock, 1, turnHostStart{sandbox: sandbox.Config{}}, notBuilt, notBuilt)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { stop(); <-done }()
	run := func(args ...string) (string, error) {
		cmd := newReleaseCheckCmd()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--serve-socket"}, args...))
		err := cmd.Execute()
		return out.String(), err
	}
	if out, err := run("--allow-unconfined"); err == nil || !strings.Contains(err.Error(), confine.NotAvailable) {
		t.Fatalf("setting off, with the flag: %q %v", out, err)
	}
	on = true
	if out, err := run(); err == nil || !strings.Contains(err.Error(), "pass --allow-unconfined") || strings.Contains(out, "serve_socket=ok") {
		t.Fatalf("setting on, no flag: %q %v", out, err)
	}
	out, err := run("--allow-unconfined")
	if err != nil || !strings.Contains(out, "unconfined_turns=on ("+unconfinedOnLine+")") {
		t.Fatalf("setting on, with the flag: %q %v", out, err)
	}
}

func TestDoctorShowsUnconfinedTurnsAsAFailure(t *testing.T) {
	t.Setenv(serveConfineEnv, "")
	notBuilt := confine.State{Reason: "confinement on windows is not built yet"}
	stubConfinement(t, notBuilt, notBuilt)
	t.Setenv("OPENNEKO_ENV", "production")
	on := false
	fakeUnconfinedSetting(t, &on)
	var out strings.Builder
	if !confinementDoctorLines(context.Background(), &out) || !strings.Contains(out.String(), unconfinedHint) {
		t.Fatalf("setting off:\n%s", out.String())
	}
	on = true
	out.Reset()
	if !confinementDoctorLines(context.Background(), &out) || !strings.Contains(out.String(), "agent confinement: FAIL, "+unconfinedOnLine) {
		t.Fatalf("setting on:\n%s", out.String())
	}
	t.Setenv(webAdminSocketEnv, filepath.Join(testsock.Dir(t), "none.sock"))
	out.Reset()
	if !confinementDoctorLines(context.Background(), &out) || !strings.Contains(out.String(), "cannot be read") {
		t.Fatalf("no web:\n%s", out.String())
	}
}

func TestConfinementCommandSendsTheHostUserToTheAdminSocket(t *testing.T) {
	var bodies []map[string]any
	socket := filepath.Join(testsock.Dir(t), "admin.sock")
	stop, err := serveWebAdmin(socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/confinement/unconfined" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"setting":{"on":true,"host":"box-1","kernel":"Linux 4.18.0","hostUser":"neko"},"applies":true}`))
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	t.Setenv(webAdminSocketEnv, socket)
	run := func(args ...string) (string, error) {
		cmd := NewRoot()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"confinement", "--local"}, args...))
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := run("allow-unconfined"); err == nil || len(bodies) != 0 {
		t.Fatalf("allow with no reason: %v, %d requests", err, len(bodies))
	}
	out, err := run("allow-unconfined", "--reason", "no Landlock on this kernel")
	if err != nil || !strings.Contains(out, "allowed on box-1 (Linux 4.18.0)") {
		t.Fatalf("allow: %q %v", out, err)
	}
	if _, err := run("deny"); err != nil {
		t.Fatal(err)
	}
	user := currentHostUser()
	if len(bodies) != 2 || bodies[0]["on"] != true || bodies[0]["reason"] != "no Landlock on this kernel" || bodies[0]["hostUser"] != user ||
		bodies[1]["on"] != false || bodies[1]["hostUser"] != user {
		t.Fatalf("requests: %v (host user %q)", bodies, user)
	}
}
