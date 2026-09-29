package agent

import (
	"encoding/json"

	ax "github.com/ax-llm/ax/packages/go"
	axgoja "github.com/ax-llm/ax/packages/go/runtime/goja"
)

const handoffName = "harnessEvidence"
const maxHandoffBytes = 256 * 1024

// handoffRuntime keeps distilled evidence in the run's code session. Ax drops
// reserved distiller inputs during its stage patch, so the host restores only
// the model's narrowed evidence under a non-input binding.
type handoffRuntime struct {
	*axgoja.Runtime
	onExecutorError func()
}

func (r *handoffRuntime) CreateSession(globals map[string]ax.Value, options map[string]ax.Value) (ax.CodeSession, error) {
	base, err := r.Runtime.CreateSession(globals, options)
	if err != nil {
		return nil, err
	}
	return &handoffSession{CodeSession: base, onExecutorError: r.onExecutorError}, nil
}

type handoffSession struct {
	ax.CodeSession
	evidence        ax.Value
	patched         bool
	onExecutorError func()
}

func (s *handoffSession) Execute(code string, options map[string]ax.Value) ax.Value {
	result := s.CodeSession.Execute(code, options)
	if s.patched {
		if envelope, ok := result.(map[string]ax.Value); ok && envelope["is_error"] == true && s.onExecutorError != nil {
			s.onExecutorError()
		}
		return result
	}
	step, ok := result.(map[string]ax.Value)
	if !ok {
		return result
	}
	payload, ok := step["completion_payload"].(map[string]ax.Value)
	if !ok {
		payload = step
	}
	if payload["type"] != "final" {
		return result
	}
	s.evidence = nil
	args, ok := payload["args"].([]ax.Value)
	if !ok || len(args) < 2 || args[1] == nil {
		return result
	}
	data, err := json.Marshal(args[1])
	if err != nil || len(data) > maxHandoffBytes {
		return map[string]ax.Value{"kind": "error", "is_error": true,
			"error": "distilled evidence is invalid or too large; narrow it before finalizing"}
	}
	var evidence ax.Value
	if err := json.Unmarshal(data, &evidence); err != nil {
		return map[string]ax.Value{"kind": "error", "is_error": true,
			"error": "distilled evidence is invalid; narrow it before finalizing"}
	}
	s.evidence = evidence
	return result
}

func (s *handoffSession) PatchGlobals(snapshot ax.Value, options map[string]ax.Value) ax.Value {
	s.patched = true
	if s.evidence != nil {
		snapshot = handoffBinding(snapshot, s.evidence)
	}
	return redactHandoff(s.CodeSession.PatchGlobals(snapshot, options))
}

func (s *handoffSession) Inspect(options map[string]ax.Value) ax.Value {
	return redactHandoff(s.CodeSession.Inspect(options))
}

func (s *handoffSession) SnapshotGlobals(options map[string]ax.Value) ax.Value {
	return redactHandoff(s.CodeSession.SnapshotGlobals(options))
}

func handoffBinding(value, evidence ax.Value) ax.Value {
	original, ok := value.(map[string]ax.Value)
	if !ok {
		return value
	}
	out := cloneHandoffMap(original)
	for _, field := range []string{"bindings", "globals"} {
		if bindings, ok := original[field].(map[string]ax.Value); ok {
			next := cloneHandoffMap(bindings)
			next[handoffName] = evidence
			out[field] = next
		}
	}
	if _, ok := out["bindings"]; !ok {
		out[handoffName] = evidence
	}
	return out
}

func redactHandoff(value ax.Value) ax.Value {
	original, ok := value.(map[string]ax.Value)
	if !ok {
		return value
	}
	out := cloneHandoffMap(original)
	if _, ok := out[handoffName]; ok {
		out[handoffName] = "[runtime evidence available]"
	}
	for _, field := range []string{"bindings", "globals"} {
		if bindings, ok := original[field].(map[string]ax.Value); ok {
			next := cloneHandoffMap(bindings)
			if _, ok := next[handoffName]; ok {
				next[handoffName] = "[runtime evidence available]"
			}
			out[field] = next
		}
	}
	return out
}

func cloneHandoffMap(source map[string]ax.Value) map[string]ax.Value {
	out := make(map[string]ax.Value, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
