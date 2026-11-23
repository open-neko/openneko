//go:build linux

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func openSession(t *testing.T, control *testClient, e egressBody) string {
	t.Helper()
	resp, err := control.do(context.Background(), http.MethodPost, "/v2/runs/"+control.run, e)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var answer struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&answer) != nil || answer.Token == "" {
		t.Fatalf("open run: %d", resp.StatusCode)
	}
	return answer.Token
}

func status(t *testing.T, c *testClient, method, path string, body any) int {
	t.Helper()
	resp, err := c.do(context.Background(), method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestARunTokenCannotChooseTheEgress(t *testing.T) {
	for _, key := range []string{"control-side-" + strings.Repeat("k", 32)} {
		t.Run("keyed", func(t *testing.T) {
			server, socket := startSandboxWith(t, false, func(s *Server) { s.Key = key })
			control := newClient(socket)
			control.key = key
			grant := egressBody{Org: "org-1", Label: "work-run-1", Hosts: []string{"allowed.example"}}
			token := openSession(t, control, grant)

			runner := newClient(socket)
			runner.run, runner.key = control.run, token
			if _, err := runner.send(context.Background(), execBody{Command: "true", Egress: &egressBody{Org: "org-2", Label: "x", Hosts: []string{"evil.example"}}}); err == nil || !strings.HasPrefix(err.Error(), "403") {
				t.Fatalf("a run token chose its own egress list: %v", err)
			}
			if _, err := runner.Exec(context.Background(), "true"); err != nil {
				t.Fatal(err)
			}
			server.mu.Lock()
			got := server.runs[control.run].egress.grant
			server.mu.Unlock()
			if got.Org != "org-1" || got.Label != "work-run-1" || !slices.Equal(got.Hosts, []string{"allowed.example"}) {
				t.Fatalf("the run's grant = %+v, want the control side's", got)
			}

			other := newClient(socket)
			other.key = token
			for _, c := range []struct {
				method, path string
				body         any
			}{
				{http.MethodPost, "/v2/runs/" + other.run + "/exec", execBody{Fresh: true, Command: "true", TimeoutSeconds: 10, MaxOutput: 10_000}},
				{http.MethodPost, "/v2/runs/" + other.run, grant},
				{http.MethodDelete, "/v2/runs/" + other.run, nil},
				{http.MethodGet, "/v2/check", nil},
			} {
				if code := status(t, other, c.method, c.path, c.body); code != http.StatusUnauthorized {
					t.Errorf("a run token reached %s %s: %d", c.method, c.path, code)
				}
			}
			if code := status(t, runner, http.MethodPost, "/v2/runs/"+control.run, grant); code != http.StatusUnauthorized {
				t.Errorf("a run token opened its own run again: %d", code)
			}
			wrong := newClient(socket)
			wrong.run, wrong.key = control.run, "not-the-token"
			if _, err := wrong.Exec(context.Background(), "true"); err == nil || !strings.HasPrefix(err.Error(), "401") {
				t.Errorf("a wrong token ran a command: %v", err)
			}
			if code := status(t, control, http.MethodPost, "/v2/runs/"+control.run, grant); code != http.StatusConflict {
				t.Errorf("the control side opened a run twice: %d", code)
			}
			if err := runner.Close(); err != nil {
				t.Fatal(err)
			}
			runner.started = false
			if _, err := runner.Exec(context.Background(), "true"); err == nil || !strings.HasPrefix(err.Error(), "401") {
				t.Errorf("the token outlived its run: %v", err)
			}
		})
	}
}

// TestOnlyTheKeyIsTheControlSide: a sandbox with no key does not start, and
// a request with no Authorization opens no run.
func TestOnlyTheKeyIsTheControlSide(t *testing.T) {
	if l, err := (&Server{}).listen("unix://" + filepath.Join(shortSocketDir(t), "s.sock")); err == nil {
		l.Close()
		t.Fatal("a sandbox with no key listened")
	} else if !strings.Contains(err.Error(), "set OPENNEKO_SANDBOX_KEY") {
		t.Fatalf("the refusal = %v", err)
	}
	_, socket := startSandbox(t, false)
	for _, key := range []string{"", "not-the-key"} {
		c := newClient(socket)
		c.key = key
		if got := status(t, c, http.MethodPost, "/v2/runs/"+c.run, egressBody{Org: "org-1", Label: "x"}); got != http.StatusUnauthorized {
			t.Errorf("a request with key %q opened a run: status %d", key, got)
		}
	}
}
