package gjhost

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func seedConfig(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "db", "graphjin", "customer.sources.example.yml"))
	if err != nil {
		t.Fatal(err)
	}
	conf := strings.NewReplacer(
		"REPLACE_WITH_BASE64_32_BYTE_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"REPLACE_WITH_PER_ORG_SECRET_B64", "c2VjcmV0LXNlY3JldC1zZWNyZXQtc2VjcmV0LXNlY3JldA==",
	).Replace(string(raw))
	file := filepath.Join(dir, "agentic.yml")
	if err := os.WriteFile(file, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func answers(addr string) bool {
	c := http.Client{Timeout: 2 * time.Second}
	res, err := c.Post("http://"+addr+"/api/v1/graphql", "application/json", strings.NewReader(`{"query":"{ __typename }"}`))
	if err != nil {
		return false
	}
	res.Body.Close()
	return true
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHostServesRestartsAndStops(t *testing.T) {
	t.Setenv("GO_ENV", "agentic")
	dir := t.TempDir()
	file := seedConfig(t, dir)
	if err := os.WriteFile(filepath.Join(dir, staleAgentKey), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	log := &syncBuffer{}
	h := &Host{ConfigFile: file, Addr: addr, Log: log, Poll: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- h.Run(ctx) }()

	waitFor(t, "graphjin to answer", func() bool { return answers(addr) })
	if _, err := os.Stat(filepath.Join(dir, staleAgentKey)); !os.IsNotExist(err) {
		t.Fatalf("stale agent key file still exists: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, RestartFile), []byte("token-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restart ack", func() bool { return readToken(filepath.Join(dir, RestartAckFile)) == "token-1" })
	waitFor(t, "graphjin to answer after restart", func() bool { return answers(addr) })
	if n := strings.Count(log.String(), "graphjin listening"); n < 2 {
		t.Fatalf("want a second start after the restart token, log:\n%s", log)
	}

	if err := os.WriteFile(filepath.Join(dir, PingFile), []byte("ping-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ping ack", func() bool { return readToken(filepath.Join(dir, PingAckFile)) == "ping-1" })

	cancel()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if answers(addr) {
		t.Fatal("graphjin still answers after Run returned")
	}
}

func TestHostKeepsAcknowledgedTokenAcrossStarts(t *testing.T) {
	t.Setenv("GO_ENV", "agentic")
	dir := t.TempDir()
	file := seedConfig(t, dir)
	for name, v := range map[string]string{RestartFile: "old", RestartAckFile: "old"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	addr := freeAddr(t)
	log := &syncBuffer{}
	h := &Host{ConfigFile: file, Addr: addr, Log: log, Poll: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()
	waitFor(t, "graphjin to answer", func() bool { return answers(addr) })
	time.Sleep(300 * time.Millisecond)
	if n := strings.Count(log.String(), "graphjin listening"); n != 1 {
		t.Fatalf("an acknowledged token must not restart graphjin; starts=%d log:\n%s", n, log)
	}
}
