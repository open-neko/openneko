package webfront

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

type thinkingRecording struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Thought  string `json:"thought"`
	Events   []struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"events"`
}

func thinkingRecordings(t *testing.T) []thinkingRecording {
	paths, err := filepath.Glob("../../../ax-harness/internal/agent/testdata/thinking/*.json")
	if err != nil || len(paths) < 10 {
		t.Fatalf("recordings %v %v", paths, err)
	}
	var out []thinkingRecording
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var r thinkingRecording
		if err := json.Unmarshal(raw, &r); err != nil || len(r.Events) == 0 {
			t.Fatalf("%s: %v", path, err)
		}
		out = append(out, r)
	}
	return out
}

func thinkingMapper(provider string, events *[]*jsObj) *axEventMapper {
	return &axEventMapper{emit: func(e *jsObj) { *events = append(*events, e) }, route: llm.AxModelRoute{Provider: provider},
		textVersion: -1.0, renderCalls: map[string]bool{}}
}

func TestThinkingIsOneContractForEveryProvider(t *testing.T) {
	shapes := map[string]string{}
	for _, r := range thinkingRecordings(t) {
		t.Run(r.Name, func(t *testing.T) {
			var events []*jsObj
			m := thinkingMapper(r.Provider, &events)
			var thought strings.Builder
			var progress []string
			for _, e := range r.Events {
				before := len(events)
				m.handle(axEvent{Type: e.Type, Data: e.Data})
				if e.Type != "thought.delta" {
					continue
				}
				var data struct{ Text string }
				_ = json.Unmarshal(e.Data, &data)
				if strings.TrimSpace(thought.String()+data.Text) != "" && len(events) == before {
					t.Fatalf("thinking text %q waited for a later event", data.Text)
				}
				thought.WriteString(data.Text)
			}
			m.finish("done")
			status, answerAt := false, -1
			for i, e := range events {
				switch e.vals["type"] {
				case "status":
					status = status || e.vals["message"] == "Check the data"
				case "message":
					if answerAt < 0 {
						answerAt = i
					}
				case "progress":
					if answerAt >= 0 {
						t.Fatalf("thinking came after the answer: %v", jsStringify(e, false))
					}
					if e.vals["id"] != "ax-summary-1" || e.vals["source"] != "provider_summary" || e.vals["provider"] != r.Provider {
						t.Fatalf("progress %s", jsStringify(e, false))
					}
					progress = append(progress, e.vals["content"].(string))
					shapes[strings.Join(e.ordered(), ",")] = r.Name
				}
			}
			if !status || answerAt < 0 {
				t.Fatalf("no step status or no answer: %d events", len(events))
			}
			if got, want := strings.Join(progress, ""), jsTrimStart(r.Thought); got != want {
				t.Fatalf("thinking %q, want %q", got, want)
			}
			if r.Thought != "" && len(progress) < 2 {
				t.Fatalf("thinking arrived in %d event; want each delta", len(progress))
			}
		})
	}
	if len(shapes) != 1 {
		t.Fatalf("progress shapes differ: %v", shapes)
	}
}

func TestThinkingNotesSplitAtEachStepAndVersion(t *testing.T) {
	var events []*jsObj
	m := thinkingMapper("anthropic", &events)
	feed := func(typ, data string) { m.handle(axEvent{Type: typ, Data: json.RawMessage(data)}) }
	feed("thought.delta", `{"version":0,"text":"\n  First "}`)
	feed("thought.delta", `{"version":0,"text":"note."}`)
	feed("actor.step", `{"text":"Read the orders"}`)
	feed("thought.delta", `{"version":0,"text":"Second note."}`)
	feed("thought.delta", `{"version":1,"text":"Third note."}`)
	feed("thought.delta", `{"text":"Fourth "}`)
	feed("thought.delta", `{"text":"note."}`)
	feed("thought.delta", `{"text":"`+strings.Repeat("x", 7000)+`"}`)
	feed("thought.delta", `{"text":"dropped"}`)
	var got []string
	for _, e := range events {
		if e.vals["type"] == "progress" {
			c := e.vals["content"].(string)
			if len(c) > 20 {
				c = "x*" + string(rune('0'+len(c)/1000))
			}
			got = append(got, e.vals["id"].(string)+":"+c)
		}
	}
	want := "ax-summary-1:First |ax-summary-1:note.|ax-summary-2:Second note.|ax-summary-3:Third note.|ax-summary-4:Fourth |ax-summary-4:note.|ax-summary-4:x*5"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %s", strings.Join(got, "|"))
	}
}
