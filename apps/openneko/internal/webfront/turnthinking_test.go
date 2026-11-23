package webfront

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
		textVersion: -1.0}
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

func TestExecutorNotesAreThinkingNotes(t *testing.T) {
	var events []*jsObj
	m := thinkingMapper("google-gemini", &events)
	feed := func(typ, data string) { m.handle(axEvent{Type: typ, Data: json.RawMessage(data)}) }
	feed("executor.thought.delta", `{"version":1,"index":1,"text":"Analyzing Sales Data"}`)
	feed("executor.thought.delta", `{"version":1,"index":2,"text":"Clarifying sk-live-0123456789a Ranges"}`)
	feed("actor.step", `{"text":"Read the orders"}`)
	feed("executor.thought.delta", `{"version":2,"index":3,"text":"Analyzing Sales Growth"}`)
	feed("thought.delta", `{"version":0,"text":"Writing the answer."}`)
	scrub := func(s string) string { return strings.ReplaceAll(s, "sk-live-0123456789a", "[REDACTED]") }
	var got []string
	for _, e := range events {
		if e.vals["type"] != "progress" {
			continue
		}
		e = scrubRunEvent(scrub, e).(*jsObj)
		if e.vals["source"] != "provider_summary" || e.vals["provider"] != "google-gemini" {
			t.Fatalf("progress %s", jsStringify(e, false))
		}
		got = append(got, e.vals["id"].(string)+":"+e.vals["content"].(string))
	}
	want := "ax-summary-1:Analyzing Sales Data|ax-summary-2:Clarifying [REDACTED] Ranges|ax-summary-3:Analyzing Sales Growth|ax-summary-4:Writing the answer."
	if strings.Join(got, "|") != want {
		t.Fatalf("got %s", strings.Join(got, "|"))
	}
}

func TestThinkingNoteIdsGoOnAfterAMalformedCallRetry(t *testing.T) {
	clearAxEnv(t, nil)
	note := func(seq int, call int, text string) string {
		return `{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":` + strconv.Itoa(seq) +
			`,"type":"executor.thought.delta","data":{"version":` + strconv.Itoa(call) + `,"index":1,"text":"` + text + `"}}}`
	}
	srv := newScriptedServe(t, [][]json.RawMessage{
		frames(note(1, 1, "Checking the sales"),
			`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":2,"type":"run.finished","result":{"status":"failed","code":"MALFORMED_FUNCTION_CALL"}}}`,
			`{"type":"exit","code":1}`),
		frames(note(1, 1, "Comparing the regions"),
			`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":2,"type":"answer.delta","data":{"version":1,"text":"Done."}}}`,
			`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":3,"type":"run.finished","result":{"status":"completed","answer":"Done."}}}`,
			`{"type":"exit","code":0}`),
	})
	notes := map[string]string{}
	res, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-1", Socket: srv.socket,
		Emit: func(_ context.Context, event any) error {
			e := event.(*jsObj)
			if e.vals["type"] == "progress" {
				id := e.vals["id"].(string)
				notes[id] += e.vals["content"].(string)
			}
			return nil
		}})
	if err != nil || res.Status != "completed" {
		t.Fatalf("got %+v %v", res, err)
	}
	if len(notes) != 2 || notes["ax-summary-1"] != "Checking the sales" || notes["ax-summary-2"] != "Comparing the regions" {
		t.Fatalf("notes %v", notes)
	}
}

func TestATurnShowsAFirstLineAtOnceAndThePlainStepLines(t *testing.T) {
	clearAxEnv(t, nil)
	srv := newScriptedServe(t, [][]json.RawMessage{
		frames(`{"type":"event","event":{"version":1,"run_id":"workrun-1","type":"step.quiet","data":{"text":"Putting the findings together"}}}`,
			`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":1,"type":"answer.delta","data":{"version":1,"text":"Done."}}}`,
			`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":2,"type":"run.finished","result":{"status":"completed","answer":"Done."}}}`,
			`{"type":"exit","code":0}`),
	})
	var got []string
	res, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-1", Socket: srv.socket,
		Emit: func(_ context.Context, event any) error {
			e := event.(*jsObj)
			if e.vals["type"] == "status" {
				got = append(got, "status:"+e.vals["message"].(string))
			} else {
				got = append(got, e.vals["type"].(string))
			}
			return nil
		}})
	if err != nil || res.Status != "completed" {
		t.Fatalf("got %+v %v", res, err)
	}
	if len(got) < 3 || got[0] != "status:Reading your question" || got[1] != "status:Putting the findings together" {
		t.Fatalf("events %v", got)
	}
}
