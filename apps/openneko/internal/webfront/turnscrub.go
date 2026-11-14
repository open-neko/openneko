package webfront

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

// eventScrubber is the worker's CoreSecrets scrubber: every value in the
// secrets file, read again when the file changes.
type eventScrubber struct {
	mu    sync.Mutex
	mtime time.Time
	size  int64
	read  bool
	scrub func(string) string
}

func (a *API) runEventScrubber() func(string) string {
	e := &a.eventScrub
	e.mu.Lock()
	defer e.mu.Unlock()
	var mtime time.Time
	var size int64
	if info, err := os.Stat(secrets.Path(a.SecretsDir)); err == nil {
		mtime, size = info.ModTime(), info.Size()
	}
	if e.read && mtime.Equal(e.mtime) && size == e.size {
		return e.scrub
	}
	values, err := secrets.AllValuesFull(a.SecretsDir)
	if err != nil {
		log.Printf("[secrets] reload failed: %v", err)
		if e.read {
			return e.scrub
		}
	}
	e.mtime, e.size, e.read, e.scrub = mtime, size, true, nil
	for _, v := range values {
		if utf16Len(v) >= 8 {
			e.scrub = secretScrubber(values)
			break
		}
	}
	return e.scrub
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
	case "tool_delta":
		deep("delta")
	case "tool_end":
		deep("result")
		str("error")
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
