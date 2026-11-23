package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-neko/openneko/apps/ax-harness/harness"
	"github.com/open-neko/openneko/apps/ax-harness/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

const spec = `{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Find reference"}`

// startServer runs turns with no confinement, as a test stands in for the
// runner; startConfinedServer keeps the rule of the product.
func startServer(t *testing.T, server *Server) string {
	t.Helper()
	server.testOnly = true
	return startConfinedServer(t, server)
}

func startConfinedServer(t *testing.T, server *Server) string {
	t.Helper()
	socket := filepath.Join(testsock.Dir(t), "serve.sock")
	listener, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return socket
}

// client is the worker's end of one turn.
type client struct {
	conn   net.Conn
	enc    *json.Encoder
	mu     sync.Mutex
	frames chan Frame
	mcp    *io.PipeWriter
	onMCP  func(json.RawMessage)
}

func dial(t *testing.T, socket string, start Frame, setup ...func(*client)) *client {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &client{conn: conn, enc: json.NewEncoder(conn), frames: make(chan Frame, 256)}
	for _, fn := range setup {
		fn(c)
	}
	if err := c.send(start); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(c.frames)
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 1<<20), maxFrameBytes)
		for scanner.Scan() {
			var frame Frame
			if json.Unmarshal(scanner.Bytes(), &frame) == nil {
				if frame.Type == "mcp" && c.mcp != nil {
					if c.onMCP != nil {
						c.onMCP(frame.Message)
					}
					_, _ = c.mcp.Write(append(append([]byte{}, frame.Message...), '\n'))
					continue
				}
				c.frames <- frame
			}
		}
	}()
	return c
}

func (c *client) send(frame Frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(frame)
}

// serveTools answers the harness's tool client with an MCP server, as the
// worker does, carrying its traffic in mcp frames.
func (c *client) serveTools(t *testing.T, listed chan<- struct{}) {
	t.Helper()
	server := protocol.NewServer(&protocol.Implementation{Name: "worker", Version: "1"}, nil)
	protocol.AddTool(server, &protocol.Tool{Name: "memory_search", Description: "Search memory"},
		func(context.Context, *protocol.CallToolRequest, struct{}) (*protocol.CallToolResult, any, error) {
			return &protocol.CallToolResult{}, nil, nil
		})
	fromServe, toServer := io.Pipe()
	c.mcp = toServer
	c.onMCP = func(message json.RawMessage) {
		if strings.Contains(string(message), `"tools/list"`) {
			select {
			case listed <- struct{}{}:
			default:
			}
		}
	}
	writer := &lineWriter{emit: func(line []byte) error { return c.send(Frame{Type: "mcp", Message: line}) }}
	go func() {
		_, _ = server.Connect(context.Background(), &protocol.IOTransport{Reader: fromServe, Writer: writer}, nil)
	}()
}

func fixtureModel(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var keys []string
	var mu sync.Mutex
	responses := []string{`{"javascriptCode":"final('Find reference', {})"}`, `{"javascriptCode":"final('Report', {reference:'REF-42'});"}`, `Answer: REF-42`}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		keys = append(keys, r.Header.Get("Authorization"))
		call := len(keys) - 1
		mu.Unlock()
		if call >= len(responses) {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": responses[call]}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(model.Close)
	return model, &keys
}

func TestServeRunsATurnWithTheFramesKeyAndSocketTools(t *testing.T) {
	model, keys := fixtureModel(t)
	env := map[string]string{"OPENAI_API_KEY": "serve-held-key", "PATH": "/usr/bin:/bin"}
	socket := startServer(t, &Server{Getenv: func(name string) string { return env[name] }})
	listed := make(chan struct{}, 1)

	c := dial(t, socket, Frame{
		Type: "start",
		Environ: []string{
			"OPENNEKO_MCP_ORG_ID=org-1", "OPENNEKO_MCP_THREAD_ID=thread-1",
			"HARNESS_MODEL_URL=" + model.URL, "HARNESS_MODEL=fixture",
			"HARNESS_MODEL_API_KEY=worker-supplied-key", "OPENAI_API_KEY=worker-supplied-key",
		},
		Spec:        json.RawMessage(spec),
		Bridge:      true,
		ModelKeyEnv: "OPENAI_API_KEY",
		ModelKeys:   map[string]string{"OPENAI_API_KEY": "frame-key"},
	}, func(c *client) { c.serveTools(t, listed) })

	var events []json.RawMessage
	var exit *Frame
	for frame := range c.frames {
		switch frame.Type {
		case "event":
			events = append(events, frame.Event)
		case "exit":
			f := frame
			exit = &f
		}
		if exit != nil {
			break
		}
	}
	if exit == nil || exit.Code == nil || *exit.Code != 0 {
		t.Fatalf("exit = %+v, events = %d", exit, len(events))
	}
	select {
	case <-listed:
	default:
		t.Fatal("the harness never listed the worker's tools over the socket")
	}
	last := string(events[len(events)-1])
	if !strings.Contains(last, `"run.finished"`) || !strings.Contains(last, "REF-42") {
		t.Fatalf("last event = %s", last)
	}
	for _, key := range *keys {
		if key != "Bearer frame-key" {
			t.Fatalf("model called with %q, want the frame's key and never serve's environment", key)
		}
	}
}

func TestServeCancelsOnHangupAndBoundsTurns(t *testing.T) {
	started := make(chan struct{}, 2)
	cancelled := make(chan struct{}, 2)
	var seen harness.Turn
	server := &Server{MaxTurns: 1, Sandbox: sandbox.Config{URL: "unix:///run/openneko/sandbox.sock", Key: "serve-sandbox-key"}, Run: func(ctx context.Context, turn harness.Turn) (int, error) {
		seen = turn
		started <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		return 1, ctx.Err()
	}}
	socket := startServer(t, server)
	first := dial(t, socket, Frame{Type: "start", Spec: json.RawMessage(spec), Environ: []string{"OPENNEKO_MCP_BRIDGE=/x", "ANTHROPIC_API_KEY=leak", "OPENNEKO_SANDBOX_URL=http://elsewhere", "OPENNEKO_SANDBOX_KEY=w", "A=1"},
		ModelKeys: map[string]string{"MODEL_API_KEY": "turn-key", "DATABASE_PASSWORD": "x"}})
	<-started
	for _, entry := range seen.Environ {
		if strings.HasPrefix(entry, "OPENNEKO_MCP_BRIDGE=") || strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") || strings.HasPrefix(entry, "OPENNEKO_SANDBOX_") {
			t.Fatalf("serve passed %q through from the worker", entry)
		}
	}
	if seen.Sandbox.URL != "unix:///run/openneko/sandbox.sock" || seen.Sandbox.Key != "serve-sandbox-key" {
		t.Fatalf("the turn's sandbox = %+v, want serve's", seen.Sandbox)
	}
	if got := seen.Secrets("DATABASE_URL"); got != "" {
		t.Fatalf("a non-key variable was readable as a secret: %q", got)
	}
	if got := seen.Secrets("HARNESS_MODEL_API_KEY"); got != "turn-key" {
		t.Fatalf("the turn's key = %q, want the one in model_keys", got)
	}
	if got := seen.Secrets("DATABASE_PASSWORD"); got != "" {
		t.Fatalf("a non-key variable was readable as a secret: %q", got)
	}

	second := dial(t, socket, Frame{Type: "start", Spec: json.RawMessage(spec)})
	frame := <-second.frames
	if frame.Type != "exit" || !strings.Contains(frame.Error, "turn limit") {
		t.Fatalf("second turn = %+v, want refused at the limit", frame)
	}

	_ = first.conn.Close()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("hanging up did not cancel the turn")
	}
}

func TestServeReadsNoModelKeyFromItsEnvironment(t *testing.T) {
	env := map[string]string{"MODEL_API_KEY": "serve-env-key", "OPENAI_API_KEY": "serve-env-key"}
	s := &Server{Getenv: func(name string) string { return env[name] }}
	for _, name := range []string{"HARNESS_MODEL_API_KEY", "OPENAI_API_KEY", "MODEL_API_KEY"} {
		if got := s.secrets("OPENAI_API_KEY", nil)(name); got != "" {
			t.Fatalf("%s read %q from serve's environment", name, got)
		}
	}
}

func TestServeWithRequireDoorRefusesARealKey(t *testing.T) {
	ran := make(chan struct{}, 4)
	server := &Server{RequireDoor: true, Run: func(context.Context, harness.Turn) (int, error) {
		ran <- struct{}{}
		return 0, nil
	}}
	socket := startServer(t, server)
	token := keydoor.TokenPrefix + strings.Repeat("a", 22) + "." + strings.Repeat("b", 43)
	for _, c := range []struct {
		frame Frame
		ok    bool
	}{
		{Frame{Type: "start", Spec: json.RawMessage(spec), ModelKeys: map[string]string{"MODEL_API_KEY": "sk-real"}}, false},
		{Frame{Type: "generate", Generate: json.RawMessage(`{}`), ModelKey: token, FallbackKey: "sk-real"}, false},
		{Frame{Type: "embed", Embed: json.RawMessage(`{}`), ModelKey: "sk-real"}, false},
		{Frame{Type: "start", Spec: json.RawMessage(spec), ModelKeys: map[string]string{"MODEL_API_KEY": token}, Environ: []string{"HARNESS_MODEL_OPTIONS={\"k\":\"sk-proj-" + strings.Repeat("x", 22) + "\"}"}}, false},
		{Frame{Type: "start", Spec: json.RawMessage(spec), ModelKeys: map[string]string{"MODEL_API_KEY": token}, Environ: []string{"RUN_DIR=/r", "NOTE=AIza" + strings.Repeat("x", 35)}}, false},
		{Frame{Type: "start", Spec: json.RawMessage(spec), ModelKeys: map[string]string{"MODEL_API_KEY": token}, Environ: []string{"MY_API_KEY=plain"}}, false},
		{Frame{Type: "start", Spec: json.RawMessage(spec), ModelKeys: map[string]string{"MODEL_API_KEY": token}, Environ: []string{"OPENNEKO_DATABASE_URL=postgres://u:p@db/x"}}, false},
		{Frame{Type: "start", Spec: json.RawMessage(spec), ModelKeys: map[string]string{"MODEL_API_KEY": token}, Environ: []string{"AWS_SECRET_ACCESS_KEY=s"}}, false},
		{Frame{Type: "start", Spec: json.RawMessage(spec), ModelKeys: map[string]string{"MODEL_API_KEY": token, "FALLBACK_MODEL_API_KEY": "none"},
			Environ: []string{"RUN_DIR=/tmp/desk-abcdefghijklmnopqrstuvwxyz", "HARNESS_MODEL=gpt-5", "MODEL_API_KEY=" + token, "OPENNEKO_HARNESS_WORKSPACE_DIR=/w"}}, true},
	} {
		frame := <-dial(t, socket, c.frame).frames
		refused := frame.Type == "exit" && strings.Contains(frame.Error, "takes no model key")
		if refused == c.ok {
			t.Fatalf("%s frame: exit %+v, want refused=%v", c.frame.Type, frame, !c.ok)
		}
	}
	if len(ran) != 1 {
		t.Fatalf("%d turns ran, want only the one with a door token", len(ran))
	}
}

func TestServeReportsItsKeyStateInAStatusFrame(t *testing.T) {
	for _, server := range []*Server{
		{RequireDoor: true},
		{RequireDoor: false, KeyEnv: []string{"GEMINI_API_KEY"}},
	} {
		frame := <-dial(t, startServer(t, server), Frame{Type: "status"}).frames
		var got Status
		if frame.Type != "status" || json.Unmarshal(frame.Result, &got) != nil {
			t.Fatalf("status frame %+v", frame)
		}
		if got.RequireDoor != server.RequireDoor || strings.Join(got.KeyEnv, ",") != strings.Join(server.KeyEnv, ",") {
			t.Fatalf("status %+v, want %+v", got, server)
		}
	}
}

func TestKeyEnvNamesNamesEveryControlZoneSecret(t *testing.T) {
	secrets := []string{"OPENAI_API_KEY", "NEKO_EMBEDDING_API_KEY", "HF_TOKEN", "VERTEX_ACCESS_TOKEN", "GOOGLE_VERTEX_ACCESS_TOKEN",
		"GOOGLE_APPLICATION_CREDENTIALS", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AZURE_OPENAI_KEY", "NEKO_SECRET_KEY",
		"DATABASE_URL", "OPENNEKO_DATABASE_URL", "OPENNEKO_DATABASE_URL_FILE", "OPENNEKO_MAGENTO_LIVE_DATABASE_URL"}
	environ := []string{"PATH=/bin", "OPENNEKO_SANDBOX_KEY=box", "GJ_DATABASE_URL=postgres://gj", "AWS_REGION=eu-west-1", "EMPTY_API_KEY="}
	for _, name := range secrets {
		environ = append(environ, name+"=v")
	}
	if got := strings.Join(KeyEnvNames(environ), ","); got != strings.Join(secrets, ",") {
		t.Fatalf("KeyEnvNames = %s\nwant %s", got, strings.Join(secrets, ","))
	}
}

func TestFramesFromWebCarryAScriptDataAnswerAndRunnerLinesStayCapped(t *testing.T) {
	message := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"` + strings.Repeat("x", 100<<20) + `"}]}}`
	line, _ := json.Marshal(Frame{Type: "mcp", Message: json.RawMessage(message)})
	frame, err := readFrame(bufio.NewReaderSize(bytes.NewReader(append(line, '\n')), 64<<10))
	if err != nil || frame.Type != "mcp" || len(frame.Message) != len(message) {
		t.Fatalf("a 100 MiB frame from web: %v", err)
	}
	runnerLine := append(bytes.Repeat([]byte("x"), maxRunnerFrameBytes+1), '\n')
	if _, err := readLine(bufio.NewReaderSize(bytes.NewReader(runnerLine), 64<<10), maxRunnerFrameBytes); err == nil || err.Error() != "frame too large" {
		t.Fatalf("a runner line over 32 MiB: %v", err)
	}
}
