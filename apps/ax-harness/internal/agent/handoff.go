package agent

import (
	"encoding/json"
	"fmt"
	"sort"

	ax "github.com/ax-llm/ax/packages/go"
	axgoja "github.com/ax-llm/ax/packages/go/runtime/goja"
)

const handoffName = "harnessEvidence"
const maxHandoffBytes = 256 * 1024
const maxVisibleRuntimeValueBytes = 4096
const maxVisibleRuntimeArrayItems = 64
const maxVisibleRuntimeProjectionBytes = 32768

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
	return limitRuntimeProjection(boundRuntimeValue(out))
}

func limitRuntimeProjection(value ax.Value) ax.Value {
	root, ok := value.(map[string]ax.Value)
	if !ok || runtimeProjectionBytes(root) <= maxVisibleRuntimeProjectionBytes {
		return value
	}
	bindings := root
	if nested, ok := root["bindings"].(map[string]ax.Value); ok {
		bindings = nested
	}
	type bindingSize struct {
		name string
		size int
	}
	ordered := make([]bindingSize, 0, len(bindings))
	for name, item := range bindings {
		encoded, _ := json.Marshal(item)
		ordered = append(ordered, bindingSize{name: name, size: len(encoded)})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].size == ordered[j].size {
			return ordered[i].name < ordered[j].name
		}
		return ordered[i].size > ordered[j].size
	})
	for _, entry := range ordered {
		marker := fmt.Sprintf("[runtime binding omitted: %d bytes; use harnessSavedOperation(id)]", entry.size)
		bindings[entry.name] = marker
		if globals, ok := root["globals"].(map[string]ax.Value); ok {
			globals[entry.name] = marker
		}
		if runtimeProjectionBytes(root) <= maxVisibleRuntimeProjectionBytes {
			return root
		}
	}
	if _, hasBindings := root["bindings"]; hasBindings {
		marker := map[string]ax.Value{"__ax_snapshot_truncated": true}
		return map[string]ax.Value{"version": root["version"], "bindings": marker,
			"globals": marker, "closed": root["closed"]}
	}
	return map[string]ax.Value{"__ax_snapshot_truncated": true}
}

func runtimeProjectionBytes(value ax.Value) int {
	encoded, _ := json.Marshal(value)
	return len(encoded)
}

// Ax Goja persists top-level const/let/var bindings between turns and includes
// them in Inspect and SnapshotGlobals. A saved operation read can therefore
// re-enter model context through an ordinary JS variable even when the tool
// result itself was returned as a short reference. Bound only the projection;
// the code session and authoritative operation checkpoint retain full bytes.
func boundRuntimeValue(value ax.Value) ax.Value {
	switch v := value.(type) {
	case string:
		if len(v) > maxVisibleRuntimeValueBytes {
			return fmt.Sprintf("[runtime value omitted: %d bytes; use harnessSavedOperation(id)]", len(v))
		}
		return v
	case map[string]ax.Value:
		out := make(map[string]ax.Value, len(v))
		for key, item := range v {
			if key == "__ax_stdout" || key == "__ax_stderr" {
				out[key] = "[cumulative runtime log omitted; current turn logs are shown separately]"
				continue
			}
			out[key] = boundRuntimeValue(item)
		}
		return out
	case []ax.Value:
		limit := len(v)
		if limit > maxVisibleRuntimeArrayItems {
			limit = maxVisibleRuntimeArrayItems
		}
		out := make([]ax.Value, 0, limit+1)
		for _, item := range v[:limit] {
			out = append(out, boundRuntimeValue(item))
		}
		if len(v) > limit {
			out = append(out, fmt.Sprintf("[runtime array omitted: %d further items]", len(v)-limit))
		}
		return out
	case []string:
		items := make([]ax.Value, len(v))
		for i, item := range v {
			items[i] = item
		}
		return boundRuntimeValue(items)
	default:
		return value
	}
}

func cloneHandoffMap(source map[string]ax.Value) map[string]ax.Value {
	out := make(map[string]ax.Value, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
