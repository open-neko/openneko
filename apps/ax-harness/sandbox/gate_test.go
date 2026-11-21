package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type sessionBox struct {
	mu     sync.Mutex
	opens  []string
	execs  []string
	tokens int
	live   string
}

func (b *sessionBox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
		b.execs = append(b.execs, string(raw))
		if r.Header.Get("Authorization") != "Bearer "+b.live {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 0, "output": "ok\n"})
	case r.Method == http.MethodPost:
		b.opens = append(b.opens, r.Header.Get("Authorization")+" "+string(bytes.TrimSpace(raw)))
		b.tokens++
		b.live = fmt.Sprintf("tok-%d", b.tokens)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": b.live})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestAFixedRunSendsNoEgressAndSurvivesASandboxRestart(t *testing.T) {
	box := &sessionBox{}
	server := httptest.NewServer(box)
	defer server.Close()
	egress := Egress{Org: "org-1", Label: "work-run-1", Hosts: []string{"allowed.example"}}
	gate, err := NewGate(Config{URL: server.URL, Key: "control-key"}, egress)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	dial := func(context.Context) (net.Conn, error) {
		runnerEnd, gateEnd := net.Pipe()
		gate.Serve(gateEnd)
		return runnerEnd, nil
	}
	client, err := NewClient(Config{URL: "http://sandbox", Run: gate.Run(), Dial: dial}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetEgress(Egress{Org: "org-evil", Hosts: []string{"evil.example"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if result, err := client.Exec(context.Background(), "true", "", nil, 10, 10_000); err != nil || result.Output != "ok\n" {
			t.Fatalf("exec %d: %+v, %v", i, result, err)
		}
		box.mu.Lock()
		box.live = ""
		box.mu.Unlock()
	}
	box.mu.Lock()
	defer box.mu.Unlock()
	want := `Bearer control-key {"org":"org-1","label":"work-run-1","hosts":["allowed.example"]}`
	if len(box.opens) != 2 || box.opens[0] != want || box.opens[1] != want {
		t.Fatalf("the gate opened the run as %q", box.opens)
	}
	if len(box.execs) != 3 {
		t.Fatalf("the sandbox got %d commands, want 3 (one lost and sent again)", len(box.execs))
	}
	for _, body := range box.execs {
		if strings.Contains(body, `"egress"`) || strings.Contains(body, "evil.example") {
			t.Fatalf("a fixed run sent an egress list: %s", body)
		}
	}
}
