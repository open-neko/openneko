package webfront

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// spendingServe is a serve that spends its turn's door token until the door
// refuses, then finishes the turn. It reports the spec's ceiling and the
// calls the door forwarded.
func spendingServe(t *testing.T) (string, *llm.KeyDoor, <-chan [2]int) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ceil")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	door := &llm.KeyDoor{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	doorPath := filepath.Join(dir, keydoor.SocketName)
	if _, err := llm.ListenDoor(ctx, doorPath, door); err != nil {
		t.Fatal(err)
	}
	keydoor.SetSocket(doorPath)
	t.Cleanup(func() { keydoor.SetSocket("") })
	socket := filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan [2]int, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadBytes('\n')
			var start serve.Frame
			var spec struct {
				RunID         string `json:"run_id"`
				MaxModelCalls int    `json:"max_model_calls"`
			}
			_ = json.Unmarshal(line, &start)
			_ = json.Unmarshal(start.Spec, &spec)
			calls := 0
			if client, err := keydoor.Client(); err == nil && spec.MaxModelCalls < 100 {
				for calls < 200 {
					req, _ := http.NewRequest(http.MethodPost, keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{}`))
					req.Header.Set("Authorization", "Bearer "+start.ModelKeys["MODEL_API_KEY"])
					res, err := client.Do(req)
					if err != nil {
						break
					}
					_, _ = io.Copy(io.Discard, res.Body)
					res.Body.Close()
					if res.StatusCode != 200 {
						break
					}
					calls++
				}
			}
			got <- [2]int{spec.MaxModelCalls, calls}
			frame, _ := json.Marshal(map[string]any{"type": "event", "event": map[string]any{"version": 1, "run_id": spec.RunID, "sequence": 1,
				"type": "run.finished", "result": map[string]any{"status": "completed", "kind": "answer", "answer": "ok",
					"usage": map[string]any{"requests": calls}}}})
			_, _ = conn.Write(append(frame, '\n'))
			conn.Close()
		}
	}()
	return socket, door, got
}

func TestARunsOwnModelCallCeilingCapsItsDoorToken(t *testing.T) {
	clearAxEnv(t, nil)
	up, _ := keyUpstream(t, func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{}`) })
	socket, door, got := spendingServe(t)
	cfg := &llm.AxBackendConfig{Route: llm.AxModelRoute{Provider: "openai-compatible", Model: "m", URL: up.URL + "/v1"}}
	structural := agentLimitsFor("chat").maxModelCalls()
	for _, c := range []struct {
		ceiling, spec, calls int
	}{
		{7, 7, 7 + axDoorCallAllowance},
		{structural + 50, structural, -1},
		{0, structural, -1},
	} {
		res, err := runAx(context.Background(), cfg, axTurnOptions{OrgID: "org", Tag: "t", Socket: socket, Door: door,
			Keys: map[string]string{"MODEL_API_KEY": "k"}, MaxModelCalls: c.ceiling})
		if err != nil || res.Status != "completed" {
			t.Fatalf("ceiling %d: %+v %v", c.ceiling, res, err)
		}
		g := <-got
		if g[0] != c.spec {
			t.Errorf("ceiling %d: serve got max_model_calls %d, want %d", c.ceiling, g[0], c.spec)
		}
		if c.calls >= 0 && g[1] != c.calls {
			t.Errorf("ceiling %d: the door forwarded %d calls, want %d", c.ceiling, g[1], c.calls)
		}
	}
}
