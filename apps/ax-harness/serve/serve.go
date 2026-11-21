// Package serve runs agent turns for the worker over a local socket.
//
// One connection is one turn. Both sides write newline-delimited JSON frames:
//
//	worker → serve  {"type":"start","environ":[...],"spec":{...},"bridge":true,"model_key_env":"MODEL_API_KEY","model_keys":{...}}
//	worker → serve  {"type":"mcp","message":{...}}    the worker's tool server
//	serve → worker  {"type":"event","event":{...}}    a harness lifecycle event
//	serve → worker  {"type":"mcp","message":{...}}    the harness's tool client
//	serve → worker  {"type":"exit","code":0,"error":""}
//
// A host model call is one connection too:
//
//	host → serve   {"type":"generate","generate":{...},"model_key":"...","fallback_key":"..."}
//	serve → host   {"type":"result","result":{...}}
//	serve → host   {"type":"exit","code":0,"error":""}
//
// An embedding call has the same shape, with {"type":"embed","embed":{...},"model_key":"..."}.
//
// A status frame, {"type":"status"}, gets one {"type":"status","result":{"require_door":true,"key_env":[]}}.
//
// Closing the connection cancels the turn. The worker sends a key door token
// in place of each model key and door URLs in place of the provider URLs; the
// door in the control zone holds the keys. A token never goes into the run's
// environment, so the run's tools and terminal never see it.
package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/embedding"
	"github.com/open-neko/openneko/apps/ax-harness/harness"
	"github.com/open-neko/openneko/apps/ax-harness/inference"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

const (
	maxFrameBytes = 32 << 20
	defaultTurns  = 32
)

var keyEnvName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,126}_API_KEY$`)

// Frame is one line of the protocol.
type Frame struct {
	Type        string            `json:"type"`
	Environ     []string          `json:"environ,omitempty"`
	Spec        json.RawMessage   `json:"spec,omitempty"`
	Bridge      bool              `json:"bridge,omitempty"`
	ModelKeyEnv string            `json:"model_key_env,omitempty"`
	ModelKeys   map[string]string `json:"model_keys,omitempty"`
	ModelKey    string            `json:"model_key,omitempty"`
	FallbackKey string            `json:"fallback_key,omitempty"`
	Generate    json.RawMessage   `json:"generate,omitempty"`
	Embed       json.RawMessage   `json:"embed,omitempty"`
	Result      json.RawMessage   `json:"result,omitempty"`
	Event       json.RawMessage   `json:"event,omitempty"`
	Message     json.RawMessage   `json:"message,omitempty"`
	Code        *int              `json:"code,omitempty"`
	Error       string            `json:"error,omitempty"`
	Runner      *RunnerSetup      `json:"runner,omitempty"`
}

// Server runs turns for connections on a listener.
type Server struct {
	// Getenv reads the host's environment for PATH.
	Getenv func(string) string
	// RequireDoor refuses a frame that carries a model key: only key door
	// tokens and "none" pass.
	RequireDoor bool
	// KeyEnv names the model key variables that serve's environment held at
	// start. A status frame reports them.
	KeyEnv []string
	// Confinement is how the OS confines the part of serve that runs turns;
	// NetworkConfinement is the part that holds the network. A status frame
	// reports both.
	Confinement        json.RawMessage
	NetworkConfinement json.RawMessage
	// MaxTurns bounds the turns that run at once; more are refused.
	MaxTurns int
	// Sandbox runs every turn's terminal commands. The worker cannot set it.
	Sandbox sandbox.Config
	// Runner, when set, is the command that runs each turn in a process of
	// its own (see RunRunner); RunnerEnv is that process's whole environment.
	Runner    []string
	RunnerEnv []string
	// RunnerConfinement is how the OS confines each runner; a status frame
	// reports it. ConfineRunner makes each runner confine itself.
	RunnerConfinement json.RawMessage
	ConfineRunner     bool
	// TurnsOff, when set, refuses every agent turn with this text. Host
	// model calls and embedding calls still run.
	TurnsOff string
	// Host, HostID and Kernel name this system in a status frame.
	Host, HostID, Kernel string
	// Log takes serve's own lines; nil writes to stderr.
	Log io.Writer
	// RefusePeer, when set, closes a connection from a process for which it
	// returns true, one whose peer the OS does not report, and one whose
	// peer exited before the check ended. It gets pid 0 for a process
	// outside this pid namespace.
	RefusePeer func(pid int) bool
	// Run runs one turn; tests replace it.
	Run      func(context.Context, harness.Turn) (int, error)
	Generate func(context.Context, inference.Request, string, string) (inference.Result, error)
	Embed    func(context.Context, embedding.Request, string) (embedding.Result, error)

	runnerStarted func(*os.Process)
	runnerLog     io.Writer
	runnerSeq     atomic.Int64
	testOnly      bool
	confine       func(environ []string) error
	limit         func(spec json.RawMessage) error

	mu      sync.Mutex
	running int
	wg      sync.WaitGroup
}

// Serve accepts turns until ctx ends, then cancels the running turns and
// waits for them.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	s.defaults()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			s.wg.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if s.RefusePeer != nil {
			pid, alive, ok := peer(conn)
			refused := !ok || s.RefusePeer(pid)
			if ok && !alive() {
				refused = true
			}
			if refused {
				fmt.Fprintf(s.logOut(), "serve refused a connection on its agent socket from process %d\n", pid)
				_ = conn.Close()
				continue
			}
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(ctx, conn)
		}()
	}
}

func (s *Server) defaults() {
	if s.Run == nil {
		s.Run = harness.RunTurn
	}
	if s.Generate == nil {
		s.Generate = inference.Generate
	}
	if s.Embed == nil {
		s.Embed = embedding.Embed
	}
	if s.Getenv == nil {
		s.Getenv = func(string) string { return "" }
	}
	if s.MaxTurns <= 0 {
		s.MaxTurns = defaultTurns
	}
}

// NotConfined refuses a turn that would run outside a confined runner.
const NotConfined = "agent turns run only in a confined agent runner, and this serve has none"

// confinesTurns tells whether a turn runs in a runner that confines itself:
// serve starts runners that confine, or this is such a runner.
func (s *Server) confinesTurns() bool {
	if s.testOnly {
		return true
	}
	if len(s.Runner) > 0 {
		return s.ConfineRunner
	}
	return s.confine != nil
}

// RunUnconfinedForTest lets a test run turns with no confinement. It panics
// outside a test binary.
func (s *Server) RunUnconfinedForTest() *Server {
	if !testing.Testing() {
		panic("serve.Server.RunUnconfinedForTest works only in a test binary")
	}
	s.testOnly = true
	return s
}

func (s *Server) logOut() io.Writer {
	if s.Log != nil {
		return s.Log
	}
	return os.Stderr
}

func (s *Server) acquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running >= s.MaxTurns {
		return false
	}
	s.running++
	return true
}

func (s *Server) release() {
	s.mu.Lock()
	s.running--
	s.mu.Unlock()
}

func (s *Server) handle(parent context.Context, conn net.Conn) {
	defer conn.Close()
	s.handleStream(parent, conn, bufio.NewReaderSize(conn, 64<<10))
}

func (s *Server) handleStream(parent context.Context, conn io.Writer, reader *bufio.Reader) {
	out := &frameWriter{enc: json.NewEncoder(conn)}
	exit := func(code int, err error) {
		frame := Frame{Type: "exit", Code: &code}
		if err != nil {
			frame.Error = err.Error()
		}
		_ = out.write(frame)
	}
	start, err := readFrame(reader)
	if err == nil && start.Type == "status" {
		runner := "in-process"
		if len(s.Runner) > 0 {
			runner = "process"
		}
		result, _ := json.Marshal(Status{RequireDoor: s.RequireDoor, KeyEnv: append([]string{}, s.KeyEnv...), Confinement: s.Confinement, NetworkConfinement: s.NetworkConfinement, Runner: runner,
			RunnerConfinement: s.RunnerConfinement, TurnsOff: s.TurnsOff, Host: s.Host, HostID: s.HostID, Kernel: s.Kernel})
		_ = out.write(Frame{Type: "status", Result: result})
		return
	}
	if err == nil && s.RequireDoor && (!onlyDoorTokens(start) || environHoldsAKey(start.Environ)) {
		exit(2, errors.New("openneko serve takes no model key: the frame must carry a key door token"))
		return
	}
	if err == nil && start.Type == "generate" {
		var request inference.Request
		if len(start.Generate) == 0 || json.Unmarshal(start.Generate, &request) != nil {
			exit(2, errors.New("expected a generate frame with a request"))
			return
		}
		s.call(parent, reader, out, exit, func(ctx context.Context) (any, error) {
			return s.Generate(ctx, request, start.ModelKey, start.FallbackKey)
		})
		return
	}
	if err == nil && start.Type == "embed" {
		var request embedding.Request
		if len(start.Embed) == 0 || json.Unmarshal(start.Embed, &request) != nil {
			exit(2, errors.New("expected an embed frame with a request"))
			return
		}
		s.call(parent, reader, out, exit, func(ctx context.Context) (any, error) {
			return s.Embed(ctx, request, start.ModelKey)
		})
		return
	}
	if err != nil || start.Type != "start" || len(start.Spec) == 0 {
		exit(2, errors.New("expected a start frame with a run specification"))
		return
	}
	if s.TurnsOff != "" {
		exit(2, errors.New(s.TurnsOff))
		return
	}
	if !s.confinesTurns() {
		exit(2, errors.New(NotConfined))
		return
	}
	if !s.acquire() {
		exit(2, errors.New("serve is at its turn limit"))
		return
	}
	defer s.release()
	if len(s.Runner) > 0 {
		if err := s.runInRunner(parent, conn, reader, start); err != nil {
			exit(2, err)
		}
		return
	}
	if s.limit != nil {
		if err := s.limit(start.Spec); err != nil {
			exit(2, fmt.Errorf("the agent runner could not limit itself, so the turn did not run: %w", err))
			return
		}
	}
	if s.confine != nil {
		if err := s.confine(start.Environ); err != nil {
			exit(2, fmt.Errorf("the agent runner could not confine itself, so the turn did not run: %w", err))
			return
		}
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	turn := harness.Turn{
		Environ: s.environ(start.Environ),
		Secrets: s.secrets(start.ModelKeyEnv, start.ModelKeys),
		Sandbox: s.Sandbox,
		In:      bytes.NewReader(start.Spec),
		Out:     &lineWriter{emit: func(line []byte) error { return out.write(Frame{Type: "event", Event: line}) }},
	}
	var toHarness *io.PipeWriter
	if start.Bridge {
		var fromWorker *io.PipeReader
		fromWorker, toHarness = io.Pipe()
		turn.Bridge = &bridgeStream{
			PipeReader: fromWorker,
			out:        &lineWriter{emit: func(line []byte) error { return out.write(Frame{Type: "mcp", Message: line}) }},
		}
	}
	// The worker's frames after start are tool traffic; its hang-up cancels.
	go func() {
		defer cancel()
		for {
			frame, err := readFrame(reader)
			if err != nil {
				if toHarness != nil {
					_ = toHarness.CloseWithError(io.ErrClosedPipe)
				}
				return
			}
			if frame.Type == "mcp" && toHarness != nil && len(frame.Message) > 0 {
				if _, err := toHarness.Write(append(append([]byte{}, frame.Message...), '\n')); err != nil {
					return
				}
			}
		}
	}()
	code, runErr := s.Run(ctx, turn)
	exit(code, runErr)
}

func (s *Server) call(parent context.Context, reader *bufio.Reader, out *frameWriter, exit func(int, error), run func(context.Context) (any, error)) {
	if !s.acquire() {
		exit(2, errors.New("serve is at its turn limit"))
		return
	}
	defer s.release()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		defer cancel()
		_, _ = io.Copy(io.Discard, reader)
	}()
	result, err := run(ctx)
	if err != nil {
		exit(1, err)
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		exit(1, err)
		return
	}
	if out.write(Frame{Type: "result", Result: raw}) == nil {
		exit(0, nil)
	}
}

// environ drops what the worker may not set: model credentials and the
// sandbox come from serve, and the tool bridge runs over the socket.
func (s *Server) environ(in []string) []string {
	out := make([]string, 0, len(in)+1)
	hasPath := false
	for _, entry := range in {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" || name == "OPENNEKO_MCP_BRIDGE" || strings.HasPrefix(name, "OPENNEKO_SANDBOX_") || keyEnvName.MatchString(name) {
			continue
		}
		hasPath = hasPath || name == "PATH"
		out = append(out, entry)
	}
	if path := s.Getenv("PATH"); !hasPath && path != "" {
		out = append(out, "PATH="+path)
	}
	return out
}

// RunnerSetup is the first frame on a runner's link: what serve decides for
// the turn and the runner cannot.
type RunnerSetup struct {
	RequireDoor bool   `json:"require_door"`
	Path        string `json:"path,omitempty"`
	SandboxURL  string `json:"sandbox_url,omitempty"`
	// SandboxRun is the run that serve opened in the sandbox; the runner
	// holds no sandbox key.
	SandboxRun string `json:"sandbox_run,omitempty"`
	// Confine makes the runner confine itself before the turn's agent code
	// runs, and refuse the turn when it cannot.
	Confine bool `json:"confine,omitempty"`
	// Turns is how many runners serve runs at once. They share the
	// account's task limit, so each runner leaves room for all of them.
	Turns int `json:"turns,omitempty"`
}

// Status is serve's answer to a status frame: whether it refuses a frame
// with a model key, the key variables its environment held at start, and
// how the OS confines it.
type Status struct {
	RequireDoor bool            `json:"require_door"`
	KeyEnv      []string        `json:"key_env"`
	Confinement json.RawMessage `json:"confinement,omitempty"`
	// NetworkConfinement is how the OS confines the part of serve that holds
	// the network: the files service and the GraphJin engines.
	NetworkConfinement json.RawMessage `json:"network_confinement,omitempty"`
	// Runner is "process" when each turn runs in an agent runner process.
	Runner string `json:"runner,omitempty"`
	// RunnerConfinement is how the OS confines each agent runner.
	RunnerConfinement json.RawMessage `json:"runner_confinement,omitempty"`
	// TurnsOff tells why serve refuses every agent turn.
	TurnsOff string `json:"turns_off,omitempty"`
	// Host, HostID and Kernel name the system that runs the agent turns.
	Host   string `json:"host,omitempty"`
	HostID string `json:"host_id,omitempty"`
	Kernel string `json:"kernel,omitempty"`
}

func onlyDoorTokens(frame Frame) bool {
	values := []string{frame.ModelKey, frame.FallbackKey}
	for _, v := range frame.ModelKeys {
		values = append(values, v)
	}
	for _, v := range values {
		if v != "" && v != "none" && !keydoor.IsToken(v) {
			return false
		}
	}
	return true
}

var secretEnvNames = map[string]bool{
	"HF_TOKEN": true, "VERTEX_ACCESS_TOKEN": true, "GOOGLE_VERTEX_ACCESS_TOKEN": true, "GOOGLE_APPLICATION_CREDENTIALS": true,
	"AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AZURE_OPENAI_KEY": true, "NEKO_SECRET_KEY": true,
}

var databaseURLEnvName = regexp.MustCompile(`^([A-Z0-9_]+_)?DATABASE_URL(_FILE)?$`)

// keyShaped matches the forms of the provider keys and cloud credentials
// that the product knows.
var keyShaped = regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{35}|xai-[A-Za-z0-9]{20,}|gsk_[A-Za-z0-9]{20,}|hf_[A-Za-z0-9]{30,}|(AKIA|ASIA)[0-9A-Z]{16}|ya29\.[0-9A-Za-z_-]{20,})|-----BEGIN [A-Z ]*PRIVATE KEY-----`)

func secretEnvName(name string) bool {
	return keyEnvName.MatchString(name) || secretEnvNames[name] || (!strings.HasPrefix(name, "GJ_") && databaseURLEnvName.MatchString(name))
}

// KeyEnvNames lists the variables in environ that can hold a model,
// embedding or cloud key, a database URL or the secret key. Serve must hold
// none of them.
func KeyEnvNames(environ []string) []string {
	var names []string
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if ok && value != "" && secretEnvName(name) {
			names = append(names, name)
		}
	}
	return names
}

func environHoldsAKey(environ []string) bool {
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		if keyShaped.MatchString(value) || (secretEnvName(name) && value != "" && value != "none" && !keydoor.IsToken(value)) {
			return true
		}
	}
	return false
}

// secrets reads model keys from the turn's frame only. HARNESS_MODEL_API_KEY
// reads the variable the worker named; route keys read their own names.
func (s *Server) secrets(modelKeyEnv string, keys map[string]string) func(string) string {
	return func(name string) string {
		if name == "HARNESS_MODEL_API_KEY" {
			if modelKeyEnv == "" {
				modelKeyEnv = "MODEL_API_KEY"
			}
			name = modelKeyEnv
		}
		if !keyEnvName.MatchString(name) {
			return ""
		}
		return keys[name]
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, chunk...)
		if len(line) > maxFrameBytes {
			return nil, errors.New("frame too large")
		}
		if !isPrefix {
			return line, nil
		}
	}
}

func readFrame(r *bufio.Reader) (Frame, error) {
	line, err := readLine(r)
	if err != nil {
		return Frame{}, err
	}
	var frame Frame
	if err := json.Unmarshal(line, &frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

// frameWriter serialises frames from the turn and its tool client.
type frameWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (w *frameWriter) write(frame Frame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enc.Encode(frame)
}

// lineWriter passes each complete JSON line written to it to emit.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	emit func([]byte) error
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := bytes.TrimSpace(w.buf[:i])
		w.buf = w.buf[i+1:]
		if len(line) == 0 {
			continue
		}
		if err := w.emit(append([]byte{}, line...)); err != nil {
			return 0, err
		}
	}
}

func (w *lineWriter) Close() error { return nil }

// bridgeStream is the harness's end of the tool bridge.
type bridgeStream struct {
	*io.PipeReader
	out *lineWriter
}

func (b *bridgeStream) Write(p []byte) (int, error) { return b.out.Write(p) }
func (b *bridgeStream) Close() error                { return b.PipeReader.Close() }

// runnerFolderVars name the folders of a turn that its runner reads and
// writes: all of them lie in the turn's staged workspace.
var runnerFolderVars = []string{"OPENNEKO_HARNESS_WORKSPACE_DIR", "OPENNEKO_HARNESS_UPLOADS_DIR", "OPENNEKO_MCP_SKILLS_ROOT", "RUN_DIR", "ARTIFACT_DIR"}

// RunnerFolders lists the folders that the turn of environ uses.
func RunnerFolders(environ []string) []string {
	var out []string
	for _, name := range runnerFolderVars {
		dir := ""
		for _, entry := range environ {
			if key, value, ok := strings.Cut(entry, "="); ok && key == name {
				dir = value
			}
		}
		if filepath.IsAbs(dir) {
			out = append(out, filepath.Clean(dir))
		}
	}
	return out
}
