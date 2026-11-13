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
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/catalog"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

var axFixtureEnv = []string{
	"MODEL_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "AZURE_OPENAI_API_KEY", "FALLBACK_MODEL_API_KEY",
	"OPENNEKO_SANDBOX_URL", "OPENNEKO_SANDBOX_KEY", "OPENNEKO_AX_SKILL_RERANK", "OPENNEKO_AGENT_TURN_TIMEOUT_MS",
	"OPENNEKO_AGENT_MAX_TURNS", "OPENNEKO_AGENT_DELEGATION_MAX_ITERATIONS", "OPENNEKO_AGENT_TOOL_OUTPUT_MAX_BYTES",
	"OPENNEKO_AGENT_TERMINAL_TIMEOUT_SECONDS", "OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS", "OPENNEKO_WORKFLOW_MAX_TOOL_CALLS",
	"OPENNEKO_WORKFLOW_MAX_CONTINUATIONS", "OPENNEKO_WORKFLOW_REASONING_EFFORT", "OPENNEKO_SERVE_URL", "OPENNEKO_MCP_BRIDGE",
}

func clearAxEnv(t *testing.T, set map[string]string) {
	t.Helper()
	for _, name := range axFixtureEnv {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	for k, v := range set {
		t.Setenv(k, v)
	}
}

type fixtureRoute struct {
	Provider, Model, URL, KeyEnv string
	Options                      map[string]string
	Price                        *llm.Price
}

func (r fixtureRoute) route() llm.AxModelRoute {
	return llm.AxModelRoute{Provider: r.Provider, Model: r.Model, URL: r.URL, KeyEnv: r.KeyEnv, Options: r.Options, Price: r.Price}
}

type fixtureTurn struct {
	Name   string
	Config struct {
		Route               fixtureRoute
		Fallback            *fixtureRoute
		ContextWindowTokens int
		MaxOutputTokens     int
		ReasoningEffort     string
	}
	Opts struct {
		Prompt, UserMessage, OrgID, Tag string
		Workspace                       *agentWorkspace
		BackendState                    json.RawMessage
		WantsCards, Debug               bool
		McpBridgeEnv                    map[string]string
		NativeDelegation                string
		NetworkHosts                    []string
		TimeoutMs, MaxToolIterations    int
		ReasoningEffort                 string
	}
	Env     map[string]string
	Scripts [][]json.RawMessage
	Starts  []json.RawMessage
	Events  []json.RawMessage
	Result  json.RawMessage
}

// scriptedServe answers each connection with the next script and records the
// start frames.
type scriptedServe struct {
	socket string
	mu     sync.Mutex
	starts []string
	ln     net.Listener
}

func newScriptedServe(t *testing.T, scripts [][]json.RawMessage) *scriptedServe {
	t.Helper()
	dir, err := os.MkdirTemp("", "ax")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &scriptedServe{socket: filepath.Join(dir, "serve.sock")}
	if f.ln, err = net.Listen("unix", f.socket); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.ln.Close() })
	go func() {
		for {
			conn, err := f.ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			var script []json.RawMessage
			if len(scripts) > 0 {
				script, scripts = scripts[0], scripts[1:]
			}
			f.mu.Unlock()
			go func() {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				f.mu.Lock()
				f.starts = append(f.starts, line)
				f.mu.Unlock()
				for _, frame := range script {
					if string(frame) == `"hangup"` {
						return
					}
					var raw struct {
						Raw *string `json:"raw"`
					}
					out := string(jsCompact(frame))
					if json.Unmarshal(frame, &raw) == nil && raw.Raw != nil {
						out = *raw.Raw
					}
					if _, err := conn.Write([]byte(out + "\n")); err != nil {
						return
					}
				}
			}()
		}
	}()
	return f
}

func jsCompact(raw []byte) []byte {
	v, err := jsParse(raw)
	if err != nil {
		return raw
	}
	return []byte(jsStringify(v, false))
}

var (
	pricesVersionRE = regexp.MustCompile(`ax-go@[^+"]*\+`)
	answerIDs       = regexp.MustCompile(`answer-[0-9a-f-]{36}`)
	scratchRE       = regexp.MustCompile(regexp.QuoteMeta(filepath.Join(os.TempDir(), "neko-ax-")) + `[A-Za-z0-9]+`)
)

func normalStart(t *testing.T, raw string) string {
	t.Helper()
	v, err := jsParse([]byte(raw))
	if err != nil {
		t.Fatalf("start frame is not JSON: %s", raw)
	}
	o := v.(*jsObj)
	var environ []string
	for _, e := range o.vals["environ"].([]any) {
		environ = append(environ, scratchRE.ReplaceAllString(e.(string), "SCRATCH"))
	}
	slices.Sort(environ)
	o.set("environ", stringsAny(environ))
	return jsCanonical(o)
}

func TestAxTurnMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/axturn.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Turns []fixtureTurn
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Turns) < 12 {
		t.Fatalf("fixture holds %d turns, want at least 12", len(fixture.Turns))
	}
	for _, c := range fixture.Turns {
		t.Run(c.Name, func(t *testing.T) {
			clearAxEnv(t, c.Env)
			srv := newScriptedServe(t, c.Scripts)
			cfg := &llm.AxBackendConfig{Route: c.Config.Route.route(), ContextWindowTokens: c.Config.ContextWindowTokens,
				MaxOutputTokens: c.Config.MaxOutputTokens, ReasoningEffort: c.Config.ReasoningEffort}
			if c.Config.Fallback != nil {
				r := c.Config.Fallback.route()
				cfg.Fallback = &r
			}
			keys := map[string]string{}
			for k, v := range c.Env {
				if strings.HasSuffix(k, "_API_KEY") {
					keys[k] = v
				}
			}
			state, _ := jsParse(c.Opts.BackendState)
			var events []string
			o := axTurnOptions{Prompt: c.Opts.Prompt, UserMessage: c.Opts.UserMessage, OrgID: c.Opts.OrgID, Tag: c.Opts.Tag,
				ThreadID: c.Opts.McpBridgeEnv["OPENNEKO_MCP_THREAD_ID"], Workspace: c.Opts.Workspace, BackendState: state,
				WantsCards: c.Opts.WantsCards, RichCards: c.Opts.McpBridgeEnv["OPENNEKO_MCP_CARD_SCHEMA"] == "rich",
				NetworkHosts: c.Opts.NetworkHosts, Debug: c.Opts.Debug, NativeDelegationOff: c.Opts.NativeDelegation == "disabled",
				Shell: c.Env["OPENNEKO_SANDBOX_URL"] != "", TimeoutMs: c.Opts.TimeoutMs, MaxToolIterations: c.Opts.MaxToolIterations,
				ReasoningEffort: c.Opts.ReasoningEffort, Keys: keys, Socket: srv.socket,
				Emit: func(_ context.Context, event any) error {
					events = append(events, answerIDs.ReplaceAllString(jsStringify(event, false), "answer-ID"))
					return nil
				}}
			if o.Workspace != nil {
				o.PythonPath = o.Workspace.BinRoot
			}
			res, err := runAx(context.Background(), cfg, o)
			if err != nil {
				t.Fatal(err)
			}
			srv.mu.Lock()
			starts := slices.Clone(srv.starts)
			srv.mu.Unlock()
			if len(starts) != len(c.Starts) {
				t.Fatalf("start frames: got %d, want %d", len(starts), len(c.Starts))
			}
			for i := range starts {
				if got, want := normalStart(t, starts[i]), normalStart(t, string(c.Starts[i])); got != want {
					t.Errorf("start %d:\n got %s\nwant %s", i, got, want)
				}
			}
			if len(events) != len(c.Events) {
				t.Errorf("events: got %d, want %d\n got %v", len(events), len(c.Events), events)
			}
			for i := range min(len(events), len(c.Events)) {
				got, want := events[i], string(jsCompact(c.Events[i]))
				if catalog.Version() == "" {
					got, want = pricesVersionRE.ReplaceAllString(got, "ax-go@V+"), pricesVersionRE.ReplaceAllString(want, "ax-go@V+")
				}
				if got != want {
					t.Errorf("event %d:\n got %s\nwant %s", i, got, want)
				}
			}
			if got, want := jsStringify(res.js(), false), string(jsCompact(c.Result)); got != want {
				t.Errorf("result:\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestAxHelpersMatchNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/axturn.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cores []struct {
			Name                string
			WantsCards          *bool
			CardSchema          string
			SourceConfigEnabled bool
			PackActions         []packActionDescriptor
			FilesURL            string `json:"filesUrl"`
			NativeDelegation    string
			Servers             []string
		}
		Limits []struct {
			Kind   string
			Env    map[string]string
			Limits struct {
				MaxTurns                                  int
				Delegation                                struct{ MaxIterations int }
				ToolOutput                                struct{ MaxBytes int }
				Terminal                                  struct{ TimeoutSeconds int }
				TimeoutMs, MaxToolCalls, MaxContinuations int
				ReasoningEffort                           string
			}
			Spec json.RawMessage
			Env2 map[string]string
		}
		Usages []struct {
			Name  string
			Usage json.RawMessage
			Route fixtureRoute
			Out   json.RawMessage
		}
		Prompts []struct {
			Prompt, UserMessage string
			Out                 json.RawMessage
		}
		Outside []struct{ Raw, Out string }
		Cards   []struct {
			Raw  string
			Rich bool
			Out  json.RawMessage
		}
		Surfaces []struct {
			Raw string
			Out json.RawMessage
		}
		Markdown []struct {
			Messages json.RawMessage
			Out      string
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cores {
		in := agentCoreInput{CardsOff: c.WantsCards != nil && !*c.WantsCards, CoreCards: c.CardSchema == "core",
			SourceConfigEnabled: c.SourceConfigEnabled, PackActions: c.PackActions,
			FilesEnabled: func() bool { return c.FilesURL != "" }}
		got := agentCoreServers(in, len(packActionTools(in.binding(), &fakeTools{})))
		if !slices.Equal(got, c.Servers) {
			t.Errorf("%s servers:\n got %v\nwant %v", c.Name, got, c.Servers)
		}
	}
	for _, c := range fixture.Limits {
		clearAxEnv(t, c.Env)
		l := agentLimitsFor(c.Kind)
		w := c.Limits
		if l.MaxTurns != w.MaxTurns || l.DelegationMaxIterations != w.Delegation.MaxIterations || l.ToolOutputMaxBytes != w.ToolOutput.MaxBytes ||
			l.TerminalTimeoutSeconds != w.Terminal.TimeoutSeconds || l.TimeoutMs != w.TimeoutMs || l.MaxToolCalls != w.MaxToolCalls ||
			l.MaxContinuations != w.MaxContinuations || l.ReasoningEffort != w.ReasoningEffort {
			t.Errorf("%s %v limits: got %+v, want %+v", c.Kind, c.Env, l, w)
		}
		spec := &jsObj{}
		l.axSpec(spec)
		if got, want := jsStringify(spec, false), string(jsCompact(c.Spec)); got != want {
			t.Errorf("%s spec: got %s, want %s", c.Kind, got, want)
		}
		if got := c.Env2["OPENNEKO_HARNESS_TERMINAL_MAX_OUTPUT"]; got != jsNumberText(float64(l.ToolOutputMaxBytes)) {
			t.Errorf("%s terminal output env: %s", c.Kind, got)
		}
	}
	clearAxEnv(t, nil)
	for _, c := range fixture.Usages {
		var u *jsObj
		if len(c.Usage) > 0 {
			v, _ := jsParse(c.Usage)
			u, _ = v.(*jsObj)
		}
		route := c.Route.route()
		got, want := jsStringify(normalizeAxUsage(u, &route, time.Now()), false), string(jsCompact(c.Out))
		if catalog.Version() == "" {
			got, want = pricesVersionRE.ReplaceAllString(got, "ax-go@V+"), pricesVersionRE.ReplaceAllString(want, "ax-go@V+")
		}
		if got != want {
			t.Errorf("usage %s:\n got %s\nwant %s", c.Name, got, want)
		}
	}
	for _, c := range fixture.Prompts {
		spec := &jsObj{}
		axRunPrompt(spec, c.Prompt, c.UserMessage)
		if got, want := jsStringify(spec, false), string(jsCompact(c.Out)); got != want {
			t.Errorf("prompt %q:\n got %s\nwant %s", c.Prompt, got, want)
		}
	}
	for _, c := range fixture.Outside {
		if got := outsideFenceText(c.Raw); got != c.Out {
			t.Errorf("outside %q: got %q, want %q", c.Raw, got, c.Out)
		}
	}
	saved := cardSurfaceID
	cardSurfaceID = func() string { return "answer-ID" }
	defer func() { cardSurfaceID = saved }()
	for _, c := range fixture.Cards {
		text, messages, issues := extractCardFences(c.Raw, c.Rich)
		got := jsStringify(jsObjOf("text", text, "messages", messages, "issues", stringsAny(issues)), false)
		if want := string(jsCompact(c.Out)); got != want {
			t.Errorf("cards %q rich=%v:\n got %s\nwant %s", c.Raw, c.Rich, got, want)
		}
	}
	for _, c := range fixture.Surfaces {
		text, messages := extractSurfaceMessages(c.Raw)
		if got, want := jsStringify(jsObjOf("text", text, "messages", messages), false), string(jsCompact(c.Out)); got != want {
			t.Errorf("surface %q:\n got %s\nwant %s", c.Raw, got, want)
		}
	}
	for _, c := range fixture.Markdown {
		v, _ := jsParse(c.Messages)
		if got := extractMarkdownText(v); got != c.Out {
			t.Errorf("markdown %s: got %q, want %q", c.Messages, got, c.Out)
		}
	}
}

func frames(lines ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(lines))
	for i, l := range lines {
		out[i] = json.RawMessage(l)
	}
	return out
}

var plainCfg = &llm.AxBackendConfig{Route: llm.AxModelRoute{Provider: "openai", Model: "gpt-4.1", KeyEnv: "OPENAI_API_KEY"}}

// The harness's tool client reaches the run's tool server through mcp frames
// on serve's socket.
func TestAxTurnBridgesTools(t *testing.T) {
	clearAxEnv(t, nil)
	dir, _ := os.MkdirTemp("", "ax")
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	replies := make(chan []string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		start, _ := r.ReadString('\n')
		var got []string
		if !strings.Contains(start, `"bridge":true`) {
			got = append(got, "no bridge in "+start)
		}
		send := func(s string) { conn.Write([]byte(s + "\n")) }
		send(`{"type":"mcp","message":{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}}`)
		send(`{"type":"mcp","message":{"jsonrpc":"2.0","id":2,"method":"tools/list"}}`)
		send(`{"type":"mcp","message":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"revenue"}}}}`)
		for len(got) < 3 {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			got = append(got, strings.TrimSpace(line))
		}
		replies <- got
		send(`{"type":"event","event":{"version":1,"run_id":"workrun-9","sequence":1,"type":"run.finished","result":{"status":"completed","answer":"Done."}}}`)
		send(`{"type":"exit","code":0}`)
	}()
	ft := &fakeTools{reply: []any{"hit"}}
	b := turnBinding{OrgID: "org-1", RunID: "run-9", Kind: "work"}
	tools, err := turnTools(b, ft, []string{"neko_memory"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-9", Servers: []string{"neko_memory"},
		Tools: tools, Socket: socket, Keys: map[string]string{"OPENAI_API_KEY": "k"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" || res.FinalText != "Done." {
		t.Fatalf("result %+v", res)
	}
	got := <-replies
	if len(got) != 3 {
		t.Fatalf("replies %v", got)
	}
	byID := map[string]string{}
	for _, line := range got {
		var f struct {
			Type    string
			Message struct {
				ID     json.RawMessage
				Result json.RawMessage
			}
		}
		if json.Unmarshal([]byte(line), &f) != nil || f.Type != "mcp" {
			t.Fatalf("frame %s", line)
		}
		byID[string(f.Message.ID)] = string(f.Message.Result)
	}
	if !strings.Contains(byID["1"], "openneko-mcp-multiplexer") || !strings.Contains(byID["2"], `"name":"memory_search"`) ||
		!strings.Contains(byID["3"], `\"results\":[\"hit\"]`) {
		t.Fatalf("replies %v", byID)
	}
}

func TestAxTurnCancelReaperAndFaults(t *testing.T) {
	clearAxEnv(t, nil)
	hold := frames(`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":1,"type":"actor.step","data":{"text":"working"}}}`)

	t.Run("cancel hangs up", func(t *testing.T) {
		srv := newScriptedServe(t, [][]json.RawMessage{append(hold, json.RawMessage(`{"raw":""}`))})
		ctx, cancel := context.WithCancel(context.Background())
		res, err := runAx(ctx, plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-1", Socket: srv.socket,
			Emit: func(_ context.Context, event any) error {
				if event.(*jsObj).vals["type"] == "status" {
					cancel()
				}
				return nil
			}})
		if err != nil || res.Status != "cancelled" {
			t.Fatalf("got %+v %v", res, err)
		}
	})

	t.Run("reaper stops a stuck turn", func(t *testing.T) {
		saved := axKillGrace
		axKillGrace = 20 * time.Millisecond
		defer func() { axKillGrace = saved }()
		dir, _ := os.MkdirTemp("", "ax")
		defer os.RemoveAll(dir)
		socket := filepath.Join(dir, "s.sock")
		ln, _ := net.Listen("unix", socket)
		defer ln.Close()
		closed := make(chan struct{})
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, conn)
			close(closed)
		}()
		started := time.Now()
		res, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-1", Socket: socket, TimeoutMs: 1})
		if err != nil || res.Status != "failed" || res.Error != "ax-harness exited (code=null) without a result" || res.ErrorCode != "exit" {
			t.Fatalf("got %+v %v", res, err)
		}
		<-closed
		if time.Since(started) > 5*time.Second {
			t.Fatal("reaper too slow")
		}
	})

	t.Run("emit failure stops the run", func(t *testing.T) {
		srv := newScriptedServe(t, [][]json.RawMessage{append(hold, json.RawMessage(`{"type":"exit","code":0}`))})
		_, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-1", Socket: srv.socket,
			Emit: func(context.Context, any) error { return errors.New("store down") }})
		if err == nil || err.Error() != "store down" {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("no serve", func(t *testing.T) {
		var events []string
		res, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Socket: filepath.Join(t.TempDir(), "none.sock"),
			Emit: func(_ context.Context, e any) error { events = append(events, jsStringify(e, false)); return nil }})
		if err != nil || res.Status != "failed" || !strings.HasPrefix(res.Error, "openneko serve unavailable: ") || len(events) != 1 {
			t.Fatalf("got %+v %v %v", res, err, events)
		}
	})

	t.Run("no model", func(t *testing.T) {
		res, err := runAx(context.Background(), nil, axTurnOptions{Prompt: "p"})
		if err != nil || res.Error != "Ax backend has no model configured. Set the primary provider in admin settings." {
			t.Fatalf("got %+v %v", res, err)
		}
	})

	t.Run("cancelled before start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, _ := runAx(ctx, plainCfg, axTurnOptions{Prompt: "p", BackendState: "s"})
		if res.Status != "cancelled" || res.BackendState != "s" {
			t.Fatalf("got %+v", res)
		}
	})
}

// A turn through a real openneko serve and the harness, with a fake
// OpenAI-compatible model: the key crosses the socket in its own field and
// the agent core's tools reach the model.
func TestAgentCoreThroughServe(t *testing.T) {
	clearAxEnv(t, nil)
	responses := []string{
		`{"javascriptCode":"final('Find reference', {})"}`,
		`{"javascriptCode":"final('Report', {reference:'REF-42'});"}`,
		"Answer: REF-42",
	}
	var mu sync.Mutex
	var calls int
	var auths, bodies []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		content := responses[min(calls, len(responses)-1)]
		calls++
		auths = append(auths, r.Header.Get("Authorization"))
		bodies = append(bodies, string(body))
		mu.Unlock()
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("content-type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0,
				"delta": map[string]any{"role": "assistant", "content": content}}}})
			end, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
			_, _ = w.Write([]byte("data: " + string(chunk) + "\n\ndata: " + string(end) + "\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	}))
	defer model.Close()

	dir, _ := os.MkdirTemp("", "ax")
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "serve.sock")
	ln, err := serve.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	srv := &serve.Server{Getenv: os.Getenv, MaxTurns: 2}
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, ln); close(done) }()
	defer func() { stop(); <-done }()

	ws, err := turnWorkspace("org-core", "thread-core", "run-core")
	if err != nil {
		t.Skipf("workspace: %v", err)
	}
	for _, d := range []string{ws.SkillsRoot, ws.RunRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ft := &fakeTools{reply: []any{}}
	var events []string
	var evMu sync.Mutex
	res, err := (&API{}).runAgentCore(context.Background(), agentCoreInput{
		Backend: &llm.AxBackendConfig{Route: llm.AxModelRoute{Provider: "openai-compatible", Model: "fixture", URL: model.URL, KeyEnv: "OPENAI_API_KEY"}},
		Prompt:  "Find reference",
		OrgID:   "org-core", ThreadID: "thread-core", RunID: "run-core",
		Workspace: ws, Keys: map[string]string{"OPENAI_API_KEY": "org-stored-key"}, Socket: socket,
		NativeDelegationOff: true,
		FilesEnabled:        func() bool { return false },
		toolBackend:         func(turnBinding) toolBackend { return ft },
		withoutDataSocket:   true,
		Emit: func(_ context.Context, e any) error {
			evMu.Lock()
			events = append(events, jsStringify(e, false))
			evMu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" || !strings.Contains(res.FinalText, "REF-42") {
		t.Fatalf("result %+v\nevents %v", res, events)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range auths {
		if a != "Bearer org-stored-key" {
			t.Fatalf("model got authorization %q", a)
		}
	}
	all := strings.Join(bodies, "\n")
	for _, tool := range []string{"memory_search", "workflow_builder_", "audit_"} {
		if !strings.Contains(all, tool) {
			t.Errorf("model requests do not name %s", tool)
		}
	}
	if strings.Contains(strings.Join(events, "\n"), "org-stored-key") {
		t.Fatal("an event holds the model key")
	}
}

func TestResolveAgentBackendDatabase(t *testing.T) {
	f, db := memoryFront(t)
	ctx := t.Context()
	resolve := func() *llm.AxBackendConfig {
		t.Helper()
		cfg, err := llm.ResolveAgentBackend(ctx, f.API.DB, "org")
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	seed(t, db, `delete from llm_provider_config where org_id = 'org'`)
	if cfg := resolve(); cfg != nil {
		t.Fatalf("no provider row: %+v", cfg)
	}
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, model, enabled, config, secrets)
		values ('org', 'primary', 'azure-openai', ' gpt-4.1 ', true, '{"resourceName":"r","deploymentName":"gpt-4.1","context_window_tokens":128000,"max_output_tokens":0,"reasoning_effort":"max"}', '{}')`)
	cfg := resolve()
	if cfg == nil || cfg.Route.Provider != "azure-openai" || cfg.Route.Model != "gpt-4.1" || cfg.Route.Options["resource_name"] != "r" ||
		cfg.Route.KeyEnv == "" || cfg.ContextWindowTokens != 128000 || cfg.MaxOutputTokens != 0 || cfg.ReasoningEffort != "medium" || cfg.Fallback != nil {
		t.Fatalf("primary row: %+v", cfg)
	}
	seed(t, db, `update llm_provider_config set enabled = false where org_id = 'org' and scope = 'primary'`)
	if cfg := resolve(); cfg != nil {
		t.Fatalf("disabled row: %+v", cfg)
	}
	seed(t, db, `insert into llm_model (org_id, name, provider, model, config, secrets, enabled, input_usd_per_1m, output_usd_per_1m) values
		('org', 'main', 'openai-compatible', 'qwen3', '{"url":"https://models.example.test/v1","reasoning_effort":"low"}', '{}', true, 2, 10),
		('org', 'spare', 'groq', 'llama', '{}', '{}', true, null, null),
		('org', 'odd', 'azure-openai', 'x', '{}', '{}', true, null, null)`)
	seed(t, db, `insert into llm_task_route (org_id, task, model_id, fallback_model_id)
		select 'org', 'turn', (select id from llm_model where name = 'main'), (select id from llm_model where name = 'spare')`)
	cfg = resolve()
	if cfg == nil || cfg.Route.Model != "qwen3" || cfg.Route.URL != "https://models.example.test/v1" || cfg.Route.Price == nil ||
		cfg.Route.Price.OutputUSDPer1M != 10 || cfg.ReasoningEffort != "low" || cfg.Fallback == nil || cfg.Fallback.Provider != "groq" || cfg.Fallback.Price != nil {
		t.Fatalf("turn route: %+v %+v", cfg, cfg.Fallback)
	}
	seed(t, db, `update llm_task_route set fallback_model_id = (select id from llm_model where name = 'odd') where org_id = 'org' and task = 'turn'`)
	if cfg := resolve(); cfg == nil || cfg.Fallback != nil {
		t.Fatalf("a fallback that fails its route is left out: %+v", cfg)
	}
}
