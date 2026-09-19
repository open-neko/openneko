// Package agent owns the lifecycle of one bounded, headless AxAgent run.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	ax "github.com/ax-llm/ax/packages/go"
	axgoja "github.com/ax-llm/ax/packages/go/runtime/goja"
)

// Spec is trusted host input. It cannot select credentials, endpoints or capabilities.
type Spec struct {
	Version int    `json:"version"`
	RunID   string `json:"run_id"`
	InputID string `json:"input_id"`
	Prompt  string `json:"prompt"`
}

type Result struct {
	Kind        string            `json:"kind,omitempty"`
	Delegations []json.RawMessage `json:"delegations,omitempty"`
	Status      string            `json:"status"`
	Answer      string            `json:"answer,omitempty"`
	Code        string            `json:"code,omitempty"`
}

type Event struct {
	Data        json.RawMessage `json:"data,omitempty"`
	Error       string          `json:"error,omitempty"`
	Version     int             `json:"version"`
	RunID       string          `json:"run_id"`
	InputID     string          `json:"input_id"`
	Sequence    uint64          `json:"sequence"`
	Type        string          `json:"type"`
	SpanID      uint64          `json:"span_id,omitempty"`
	ParentID    uint64          `json:"parent_id,omitempty"`
	OperationID uint64          `json:"operation_id,omitempty"`
	Name        string          `json:"name,omitempty"`
	DurationMS  int64           `json:"duration_ms,omitempty"`
	Result      *Result         `json:"result,omitempty"`
}

// Run emits ordered lifecycle metadata and one terminal result if the sink remains
// writable. Sink failure cancels admission and returns an error. Events are not a
// durable journal; InputID is correlation, not yet persistent deduplication.
func Run(ctx context.Context, spec Spec, client ax.AIClient, lookup func(context.Context, string) (json.RawMessage, error), emit func(Event) error) (Result, error) {
	if spec.Version != 1 || strings.TrimSpace(spec.RunID) == "" || len(spec.RunID) > 128 || strings.TrimSpace(spec.InputID) == "" || len(spec.InputID) > 128 || strings.TrimSpace(spec.Prompt) == "" || len(spec.Prompt) > 65536 || client == nil || emit == nil {
		return Result{}, fmt.Errorf("invalid run specification")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	events := &recorder{spec: spec, emit: emit, cancel: cancel}
	events.send(Event{Type: "run.started"})
	result := Result{Status: "failed", Kind: "failure", Code: "model_failed"}
	var delegations []json.RawMessage
	var operationID uint64
	lookupFailed := false
	if events.err == nil && ctx.Err() == nil {
		runtime := axgoja.NewRuntime()
		if lookup != nil {
			runtime = axgoja.NewRuntime(axgoja.WithCallable("lookup", func(value ax.Value) (ax.Value, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				instruction, ok := value.(string)
				if !ok || strings.TrimSpace(instruction) == "" || len(instruction) > 8000 {
					return nil, fmt.Errorf("lookup requires a bounded instruction string")
				}
				if operationID >= 4 {
					lookupFailed = true
					return ax.Object("error", "lookup_limit_exceeded"), nil
				}
				operationID++
				events.send(Event{Type: "tool.started", Name: "lookup", OperationID: operationID})
				finished := Event{Type: "tool.finished", Name: "lookup", OperationID: operationID}
				defer func() { events.send(finished) }()
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				raw, err := lookup(ctx, instruction)
				if err != nil || ctx.Err() != nil {
					finished.Error = "lookup_failed"
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if err != nil {
					lookupFailed = true
					return ax.Object("error", "lookup_failed"), nil
				}
				var result ax.Value
				if len(raw) > 262144 || json.Unmarshal(raw, &result) != nil {
					finished.Error = "invalid_lookup_result"
					lookupFailed = true
					return ax.Object("error", "invalid_lookup_result"), nil
				}
				finished.Data = append(json.RawMessage(nil), raw...)
				delegations = append(delegations, append(json.RawMessage(nil), raw...))
				return result, nil
			}))
		}
		instruction := "Answer using the supplied context. Do not invent tool access."
		if lookup != nil {
			instruction = "For questions needing live data, call lookup(instruction) in JavaScript. This delegates read-only investigation to the server-side data agent. Preserve its evidence, refusal, clarification, partial status and errors; never claim a lookup succeeded when it did not."
		}
		engine := ax.NewAgent("question:string -> answer:string", ax.Object("runtime", runtime, "instruction", instruction, "directResponse", "off", "maxSteps", 8, "validationRetries", 0, "infraRetries", 0))
		output, err := engine.ForwardWithHooks(ctx, client, ax.Object("question", spec.Prompt), ax.Object("maxSteps", 8, "validationRetries", 0, "infraRetries", 0), ax.AxRuntimeHooks{Tracer: events})
		engine.CloseRuntimeSession()
		var providerError ax.AxError
		if errors.As(err, &providerError) && providerError.Status > 0 {
			result.Code = fmt.Sprintf("model_http_%d", providerError.Status)
		}
		if err == nil {
			object, ok := output.(map[string]ax.Value)
			answer, valid := object["answer"].(string)
			if ok && valid && strings.TrimSpace(answer) != "" && len(answer) <= 65536 {
				result = Result{Status: "completed", Kind: "answer", Answer: answer}
			} else {
				result.Code = "invalid_output"
			}
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result = Result{Status: "failed", Code: "deadline_exceeded"}
	} else if ctx.Err() != nil {
		result = Result{Status: "cancelled", Code: "cancelled"}
	}
	result.Delegations = delegations
	if result.Status == "failed" {
		result.Kind = "failure"
	}
	if lookupFailed && result.Status == "completed" {
		result.Kind = "partial"
	}
	for _, raw := range delegations {
		var remote struct {
			Denied   bool   `json:"denied"`
			Error    string `json:"error"`
			Response struct {
				Status string `json:"status"`
			} `json:"response"`
		}
		_ = json.Unmarshal(raw, &remote)
		if result.Status == "completed" && result.Kind != "refusal" {
			switch {
			case remote.Denied || remote.Response.Status == "refused" || remote.Response.Status == "blocked":
				result.Kind = "refusal"
			case remote.Error != "" || remote.Response.Status == "failed" || remote.Response.Status == "error":
				result.Kind = "partial"
			case remote.Response.Status == "needs_input" || remote.Response.Status == "clarification" || remote.Response.Status == "needs_clarification":
				result.Kind = "clarification"
			case remote.Response.Status == "partial":
				result.Kind = "partial"
			}
		}
	}
	events.send(Event{Type: "run.finished", Result: &result})
	events.mu.Lock()
	defer events.mu.Unlock()
	return result, events.err
}

type recorder struct {
	mu         sync.Mutex
	spec       Spec
	emit       func(Event) error
	cancel     context.CancelFunc
	seq, spans uint64
	err        error
}

func (r *recorder) send(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	r.seq++
	e.Version = 1
	e.RunID = r.spec.RunID
	e.InputID = r.spec.InputID
	e.Sequence = r.seq
	if err := r.emit(e); err != nil {
		r.err = err
		r.cancel()
	}
}
func (r *recorder) StartSpan(s ax.AxSpanStart) ax.AxSpan {
	r.mu.Lock()
	r.spans++
	id := r.spans
	r.mu.Unlock()
	parent := uint64(0)
	if p, ok := s.Parent.(*span); ok {
		parent = p.id
	}
	r.send(Event{Type: "span.started", SpanID: id, ParentID: parent, Name: s.Name})
	return &span{r: r, id: id, start: time.Now()}
}

type span struct {
	r     *recorder
	id    uint64
	start time.Time
	once  sync.Once
}

// Raw Ax attributes/events/errors can contain content or credentials. This initial
// projection deliberately exports lifecycle only; collector/usage coverage is pending.
func (*span) SetAttributes(map[string]ax.Value)    {}
func (*span) AddEvent(string, map[string]ax.Value) {}
func (*span) RecordException(error)                {}
func (*span) SetStatus(string, string)             {}
func (s *span) End() {
	s.once.Do(func() {
		s.r.send(Event{Type: "span.finished", SpanID: s.id, DurationMS: time.Since(s.start).Milliseconds()})
	})
}
