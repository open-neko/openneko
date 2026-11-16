package webfront

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// EvalTraceEnv names the file that receives every run's semantic trace with
// its step content. Only an eval install sets it.
const EvalTraceEnv = "OPENNEKO_EVAL_TRACE_FILE"

const traceHostSource = "trusted-host"

// EvalTrace writes one JSON line per trace event: {"event":…,"content":…}.
type EvalTrace struct {
	mu    sync.Mutex
	f     *os.File
	scrub func(string) string
}

// OpenEvalTrace opens path for append with mode 600. An empty path gives a
// nil trace, which records nothing. A path inside one of the refused
// folders is an error, because those folders reach the agent and the users.
func OpenEvalTrace(path string, refused []string, environ []string) (*EvalTrace, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	abs = resolvedPath(abs)
	for _, root := range refused {
		if strings.TrimSpace(root) == "" {
			continue
		}
		r, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		r = resolvedPath(r)
		if rel, err := filepath.Rel(r, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s=%s is inside %s, which the agent or the users can read; choose a path outside it", EvalTraceEnv, path, root)
		}
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	return &EvalTrace{f: f, scrub: evalScrubber(environ)}, nil
}

// EvalTraceRefused lists the folders that hold the runs, the stages that
// serve shares and the users' files.
func EvalTraceRefused(filesDir string) []string {
	return []string{filepath.Dir(filepath.Dir(orgAgentRoot("_"))), agentRunsRoot(), filesDir}
}

// resolvedPath follows the links of the longest part of p that exists.
func resolvedPath(p string) string {
	rest := ""
	for dir := p; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		if filepath.Dir(dir) == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

var evalSecretName = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD)$`)
var evalURLName = regexp.MustCompile(`(?i)URL$`)

// evalScrubber is evalContentScrubber() of the backend eval driver: the
// values of the secret-named variables and the passwords inside URLs.
func evalScrubber(environ []string) func(string) string {
	var values []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		if evalSecretName.MatchString(name) {
			values = append(values, value)
		}
		if evalURLName.MatchString(name) {
			if u, err := url.Parse(value); err == nil && u.User != nil {
				if pw, ok := u.User.Password(); ok && pw != "" {
					values = append(values, pw)
				}
			}
		}
	}
	return secretScrubber(values)
}

func (t *EvalTrace) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.f.Close()
}

func (t *EvalTrace) write(event, content *jsObj) {
	line := &jsObj{}
	line.set("event", event)
	if content != nil {
		line.set("content", content)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.f.WriteString(jsStringify(line, false) + "\n"); err != nil {
		log.Printf("[eval-trace] write failed: %v", err)
	}
}

// register sends the run's trace to the file until the returned function runs.
func (t *EvalTrace) register(runID string) func() {
	if t == nil {
		return func() {}
	}
	unregister := registerTraceSink(runID, t.write, t.scrub)
	traceSinks.Lock()
	if reg := traceSinks.runs[runID]; reg != nil {
		reg.model = t.writeModelRequest
	}
	traceSinks.Unlock()
	return unregister
}

// writeModelRequest writes a line that holds no "event", so a reader of the
// semantic trace skips it.
func (t *EvalTrace) writeModelRequest(record *jsObj) {
	line := jsObjOf("modelRequest", record)
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.f.WriteString(jsStringify(line, false) + "\n"); err != nil {
		log.Printf("[eval-trace] write failed: %v", err)
	}
}

const evalResponderEffortEnv = "OPENNEKO_EVAL_RESPONDER_REASONING_EFFORT"

// evalResponderEffort is the responder effort of an eval trial. It applies
// only to a run that an eval trace records.
func evalResponderEffort(runID string) string {
	effort := strings.TrimSpace(os.Getenv(evalResponderEffortEnv))
	if effort == "" || runID == "" || !traceActive(runID) {
		return ""
	}
	switch effort {
	case "minimal", "low", "medium", "high":
		return effort
	}
	log.Printf("[eval-trace] %s=%q is not minimal, low, medium or high; ignored", evalResponderEffortEnv, effort)
	return ""
}

// traceModelRequest records one finished model request of an eval run: its
// time, its usage and whether it streamed.
func traceModelRequest(b traceBinding, responderEffort string, ev axEvent) {
	traceSinks.Lock()
	reg := traceSinks.runs[b.RunID]
	traceSinks.Unlock()
	if reg == nil || reg.model == nil {
		return
	}
	record := jsObjOf("runId", b.RunID, "runKind", b.Kind, "callId", ev.CallID, "streamed", ev.Streaming, "durationMs", ev.DurationMS)
	if ev.Name != nil {
		record.set("model", *ev.Name)
	}
	if ev.Observed != "" {
		record.set("observedModel", ev.Observed)
	}
	if ev.Error != "" {
		record.set("error", ev.Error)
	}
	if responderEffort != "" {
		record.set("responderReasoningEffort", responderEffort)
	}
	if len(ev.Usage) > 0 {
		if usage, err := jsParse(ev.Usage); err == nil {
			record.set("usage", usage)
		}
	}
	reg.model(record)
}

func traceActive(runID string) bool {
	traceSinks.Lock()
	defer traceSinks.Unlock()
	return traceSinks.runs[runID] != nil
}

// traceHost is recordWorkSemanticHostEvent(): an "ok" event from the host
// with no step content.
func traceHost(b traceBinding, operation string, request any, evidence *jsObj) {
	traceSinks.Lock()
	reg := traceSinks.runs[b.RunID]
	sequence := 0
	if reg != nil {
		sequence = reg.next
		reg.next++
	}
	traceSinks.Unlock()
	if reg == nil {
		return
	}
	defer func() {
		if recover() != nil {
			log.Printf("[work-semantic-trace] emit failed run=%s operation=%s", b.RunID, operation)
		}
	}()
	event := &jsObj{}
	event.set("schemaVersion", traceSchemaVersion)
	event.set("sequence", float64(sequence))
	event.set("timestamp", jsTime(time.Now().UTC()))
	event.set("runId", b.RunID)
	event.set("orgId", b.OrgID)
	event.set("runKind", b.Kind)
	event.set("source", traceHostSource)
	event.set("status", "ok")
	event.set("durationMs", float64(0))
	event.set("requestDigest", traceDigest(request))
	event.set("responseDigest", traceDigest(evidence))
	event.set("operation", operation)
	event.set("evidence", evidence)
	reg.sink(event, nil)
}

// traceMemoryPrefetched records the memories that the turn put in its prompt.
func traceMemoryPrefetched(b traceBinding, memories []memoryOut) {
	if len(memories) == 0 || !traceActive(b.RunID) {
		return
	}
	ids := []any{}
	list := []any{}
	for _, m := range memories {
		ids = append(ids, m.ID)
		item := &jsObj{}
		item.set("id", m.ID)
		item.set("contentDigest", traceDigest(m.Text))
		layer := "team"
		if m.UserID != nil && *m.UserID != "" {
			layer = "personal"
		}
		item.set("layer", layer)
		if m.Scope != "" {
			item.set("scope", m.Scope)
		}
		if m.Kind != "" {
			item.set("kind", m.Kind)
		}
		list = append(list, item)
	}
	evidence := &jsObj{}
	evidence.set("returnedCount", float64(len(list)))
	evidence.set("memories", list)
	traceHost(b, "memory.prefetched", jsObjOf("ids", ids), evidence)
}

// skillTrace records skill.loaded once per skill, when a tool call that
// reads the skill's SKILL.md ends without an error.
type skillTrace struct {
	pending map[string]string
	loaded  map[string]bool
}

func (s *skillTrace) observe(b traceBinding, event any) {
	if !traceActive(b.RunID) {
		return
	}
	if s.pending == nil {
		s.pending, s.loaded = map[string]string{}, map[string]bool{}
	}
	id, _ := jsGet(event, "id").(string)
	switch jsGet(event, "type") {
	case "tool_start":
		if name := detectSkillUse(event); name != "" {
			s.pending[id] = name
		}
	case "tool_end":
		name, ok := s.pending[id]
		delete(s.pending, id)
		if !ok || jsTruthy(jsGet(event, "error")) || s.loaded[name] {
			return
		}
		r, err := orgWorkspace(b.OrgID)
		if err != nil {
			return
		}
		body, err := os.ReadFile(filepath.Join(r.Skills, name, "SKILL.md"))
		if err != nil {
			return
		}
		s.loaded[name] = true
		sum := sha256.Sum256(body)
		evidence := &jsObj{}
		evidence.set("id", name)
		evidence.set("contentDigest", "sha256:"+hex.EncodeToString(sum[:]))
		traceHost(b, "skill.loaded", jsObjOf("id", name), evidence)
	}
}
