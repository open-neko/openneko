package webfront

import (
	"log"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

// eventScrubber is the worker's CoreSecrets scrubber: every value in the
// secrets file, read again when the file changes.
type eventScrubber struct {
	mu      sync.Mutex
	mtime   time.Time
	size    int64
	read    bool
	scrub   func(string) string
	pattern *regexp.Regexp
	values  []string
}

func (a *API) runEventScrubber() func(string) string {
	scrub, _, _ := a.runEventSecrets()
	return scrub
}

// runEventSecrets returns the scrubber, its pattern and the values it
// matches; all are nil when no secret is long enough to scrub.
func (a *API) runEventSecrets() (func(string) string, *regexp.Regexp, []string) {
	e := &a.eventScrub
	e.mu.Lock()
	defer e.mu.Unlock()
	var mtime time.Time
	var size int64
	if info, err := os.Stat(secrets.Path(a.SecretsDir)); err == nil {
		mtime, size = info.ModTime(), info.Size()
	}
	if e.read && mtime.Equal(e.mtime) && size == e.size {
		return e.scrub, e.pattern, e.values
	}
	values, err := secrets.AllValuesFull(a.SecretsDir)
	if err != nil {
		log.Printf("[secrets] reload failed: %v", err)
		if e.read {
			return e.scrub, e.pattern, e.values
		}
	}
	e.mtime, e.size, e.read, e.scrub = mtime, size, true, nil
	e.pattern, e.values = secretPattern(values)
	if e.pattern != nil {
		rx := e.pattern
		e.scrub = func(s string) string { return rx.ReplaceAllLiteralString(s, "[REDACTED]") }
	}
	return e.scrub, e.pattern, e.values
}

// scrubRunEvent is scrubAgentEvent() in packages/llm/src/work/secret-scrubber.ts.
func scrubRunEvent(scrub func(string) string, event any) any {
	if scrub == nil {
		return event
	}
	e, ok := event.(*jsObj)
	if !ok {
		if e, ok = jsFromGo(event).(*jsObj); !ok {
			return event
		}
	}
	out := &jsObj{}
	for _, k := range e.ordered() {
		out.set(k, e.vals[k])
	}
	str := func(key string) {
		if s, ok := out.vals[key].(string); ok {
			out.vals[key] = scrub(s)
		}
	}
	deep := func(key string) {
		if v, ok := out.vals[key]; ok {
			out.vals[key] = scrubJS(scrub, v)
		}
	}
	switch out.vals["type"] {
	case "message", "interim":
		str("content")
	case "tool_start":
		deep("input")
		str("label")
	case "tool_delta":
		deep("delta")
	case "tool_end":
		deep("result")
		str("error")
		str("label")
	case "surface":
		deep("messages")
	case "artifact":
		deep("artifact")
	case "status", "progress", "error":
		str("message")
		str("content")
	case "done":
		deep("result")
	case "needs_input":
		str("question")
		if options, ok := out.vals["options"].([]any); ok {
			scrubbed := make([]any, len(options))
			for i, o := range options {
				if s, ok := o.(string); ok {
					scrubbed[i] = scrub(s)
				} else {
					scrubbed[i] = o
				}
			}
			out.vals["options"] = scrubbed
		}
		if _, ok := out.vals["questions"].([]any); ok {
			deep("questions")
		}
		str("reason")
	case "output_emit":
		return event
	case "action_request_emit":
		str("intent")
		str("summary")
	default:
		return scrubJS(scrub, e)
	}
	return out
}
