package webfront

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
)

// startingGraphjin answers as the supervisor does while GraphJin starts: 503
// for the first down requests, or for all of them when down is negative.
type startingGraphjin struct {
	mu           sync.Mutex
	down         int
	lists, calls int
}

func (g *startingGraphjin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct{ Method string }
	_ = json.Unmarshal(raw, &body)
	g.mu.Lock()
	if body.Method == "tools/list" {
		g.lists++
	} else {
		g.calls++
	}
	down := g.down != 0
	if g.down > 0 {
		g.down--
	}
	g.mu.Unlock()
	w.Header().Set("content-type", "application/json")
	if down {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"errors":[{"message":"GraphJin is starting"}]}`))
		return
	}
	result := `{"content":[{"type":"text","text":"ok"}]}`
	if body.Method == "tools/list" {
		result = `{"tools":[{"name":"execute_graphql","inputSchema":{"type":"object"}}]}`
	}
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
}

func (g *startingGraphjin) seen() (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lists, g.calls
}

func startingEndpoint(t *testing.T, down int, wait time.Duration) (*startingGraphjin, graphjinEndpoint) {
	t.Helper()
	oldWait, oldRetry := graphjinStartWait, graphjinStartRetry
	graphjinStartWait, graphjinStartRetry = wait, 5*time.Millisecond
	t.Cleanup(func() { graphjinStartWait, graphjinStartRetry = oldWait, oldRetry })
	gj := &startingGraphjin{down: down}
	srv := httptest.NewServer(gj)
	t.Cleanup(srv.Close)
	return gj, graphjinEndpoint{url: srv.URL, headers: func(context.Context) (map[string]string, error) { return nil, nil }}
}

func startingTools(ep graphjinEndpoint) *toolServer {
	return newToolServer(toolGroup{dynamic: &dynamicTools{
		server: "neko_graphjin",
		list:   func(ctx context.Context) ([]any, error) { return listGraphjinMCPTools(ctx, ep) },
		call: func(ctx context.Context, name string, args *jsObj) (any, error) {
			return callGraphjinMCPTool(ctx, ep, name, args)
		},
	}})
}

func TestGraphjinRequestsWaitWhileItStarts(t *testing.T) {
	ctx := context.Background()

	t.Run("list", func(t *testing.T) {
		gj, ep := startingEndpoint(t, 2, 5*time.Second)
		tools, err := listGraphjinMCPTools(ctx, ep)
		if lists, _ := gj.seen(); err != nil || len(tools) != 1 || lists != 3 {
			t.Fatalf("tools %v, err %v, lists %d", tools, err, lists)
		}
	})

	t.Run("call", func(t *testing.T) {
		gj, ep := startingEndpoint(t, 1, 5*time.Second)
		result, err := callGraphjinMCPTool(ctx, ep, "execute_graphql", jsObjOf("query", "{ orders { id } }"))
		if _, calls := gj.seen(); err != nil || !strings.Contains(jsStringify(result, false), `"ok"`) || calls != 2 {
			t.Fatalf("result %v, err %v, calls %d", result, err, calls)
		}
	})

	t.Run("a mutation is sent once", func(t *testing.T) {
		gj, ep := startingEndpoint(t, 1, 5*time.Second)
		_, err := callGraphjinMCPTool(ctx, ep, "execute_graphql", jsObjOf("query", "mutation { orders(insert: {}) { id } }"))
		var starting *graphjinStartingError
		if _, calls := gj.seen(); !errors.As(err, &starting) || calls != 1 {
			t.Fatalf("err %v, calls %d", err, calls)
		}
	})

	t.Run("the wait ends", func(t *testing.T) {
		gj, ep := startingEndpoint(t, -1, 50*time.Millisecond)
		began := time.Now()
		_, err := listGraphjinMCPTools(ctx, ep)
		var starting *graphjinStartingError
		if lists, _ := gj.seen(); !errors.As(err, &starting) || lists < 2 || time.Since(began) > 3*time.Second ||
			!strings.HasPrefix(err.Error(), "GraphJin MCP tools/list failed: 503 ") {
			t.Fatalf("err %v, lists %d, took %s", err, lists, time.Since(began))
		}
	})
}

func TestAxTurnStopsWhileDataStarts(t *testing.T) {
	clearAxEnv(t, nil)
	gj, ep := startingEndpoint(t, -1, 50*time.Millisecond)
	srv := newScriptedServe(t, nil)
	var events []string
	res, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-1", Servers: []string{"neko_graphjin"},
		Tools: startingTools(ep), Socket: srv.socket, Keys: map[string]string{"OPENAI_API_KEY": "k"},
		Emit: func(_ context.Context, event any) error {
			events = append(events, jsStringify(event, false))
			return nil
		}})
	if err != nil || res.Status != "failed" || res.Error != dataStartingMessage {
		t.Fatalf("got %+v %v", res, err)
	}
	if len(events) != 1 || events[0] != `{"type":"error","message":"The data connection is still starting. Ask again in a minute."}` {
		t.Fatalf("events %v", events)
	}
	srv.mu.Lock()
	starts := len(srv.starts)
	srv.mu.Unlock()
	if lists, _ := gj.seen(); starts != 0 || lists < 2 {
		t.Fatalf("the turn started: %d starts, %d lists", starts, lists)
	}
}

func TestAxTurnWaitsForDataTools(t *testing.T) {
	clearAxEnv(t, nil)
	gj, ep := startingEndpoint(t, 2, 5*time.Second)
	dir := testsock.Dir(t)
	socket := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	listed := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		_, _ = r.ReadString('\n')
		send := func(s string) { conn.Write([]byte(s + "\n")) }
		send(`{"type":"mcp","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}`)
		line, _ := r.ReadString('\n')
		listed <- line
		send(`{"type":"event","event":{"version":1,"run_id":"workrun-9","sequence":1,"type":"run.finished","result":{"status":"completed","answer":"Done."}}}`)
		send(`{"type":"exit","code":0}`)
	}()
	res, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-9", Servers: []string{"neko_graphjin"},
		Tools: startingTools(ep), Socket: socket, Keys: map[string]string{"OPENAI_API_KEY": "k"}})
	if err != nil || res.Status != "completed" || res.FinalText != "Done." {
		t.Fatalf("got %+v %v", res, err)
	}
	if line := <-listed; !strings.Contains(line, `"name":"graphjin_execute_graphql"`) {
		t.Fatalf("the harness list: %s", line)
	}
	if lists, _ := gj.seen(); lists != 3 {
		t.Fatalf("two failed lists and one good list: %d", lists)
	}
}
