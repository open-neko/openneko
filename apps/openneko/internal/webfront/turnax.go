package webfront

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

// The Ax backend of packages/llm/src/agent-backends/ax.ts and harness-turn.ts:
// one ax-harness turn in openneko serve. The model keys travel in the start
// frame's own field and never in the run's environment.

type agentLimits struct {
	TimeoutMs, MaxTurns, MaxToolCalls, MaxContinuations int
	ReasoningEffort                                     string
	DelegationMaxIterations, ToolOutputMaxBytes         int
	TerminalTimeoutSeconds                              int
}

func readLimitInt(name string, fallback, lo, hi int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return fallback
	}
	n := int(math.Floor(f))
	if n < lo {
		return lo
	}
	if hi > 0 && n > hi {
		return hi
	}
	return n
}

// agentLimitsFor is agentLimits() in packages/llm/src/agent-limits.ts.
func agentLimitsFor(kind string) agentLimits {
	l := agentLimits{
		MaxTurns:                readLimitInt("OPENNEKO_AGENT_MAX_TURNS", 25, 1, 500),
		DelegationMaxIterations: readLimitInt("OPENNEKO_AGENT_DELEGATION_MAX_ITERATIONS", 50, 1, 0),
		ToolOutputMaxBytes:      readLimitInt("OPENNEKO_AGENT_TOOL_OUTPUT_MAX_BYTES", 50_000, 1_000, 0),
		TerminalTimeoutSeconds:  readLimitInt("OPENNEKO_AGENT_TERMINAL_TIMEOUT_SECONDS", 180, 1, 600),
	}
	if kind == "workflow" {
		l.TimeoutMs = readLimitInt("OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS", 15*60_000, 1_000, 0)
		l.MaxToolCalls = readLimitInt("OPENNEKO_WORKFLOW_MAX_TOOL_CALLS", 150, 1, 0)
		l.MaxContinuations = readLimitInt("OPENNEKO_WORKFLOW_MAX_CONTINUATIONS", 2, 0, 0)
		l.ReasoningEffort = "medium"
		if effort := strings.TrimSpace(os.Getenv("OPENNEKO_WORKFLOW_REASONING_EFFORT")); llm.IsReasoningEffort(effort) {
			l.ReasoningEffort = effort
		}
		return l
	}
	l.TimeoutMs = readLimitInt("OPENNEKO_AGENT_TURN_TIMEOUT_MS", 9*60_000, 1_000, 0)
	return l
}

func (l agentLimits) axSpec(spec *jsObj) {
	steps := l.MaxTurns
	if l.MaxToolCalls > 0 {
		steps = l.MaxToolCalls
	}
	spec.set("timeout_ms", float64(l.TimeoutMs))
	spec.set("max_actor_steps", float64(min(steps, 500)))
	spec.set("max_child_steps", float64(min(l.DelegationMaxIterations, 500)))
	if l.MaxToolCalls > 0 {
		spec.set("max_operations", float64(l.MaxToolCalls))
	}
	if l.ReasoningEffort != "" {
		spec.set("reasoning_effort", l.ReasoningEffort)
	}
}

var turnSectionOpeners = []string{"<operator-profile>", "<bot>", "<workspace>", "<long_term_memory>", "<conversation_so_far>", "<files>"}

// splitStablePrompt is splitStablePrompt() in prompts/stable-prefix.ts: the
// text before the first per-turn section is the same for every turn of the
// org, so providers can cache it.
func splitStablePrompt(prompt string) (string, string) {
	cut := -1
	for _, opener := range turnSectionOpeners {
		if at := strings.Index(prompt, "\n\n"+opener+"\n"); at > 0 && (cut < 0 || at < cut) {
			cut = at
		}
	}
	if cut < 0 {
		return "", prompt
	}
	return prompt[:cut], prompt[cut+2:]
}

func axRunPrompt(spec *jsObj, prompt, userMessage string) {
	stable, turn := splitStablePrompt(prompt)
	question := turn
	if userMessage != "" {
		question = turn + "\n\nCurrent user message:\n" + userMessage
	}
	if stable != "" {
		spec.set("instructions", stable)
	}
	spec.set("prompt", question)
}

const (
	primaryRouteKeyEnv = "PRIMARY_MODEL_API_KEY"
	axRenderToolTitle  = "mcp_neko_ui_render_cards"
)

var axKillGrace = 15 * time.Second

func routeJS(key string, r llm.AxModelRoute, keyEnv string) *jsObj {
	o := jsObjOf("key", key)
	if r.Provider != "" {
		o.set("provider", r.Provider)
	}
	o.set("model", r.Model)
	if r.URL != "" {
		o.set("url", r.URL)
	}
	o.set("api_key_env", keyEnv)
	if r.Options != nil {
		o.set("options", optionsJS(r.Options))
	}
	return o
}

var routeOptionOrder = []string{"resource_name", "deployment_name", "api_version"}

func optionsJS(options map[string]string) *jsObj {
	o := &jsObj{}
	for _, k := range routeOptionOrder {
		if v, ok := options[k]; ok {
			o.set(k, v)
		}
	}
	for k, v := range options {
		if _, ok := o.vals[k]; !ok {
			o.set(k, v)
		}
	}
	return o
}

// fallbackRouteProfile is fallbackRouteProfile(): every stage uses the
// primary route, and a retryable failure moves the call to the fallback.
func fallbackRouteProfile(primary, fallback llm.AxModelRoute) string {
	return jsStringify(jsObjOf("context", "primary", "executor", "primary", "responder", "primary",
		"fallbacks", []any{jsObjOf("from", "primary", "to", "fallback")},
		"routes", []any{routeJS("primary", primary, primaryRouteKeyEnv), routeJS("fallback", fallback, llm.FallbackKeyEnv)}), false)
}

// axTurnOptions is AgentRunOptions for an Ax turn, with what the runner took
// from its own environment set by the caller.
type axTurnOptions struct {
	Prompt, UserMessage, OrgID, ThreadID, Tag string
	Workspace                                 *agentWorkspace
	BackendState                              any
	WantsCards, RichCards                     bool
	Servers                                   []string
	Tools                                     *toolServer
	NetworkHosts                              []string
	Debug, NativeDelegationOff, Shell         bool
	TimeoutMs, MaxToolIterations              int
	ReasoningEffort, PythonPath               string
	Keys                                      map[string]string
	Socket                                    string
	Emit                                      func(ctx context.Context, event any) error
}

type axRunResult struct {
	FinalText, RawText, Status, Error, ErrorCode string
	HasRaw, TimedOut, Degraded                   bool
	BackendState                                 any
}

func (r axRunResult) js() *jsObj {
	o := jsObjOf("finalText", r.FinalText)
	if r.HasRaw {
		o.set("rawText", r.RawText)
	}
	o.set("status", r.Status)
	if r.BackendState != nil {
		o.set("backendState", r.BackendState)
	}
	if r.Error != "" {
		o.set("error", r.Error)
	}
	if r.ErrorCode != "" {
		o.set("errorCode", r.ErrorCode)
	}
	if r.TimedOut {
		o.set("timedOut", true)
	}
	if r.Degraded {
		o.set("degraded", true)
	}
	return o
}

func axModelKey(r llm.AxModelRoute, keys map[string]string) string {
	if r.KeyEnv != "" && keys[r.KeyEnv] != "" {
		return keys[r.KeyEnv]
	}
	if keys["MODEL_API_KEY"] != "" {
		return keys["MODEL_API_KEY"]
	}
	return "none"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

var runIDUnsafe = regexp.MustCompile(`[^\w.:-]`)

func axRunID(tag string) string {
	if tag == "" {
		tag = fmt.Sprintf("ax-%d", time.Now().UnixMilli())
	}
	id := runIDUnsafe.ReplaceAllString(tag, "")
	if len(id) > 128 {
		id = id[:128]
	}
	if id == "" {
		return "ax"
	}
	return id
}

// axEnviron is the part of the runner's environment that serve accepts.
func axEnviron(cfg *llm.AxBackendConfig, o axTurnOptions, cwd string, limits agentLimits) []string {
	env := []string{}
	add := func(name, value string) { env = append(env, name+"="+value) }
	route := cfg.Route
	add("HARNESS_MODEL_PROVIDER", route.Provider)
	add("HARNESS_MODEL", route.Model)
	add("HARNESS_MODEL_URL", route.URL)
	options := ""
	if route.Options != nil {
		options = jsStringify(optionsJS(route.Options), false)
	}
	add("HARNESS_MODEL_OPTIONS", options)
	if cfg.Fallback != nil {
		add("HARNESS_MODEL_ROUTES", fallbackRouteProfile(route, *cfg.Fallback))
	}
	add("OPENNEKO_HARNESS_TERMINAL_TIMEOUT_SECONDS", strconv.Itoa(limits.TerminalTimeoutSeconds))
	add("OPENNEKO_HARNESS_TERMINAL_MAX_OUTPUT", strconv.Itoa(limits.ToolOutputMaxBytes))
	add("OPENNEKO_MCP_ORG_ID", orDefault(o.OrgID, "local"))
	add("OPENNEKO_MCP_THREAD_ID", orDefault(o.ThreadID, orDefault(o.Tag, "local")))
	add("OPENNEKO_HARNESS_WORKSPACE_DIR", cwd)
	if o.Shell {
		add("OPENNEKO_HARNESS_SHELL", "1")
	}
	if o.Workspace != nil {
		if o.Workspace.ThreadUploadsRoot != "" {
			add("OPENNEKO_HARNESS_UPLOADS_DIR", o.Workspace.ThreadUploadsRoot)
		}
		if o.Workspace.SkillsRoot != "" {
			add("OPENNEKO_HARNESS_SKILLS_READ", "1")
			add("OPENNEKO_MCP_SKILLS_ROOT", o.Workspace.SkillsRoot)
		}
		add("RUN_DIR", o.Workspace.RunRoot)
		add("ARTIFACT_DIR", o.Workspace.ArtifactRoot)
	}
	if len(o.NetworkHosts) > 0 {
		add("OPENNEKO_HARNESS_WEB_HOSTS", strings.Join(o.NetworkHosts, ","))
	}
	if o.Debug {
		add("OPENNEKO_HARNESS_DEBUG", "1")
	}
	if !o.NativeDelegationOff {
		add("OPENNEKO_HARNESS_CHILD_TOOLS", "*")
	}
	if o.PythonPath != "" {
		add("PYTHONPATH", o.PythonPath)
	}
	return env
}

type axTurnOutcome struct {
	finalText, rawText, err, errorCode string
	hasRaw, degraded, timedOut         bool
	retrySafe, cancelled               bool
}

// runAx is AxBackend.run(): one turn, and a second when Gemini repeats a
// malformed call before anything reached the user or was written.
func runAx(ctx context.Context, cfg *llm.AxBackendConfig, o axTurnOptions) (axRunResult, error) {
	failed := func(msg string) (axRunResult, error) {
		if err := emitTo(ctx, o.Emit, jsObjOf("type", "error", "message", msg)); err != nil {
			return axRunResult{}, err
		}
		return axRunResult{Status: "failed", BackendState: o.BackendState, Error: msg}, nil
	}
	cancelled := axRunResult{Status: "cancelled", BackendState: o.BackendState}
	if ctx.Err() != nil {
		return cancelled, nil
	}
	if cfg == nil {
		return failed("Ax backend has no model configured. Set the primary provider in admin settings.")
	}
	out, err := runAxOnce(ctx, cfg, o)
	if err == nil && out.err != "" && out.retrySafe && strings.Contains(out.err, "MALFORMED_FUNCTION_CALL") && ctx.Err() == nil {
		if err = emitTo(ctx, o.Emit, jsObjOf("type", "retry", "reason", "malformed_function_call")); err == nil {
			out, err = runAxOnce(ctx, cfg, o)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return cancelled, nil
		}
		var emitErr emitError
		if errors.As(err, &emitErr) {
			return axRunResult{}, emitErr.err
		}
		return failed(err.Error())
	}
	if out.cancelled || ctx.Err() != nil {
		return cancelled, nil
	}
	if out.err != "" {
		if err := emitTo(ctx, o.Emit, jsObjOf("type", "error", "message", out.err)); err != nil {
			return axRunResult{}, err
		}
		return axRunResult{Status: "failed", BackendState: o.BackendState, Error: out.err, ErrorCode: out.errorCode, TimedOut: out.timedOut}, nil
	}
	return axRunResult{FinalText: out.finalText, RawText: out.rawText, HasRaw: out.hasRaw, Status: "completed",
		BackendState: o.BackendState, ErrorCode: out.errorCode, Degraded: out.degraded}, nil
}

type emitError struct{ err error }

func (e emitError) Error() string { return e.err.Error() }

func emitTo(ctx context.Context, emit func(context.Context, any) error, event any) error {
	if emit == nil {
		return nil
	}
	return emit(ctx, event)
}

type axEvent struct {
	Version     *float64        `json:"version"`
	RunID       string          `json:"run_id"`
	Sequence    *float64        `json:"sequence"`
	Type        string          `json:"type"`
	Name        *string         `json:"name"`
	OperationID *float64        `json:"operation_id"`
	Effect      string          `json:"effect"`
	Observed    string          `json:"observed_model"`
	Provider    *string         `json:"provider"`
	Error       string          `json:"error"`
	Data        json.RawMessage `json:"data"`
	Result      json.RawMessage `json:"result"`
}

func runAxOnce(ctx context.Context, cfg *llm.AxBackendConfig, o axTurnOptions) (axTurnOutcome, error) {
	timeoutMs := o.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = agentLimitsFor("chat").TimeoutMs
	}
	runID := axRunID(o.Tag)
	cwd := ""
	if o.Workspace != nil {
		cwd = o.Workspace.OrgRoot
	}
	if cwd == "" {
		scratch, err := os.MkdirTemp("", "neko-ax-")
		if err != nil {
			return axTurnOutcome{}, err
		}
		defer os.RemoveAll(scratch)
		cwd = scratch
	}
	limits := agentLimitsFor("chat")
	environ := axEnviron(cfg, o, cwd, limits)

	spec := jsObjOf("version", 1.0, "run_id", runID, "input_id", runID)
	axRunPrompt(spec, o.Prompt, o.UserMessage)
	if o.UserMessage != "" {
		spec.set("skill_query", o.UserMessage)
	}
	if os.Getenv("OPENNEKO_AX_SKILL_RERANK") == "off" {
		spec.set("skill_rerank", "off")
	}
	spec.set("stream_responses", o.Emit != nil)
	runLimits := limits
	runLimits.TimeoutMs = min(timeoutMs, 1_800_000)
	if o.MaxToolIterations > 0 {
		runLimits.MaxToolCalls = o.MaxToolIterations
	}
	runLimits.ReasoningEffort = orDefault(o.ReasoningEffort, cfg.ReasoningEffort)
	runLimits.axSpec(spec)
	if cfg.ContextWindowTokens > 0 {
		spec.set("context_window_tokens", float64(cfg.ContextWindowTokens))
	}
	if cfg.MaxOutputTokens > 0 {
		spec.set("max_output_tokens", float64(cfg.MaxOutputTokens))
	}
	if o.Debug {
		spec.set("debug", true)
	}

	keyEnv := orDefault(cfg.Route.KeyEnv, "MODEL_API_KEY")
	keys := map[string]string{keyEnv: axModelKey(cfg.Route, o.Keys)}
	if cfg.Fallback != nil {
		keys[primaryRouteKeyEnv] = axModelKey(cfg.Route, o.Keys)
		keys[llm.FallbackKeyEnv] = orNone(o.Keys[llm.FallbackKeyEnv])
	}
	bridge := o.Tools != nil && len(o.Servers) > 0
	modelKeys := jsObjOf(keyEnv, keys[keyEnv])
	for _, name := range []string{primaryRouteKeyEnv, llm.FallbackKeyEnv} {
		if v, ok := keys[name]; ok {
			modelKeys.set(name, v)
		}
	}
	start := jsObjOf("type", "start", "environ", stringsAny(environ), "spec", spec, "bridge", bridge,
		"model_key_env", keyEnv, "model_keys", modelKeys)

	turn, err := dialAxTurn(ctx, o.Socket, start, bridgeTools(bridge, o.Tools))
	if err != nil {
		return axTurnOutcome{}, err
	}
	defer turn.stop()
	reaper := time.AfterFunc(time.Duration(timeoutMs)*time.Millisecond+axKillGrace, turn.stop)
	defer reaper.Stop()

	shownText := false
	var queue *emitQueue
	if o.Emit != nil {
		queue = newEmitQueue(ctx, o.Emit)
		defer queue.close()
	}
	emit := func(event *jsObj) {
		if t := event.vals["type"]; (t == "message" || t == "interim") && event.vals["content"] != "" {
			shownText = true
		}
		if queue != nil {
			queue.push(event)
		}
	}
	drained := func() error {
		if queue == nil {
			return nil
		}
		return queue.drain()
	}
	skillsRoot := ""
	if o.Workspace != nil {
		skillsRoot = o.Workspace.SkillsRoot
	}
	mapper := &axEventMapper{emit: emit, route: cfg.Route, skillsRoot: skillsRoot, debug: o.Debug, textVersion: -1.0, renderCalls: map[string]bool{}}
	var result *jsObj
	protocolError := ""
	last := 0.0
	for raw := range turn.lines() {
		var ev axEvent
		if json.Unmarshal(raw, &ev) != nil {
			if protocolError == "" {
				protocolError = "ax-harness wrote a line that is not JSON"
			}
			continue
		}
		seq := 0.0
		if ev.Sequence != nil {
			seq = *ev.Sequence
		}
		if ev.Version == nil || *ev.Version != 1 || ev.RunID != runID || (ev.Sequence != nil && seq != 0 && seq <= last) {
			if protocolError == "" {
				protocolError = "ax-harness event out of order: " + ev.Type
			}
			continue
		}
		if seq != 0 {
			last = seq
		}
		if ev.Type == "run.finished" {
			result = nil
			if parsed, err := jsParse(ev.Result); err == nil {
				result, _ = parsed.(*jsObj)
			}
			continue
		}
		mapper.handle(ev)
	}
	code := turn.exitCode()
	if err := drained(); err != nil {
		return axTurnOutcome{}, emitError{err}
	}
	if turn.cancelled() || ctx.Err() != nil {
		return axTurnOutcome{cancelled: true}, nil
	}
	if result == nil {
		if protocolError != "" {
			return axTurnOutcome{err: protocolError, errorCode: "protocol"}, nil
		}
		lines := strings.Split(jsTrim(turn.diagnostics()), "\n")
		tail := utf16Tail(strings.Join(lines[max(0, len(lines)-6):], "\n"), 600)
		codeText := "null"
		if code != nil {
			codeText = strconv.Itoa(*code)
		}
		msg := "ax-harness exited (code=" + codeText + ") without a result"
		if tail != "" {
			msg += ": " + tail
		}
		return axTurnOutcome{err: msg, errorCode: "exit"}, nil
	}
	mapper.usage(result.vals["usage"])
	if err := drained(); err != nil {
		return axTurnOutcome{}, emitError{err}
	}
	status, _ := result.vals["status"].(string)
	if status == "cancelled" {
		return axTurnOutcome{cancelled: true}, nil
	}
	answer, _ := result.vals["answer"].(string)
	resultCode, hasCode := result.vals["code"].(string)
	if status != "completed" || jsTrim(answer) == "" {
		reason := "no answer"
		errorCode := "empty_output"
		if hasCode {
			reason, errorCode = resultCode, resultCode
		}
		out := axTurnOutcome{errorCode: errorCode, retrySafe: !shownText && !mapper.wroteState}
		if reason == "deadline_exceeded" {
			out.err = fmt.Sprintf("ax turn exceeded its %ss budget and was terminated (OPENNEKO_AGENT_TURN_TIMEOUT_MS overrides)",
				jsNumberText(jsRound(float64(timeoutMs)/1000)))
			out.timedOut = true
		} else {
			out.err = "ax run failed: " + reason
			if tail := utf16Tail(jsTrim(turn.diagnostics()), 600); tail != "" {
				out.err += ": " + tail
			}
		}
		return out, nil
	}
	mapper.finish(answer)
	finalText := answer
	if o.Emit != nil && o.WantsCards {
		text, messages, issues := extractCardFences(answer, o.RichCards)
		if len(messages) > 0 {
			emit(jsObjOf("type", "surface", "messages", messages))
		}
		if len(issues) > 0 {
			log.Printf("[ax] cards in the answer not shown: %s", strings.Join(issues, "; "))
		}
		finalText = text
	} else {
		finalText, _, _ = extractCardFences(answer, false)
	}
	if err := drained(); err != nil {
		return axTurnOutcome{}, emitError{err}
	}
	if o.Emit != nil {
		text, messages := extractSurfaceMessages(finalText)
		if md := extractMarkdownText(messages); md != "" {
			finalText = md
		} else {
			finalText = text
		}
	}
	out := axTurnOutcome{finalText: jsTrim(finalText), rawText: answer, hasRaw: true}
	if kind, _ := result.vals["kind"].(string); kind == "summary" {
		out.degraded = true
		if hasCode {
			out.errorCode = resultCode
		}
	}
	return out, nil
}

func stringsAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

func utf16Tail(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[len(units)-n:]))
}

func utf16Head(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}

func bridgeTools(bridge bool, tools *toolServer) *toolServer {
	if !bridge {
		return nil
	}
	return tools
}

// axServeTurn is one connection to serve's socket. Hanging up cancels the
// turn in serve.
type axServeTurn struct {
	conn     net.Conn
	frames   chan json.RawMessage
	mu       sync.Mutex
	diag     strings.Builder
	code     *int
	stopped  bool
	userStop bool
	stopOnce sync.Once
	toolsIn  *io.PipeWriter
	cancel   context.CancelFunc
	unwatch  func() bool
}

func dialAxTurn(ctx context.Context, socket string, start *jsObj, tools *toolServer) (*axServeTurn, error) {
	if socket == "" {
		return nil, errors.New("openneko serve unavailable: no serve socket is set")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("openneko serve unavailable: %w", err)
	}
	t := &axServeTurn{conn: conn, frames: make(chan json.RawMessage, 64)}
	toolCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	t.cancel = cancel
	var writeMu sync.Mutex
	send := func(frame any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err := conn.Write([]byte(jsStringify(frame, false) + "\n"))
		return err
	}
	if tools != nil {
		in, w := io.Pipe()
		t.toolsIn = w
		go func() {
			_ = tools.serve(toolCtx, in, &mcpFrameWriter{send: send})
		}()
	}
	t.unwatch = context.AfterFunc(ctx, func() {
		t.mu.Lock()
		t.userStop = true
		t.mu.Unlock()
		t.stop()
	})
	if err := send(start); err != nil {
		t.stop()
		return nil, fmt.Errorf("openneko serve unavailable: %w", err)
	}
	go t.read()
	return t, nil
}

type mcpFrameWriter struct {
	send func(any) error
	buf  []byte
}

func (w *mcpFrameWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			return len(p), nil
		}
		line := w.buf[:i]
		w.buf = w.buf[i+1:]
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		message, err := jsParse(line)
		if err != nil {
			continue
		}
		if err := w.send(jsObjOf("type", "mcp", "message", message)); err != nil {
			return 0, err
		}
	}
}

func (t *axServeTurn) read() {
	defer close(t.frames)
	reader := bufio.NewReaderSize(t.conn, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var f struct {
				Type    string          `json:"type"`
				Event   json.RawMessage `json:"event"`
				Message json.RawMessage `json:"message"`
				Code    *float64        `json:"code"`
				Error   string          `json:"error"`
			}
			if json.Unmarshal(line, &f) != nil {
				t.note("serve wrote a line that is not JSON")
			} else {
				switch f.Type {
				case "event":
					t.frames <- f.Event
				case "mcp":
					if t.toolsIn != nil && len(f.Message) > 0 {
						_, _ = t.toolsIn.Write(append(append([]byte{}, f.Message...), '\n'))
					}
				case "exit":
					if f.Code != nil {
						c := int(*f.Code)
						t.mu.Lock()
						t.code = &c
						t.mu.Unlock()
					}
					if f.Error != "" {
						t.note(f.Error)
					}
					t.stop()
					return
				}
			}
		}
		if err != nil {
			t.stop()
			return
		}
	}
}

func (t *axServeTurn) note(line string) {
	t.mu.Lock()
	t.diag.WriteString(line + "\n")
	t.mu.Unlock()
}

func (t *axServeTurn) lines() <-chan json.RawMessage { return t.frames }

func (t *axServeTurn) stop() {
	t.stopOnce.Do(func() {
		t.mu.Lock()
		t.stopped = true
		t.mu.Unlock()
		_ = t.conn.Close()
		if t.toolsIn != nil {
			_ = t.toolsIn.Close()
		}
		t.cancel()
		if t.unwatch != nil {
			t.unwatch()
		}
	})
}

func (t *axServeTurn) exitCode() *int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.code
}

func (t *axServeTurn) cancelled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.userStop
}

func (t *axServeTurn) diagnostics() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.diag.String()
}

// axEventMapper is AxEventMapper: harness events to AgentEvents.
type axEventMapper struct {
	emit           func(*jsObj)
	route          llm.AxModelRoute
	skillsRoot     string
	debug          bool
	text           string
	textVersion    any
	emittedOutside string
	thought        string
	thoughtCount   int
	observed       *jsObj
	renderCalls    map[string]bool
	wroteState     bool
}

func (m *axEventMapper) summaryProvider() string {
	if m.route.Provider == "anthropic" || m.route.Provider == "google-gemini" {
		return m.route.Provider
	}
	return ""
}

func (m *axEventMapper) handle(ev axEvent) {
	data := &jsObj{}
	var dataValue any
	hasData := len(ev.Data) > 0
	if hasData {
		dataValue, _ = jsParse(ev.Data)
		if o, ok := dataValue.(*jsObj); ok {
			data = o
		}
	}
	text, isText := data.vals["text"].(string)
	opID := "0"
	if ev.OperationID != nil {
		opID = jsNumberText(*ev.OperationID)
	}
	name := "tool"
	if ev.Name != nil {
		name = *ev.Name
	}
	switch ev.Type {
	case "answer.delta":
		if !isText {
			return
		}
		m.flushThought()
		v, present := data.vals["version"]
		if !present || v == nil {
			m.textVersion = 0.0
			m.text = ""
		} else if !jsStrictEqual(v, m.textVersion) {
			m.textVersion = v
			m.text = ""
		}
		m.text += text
		m.emitOutside(outsideFenceText(m.text))
	case "thought.delta":
		if m.summaryProvider() != "" && isText {
			m.thought = utf16Head(m.thought+text, 6_000)
		}
	case "actor.step":
		m.flushThought()
		if isText && jsTrim(text) != "" {
			m.emit(jsObjOf("type", "status", "message", jsTrim(text)))
		}
	case "tool.started":
		m.flushThought()
		if ev.Effect == "durable" {
			m.wroteState = true
		}
		if ev.Name != nil && *ev.Name == axRenderToolTitle {
			m.renderCalls[opID] = true
			return
		}
		e := jsObjOf("type", "tool_start", "id", "ax-op-"+opID, "name", name)
		if hasData {
			e.set("input", dataValue)
		}
		m.emit(e)
	case "tool.finished":
		if m.renderCalls[opID] {
			delete(m.renderCalls, opID)
			if ev.Error == "" {
				return
			}
			m.emit(jsObjOf("type", "tool_start", "id", "ax-op-"+opID, "name", name))
		}
		e := jsObjOf("type", "tool_end", "id", "ax-op-"+opID)
		if hasData {
			e.set("result", dataValue)
		}
		if ev.Error != "" {
			e.set("error", ev.Error)
		}
		m.emit(e)
	case "skill.shortlist":
		var names []string
		if list, ok := dataValue.([]any); ok {
			for _, n := range list {
				if s, ok := n.(string); ok {
					names = append(names, s)
				}
			}
		}
		msg := "Skill guides that may fit: " + strings.Join(names, ", ")
		if ev.Error != "" {
			msg = "Skill search failed: " + ev.Error
		}
		m.emit(jsObjOf("type", "status", "message", msg))
	case "skill.loaded", "skill.used":
		if ev.Name == nil || *ev.Name == "" || m.skillsRoot == "" {
			return
		}
		id, tool := "ax-skill-load-"+*ev.Name, "skill_view"
		if ev.Type == "skill.used" {
			id, tool = "ax-skill-"+*ev.Name, "skill_used"
		}
		m.emit(jsObjOf("type", "tool_start", "id", id, "name", tool, "input", jsObjOf("path", filepath.Join(m.skillsRoot, *ev.Name, "SKILL.md"))))
		m.emit(jsObjOf("type", "tool_end", "id", id))
	case "actor.code":
		if code, ok := data.vals["code"].(string); m.debug && ok {
			failed := ""
			if e, ok := data.vals["error"].(string); ok {
				failed = " failed (" + e + ")"
			}
			m.emit(jsObjOf("type", "status", "message", "actor code"+failed+":\n"+code))
		}
	case "model.route.fallback":
		m.emit(jsObjOf("type", "retry", "reason", "model_route_fallback"))
	case "executor.step.failed":
		m.emit(jsObjOf("type", "retry", "reason", "executor_step_failed"))
	case "model.request.finished":
		if ev.Observed != "" {
			provider := m.route.Provider
			if ev.Provider != nil {
				provider = *ev.Provider
			}
			m.observed = jsObjOf("provider", provider, "model", ev.Observed)
		}
	}
}

func jsStrictEqual(x, y any) bool {
	switch x.(type) {
	case float64, string, bool:
		return x == y
	}
	return false
}

func (m *axEventMapper) finish(answer string) {
	m.flushThought()
	outside := outsideFenceText(answer + "\n")
	if !strings.HasSuffix(answer, "\n") {
		outside = strings.TrimSuffix(outside, "\n")
	}
	m.emitOutside(outside)
}

func (m *axEventMapper) usage(usage any) {
	configured := jsObjOf("provider", m.route.Provider, "model", m.route.Model)
	e := jsObjOf("type", "usage", "source", "outer")
	identity := jsObjOf("configured", configured)
	if m.observed != nil {
		e.set("provider", m.observed.vals["provider"])
		e.set("model", m.observed.vals["model"])
		identity.set("observed", m.observed)
	}
	e.set("modelIdentity", identity)
	u, _ := usage.(*jsObj)
	e.set("usage", normalizeAxUsage(u, &m.route, time.Now()))
	m.emit(e)
}

func (m *axEventMapper) emitOutside(outside string) {
	if !strings.HasPrefix(outside, m.emittedOutside) {
		return
	}
	delta := outside[len(m.emittedOutside):]
	if delta == "" {
		return
	}
	m.emittedOutside = outside
	m.emit(jsObjOf("type", "message", "role", "assistant", "content", delta))
}

func (m *axEventMapper) flushThought() {
	content := jsTrim(m.thought)
	m.thought = ""
	provider := m.summaryProvider()
	if content == "" || provider == "" {
		return
	}
	m.thoughtCount++
	m.emit(jsObjOf("type", "progress", "id", "ax-summary-"+strconv.Itoa(m.thoughtCount), "content", content,
		"source", "provider_summary", "provider", provider))
}

func usageNumber(u *jsObj, key string) (float64, bool) {
	if u == nil {
		return 0, false
	}
	f, ok := u.vals[key].(float64)
	return f, ok
}

// normalizeAxUsage is normalizeAxUsage(): a partial report is scaled up by
// the calls it missed, and a host price beats the harness's catalog price.
func normalizeAxUsage(u *jsObj, route *llm.AxModelRoute, at time.Time) *jsObj {
	if u == nil {
		return jsObjOf("coverage", "unavailable", "missingReasons", []any{"Ax Harness reported no usage"})
	}
	out := &jsObj{}
	for _, pair := range [][2]string{{"input_tokens", "inputTokens"}, {"output_tokens", "outputTokens"}, {"total_tokens", "totalTokens"},
		{"cache_read_tokens", "cacheReadTokens"}, {"cache_write_tokens", "cacheWriteTokens"}, {"reasoning_tokens", "reasoningTokens"}} {
		if v, ok := usageNumber(u, pair[0]); ok {
			out.set(pair[1], v)
		}
	}
	coverage, hasCoverage := u.vals["coverage"].(string)
	requests, _ := usageNumber(u, "requests")
	reported, _ := usageNumber(u, "reported")
	scale := 0.0
	if coverage == "complete" {
		scale = 1
	} else if coverage == "partial" && reported != 0 && requests != 0 {
		scale = requests / reported
	}
	cost, hasCost := usageNumber(u, "cost_micros")
	version, _ := u.vals["catalog_version"].(string)
	pricedBy, _ := u.vals["priced_by"].(string)
	harnessPriced := pricedBy == "catalog" && hasCost && !math.IsInf(cost, 0) && !math.IsNaN(cost) && version != "" &&
		!(route != nil && (route.Price != nil || llm.HasPriceOverride(route.Provider, route.Model, at)))
	tokens := func(key string) int64 { v, _ := usageNumber(u, key); return int64(v) }
	counts := llm.Usage{Input: tokens("input_tokens"), Output: tokens("output_tokens"), CacheRead: tokens("cache_read_tokens"),
		CacheWrite: tokens("cache_write_tokens"), Total: tokens("total_tokens"), Calls: tokens("reported")}
	var usd float64
	var source, pricing string
	priced := false
	if scale > 0 {
		switch {
		case route != nil && route.Price != nil:
			usd, source, pricing, priced = llm.RecordCostUSD(*route.Price, counts), "model_record", "model_record", true
		case harnessPriced:
			usd, source, pricing, priced = cost/1_000_000, "ax_harness_catalog", "ax-go@"+version, true
		case route != nil:
			if usd, source, priced = llm.TokenCostUSD(route.Provider, route.Model, counts, at); priced {
				pricing = llm.PricesVersion()
			}
		}
	}
	if !hasCoverage {
		coverage = "unavailable"
	}
	out.set("coverage", coverage)
	if !priced {
		out.set("costStatus", "unknown")
	} else {
		status := "estimated"
		if usd == 0 {
			status = "included"
		}
		out.set("costStatus", status)
		out.set("estimatedCostUsd", usd*scale)
		out.set("currency", "USD")
		out.set("costSource", source)
		out.set("pricingCatalogVersion", pricing)
	}
	if coverage == "partial" {
		out.set("missingReasons", []any{"The provider reported usage for " + jsNumberText(reported) + " of " + jsNumberText(requests) + " model calls"})
	}
	return out
}
