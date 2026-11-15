package webfront

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMapperReplacesTheAnswerWhenTheModelStartsAgain(t *testing.T) {
	var events []string
	m := &axEventMapper{textVersion: -1.0, renderCalls: map[string]bool{},
		emit: func(e *jsObj) { events = append(events, jsStringify(e, false)) }}
	delta := func(version int, text string) {
		data, _ := json.Marshal(map[string]any{"version": version, "index": 0, "text": text})
		m.handle(axEvent{Type: "answer.delta", Data: data})
	}
	delta(1, "Revenue fell ")
	delta(1, "by 4%.")
	delta(2, "Revenue rose")
	delta(2, " by 2%.")
	m.finish("Revenue rose by 2% in March.")

	want := []string{
		`{"type":"message","role":"assistant","content":"Revenue fell "}`,
		`{"type":"message","role":"assistant","content":"by 4%."}`,
		`{"type":"answer_reset"}`,
		`{"type":"message","role":"assistant","content":"Revenue rose"}`,
		`{"type":"message","role":"assistant","content":" by 2%."}`,
		`{"type":"answer_reset"}`,
		`{"type":"message","role":"assistant","content":"Revenue rose by 2% in March."}`,
	}
	if strings.Join(events, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(events, "\n"), strings.Join(want, "\n"))
	}
}

func TestJoinedAnswerTextStartsAgainAfterAReset(t *testing.T) {
	ctx := context.Background()
	msg := func(text string) any { return jsObjOf("type", "message", "role", "assistant", "content", text) }
	stream := []any{msg("Old "), msg("answer"), jsObjOf("type", "answer_reset"), msg("New "), msg("answer")}

	chat := &chatTurnState{emit: func(context.Context, any) error { return nil }, denials: map[string]bool{}}
	flow := &workflowTurnState{emit: func(context.Context, any) error { return nil }}
	trail := &actionTrail{}
	for _, e := range stream {
		if err := chat.wrapped(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := flow.wrapped(ctx, e); err != nil {
			t.Fatal(err)
		}
		trail.record(e)
	}
	if got := chat.assistantText.String(); got != "New answer" {
		t.Errorf("chat turn text %q", got)
	}
	if got := flow.text(); got != "New answer" {
		t.Errorf("workflow turn text %q", got)
	}
	if trail.text != "New answer" {
		t.Errorf("action trail text %q", trail.text)
	}
}

func TestMapperMarksAnOpenCardOnceAndHidesItsPayload(t *testing.T) {
	var events []string
	m := &axEventMapper{textVersion: -1.0, renderCalls: map[string]bool{},
		emit: func(e *jsObj) { events = append(events, jsStringify(e, false)) }}
	for _, part := range []string{"Sales rose.\n`", "``neko_ca", "rds\n{\"cards\":[{\"title\":", "\"East\"}]}\n``", "`\nEast led.", "\n```neko_a2ui\n[]"} {
		data, _ := json.Marshal(map[string]any{"version": 1, "index": 0, "text": part})
		m.handle(axEvent{Type: "answer.delta", Data: data})
	}
	want := []string{
		`{"type":"message","role":"assistant","content":"Sales rose.\n"}`,
		`{"type":"card_pending"}`,
		`{"type":"message","role":"assistant","content":"\nEast led."}`,
		`{"type":"message","role":"assistant","content":"\n"}`,
		`{"type":"card_pending"}`,
	}
	if strings.Join(events, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(events, "\n"), strings.Join(want, "\n"))
	}
}

func TestMapperKeepsAnAnswerThatDiffersOnlyInTrailingSpace(t *testing.T) {
	var events []string
	m := &axEventMapper{textVersion: -1.0, renderCalls: map[string]bool{},
		emit: func(e *jsObj) { events = append(events, jsStringify(e, false)) }}
	data, _ := json.Marshal(map[string]any{"version": 1, "index": 0, "text": "Revenue rose by 2%.\n\n"})
	m.handle(axEvent{Type: "answer.delta", Data: data})
	m.finish("Revenue rose by 2%.")
	if strings.Join(events, "\n") != `{"type":"message","role":"assistant","content":"Revenue rose by 2%.\n\n"}` {
		t.Fatalf("events:\n%s", strings.Join(events, "\n"))
	}
}

func TestMapperMarksOnlyFencesThatBecomeACard(t *testing.T) {
	for _, opener := range []string{"neko_workflow_save", "neko_workflow_output", "neko_action_request", "neko_rule_save"} {
		var events []string
		m := &axEventMapper{textVersion: -1.0, renderCalls: map[string]bool{},
			emit: func(e *jsObj) { events = append(events, jsStringify(e, false)) }}
		data, _ := json.Marshal(map[string]any{"version": 1, "index": 0, "text": "Saved.\n```" + opener + "\n{}\n```\nDone."})
		m.handle(axEvent{Type: "answer.delta", Data: data})
		if got := strings.Join(events, "\n"); strings.Contains(got, "card_pending") {
			t.Errorf("%s: %s", opener, got)
		}
	}
}
