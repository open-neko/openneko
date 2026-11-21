package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// serveDoor points this process's serve at the door beside socket, as
// openneko serve does.
func serveDoor(t *testing.T, socket string) {
	t.Helper()
	keydoor.SetSocket(keydoor.SocketPath(func(string) string { return "" }, socket))
	t.Cleanup(func() { keydoor.SetSocket("") })
}

// strictServe runs serve as production does: it refuses a frame with a key.
func strictServe(t *testing.T) string {
	t.Helper()
	dir := testsock.Dir(t)
	socket := filepath.Join(dir, "serve.sock")
	ln, err := serve.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	serveDoor(t, socket)
	ctx, stop := context.WithCancel(context.Background())
	srv := (&serve.Server{MaxTurns: 2, RequireDoor: true}).RunUnconfinedForTest()
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { stop(); <-done; os.RemoveAll(dir) })
	t.Setenv("OPENNEKO_SERVE_SOCKET", socket)
	t.Setenv(keydoor.SocketEnv, "")
	return socket
}

type keySeen struct {
	mu   sync.Mutex
	auth []string
}

func (k *keySeen) all() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string{}, k.auth...)
}

func keyUpstream(t *testing.T, reply func(w http.ResponseWriter)) (*httptest.Server, *keySeen) {
	t.Helper()
	seen := &keySeen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		seen.mu.Lock()
		seen.auth = append(seen.auth, r.Header.Get("Authorization"))
		seen.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		reply(w)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestHostCallGoesThroughTheKeyDoor(t *testing.T) {
	strictServe(t)
	upstream, seen := keyUpstream(t, func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "Reply: READY"}, "finish_reason": "stop"}}})
	})
	a := &API{}
	s := llm.Settings{Source: "org", Provider: "openai-compatible", Model: "fixture", Enabled: true,
		Config: map[string]any{"url": upstream.URL + "/v1"}, Secrets: map[string]string{"apiKey": "planted-host-key"}}
	if err := a.hostCalls().TestPrimary(context.Background(), "org-1", s); err != nil {
		t.Fatal(err)
	}
	got := seen.all()
	if len(got) == 0 {
		t.Fatal("the upstream got no call")
	}
	for _, auth := range got {
		if auth != "Bearer planted-host-key" {
			t.Fatalf("the upstream got %q, want the stored key", auth)
		}
	}
}

func TestEmbeddingGoesThroughTheKeyDoor(t *testing.T) {
	strictServe(t)
	upstream, seen := keyUpstream(t, func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float64{1, 2, 3}}}})
	})
	t.Setenv("NEKO_EMBEDDING_URL", upstream.URL+"/v1")
	t.Setenv("NEKO_EMBEDDING_MODEL", "fixture")
	t.Setenv("NEKO_EMBEDDING_API_KEY", "planted-embed-key")
	t.Setenv("NEKO_EMBEDDING_DIMENSIONS", "3")
	v, err := (&API{}).EmbedText(context.Background(), "a memory")
	if err != nil || len(v) != 3 {
		t.Fatalf("vector %v, %v", v, err)
	}
	if got := seen.all(); len(got) != 1 || got[0] != "Bearer planted-embed-key" {
		t.Fatalf("the upstream got %q", got)
	}
}
