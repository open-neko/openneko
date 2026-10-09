package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestExactSkillNameSelectionNeedsNoModel(t *testing.T) {
	var selected Event
	events := &recorder{spec: Spec{Version: 1, RunID: "skill", InputID: "input"}, emit: func(event Event) error {
		selected = event
		return nil
	}}
	name := selectSkill(context.Background(), nil, "unused", "Use daily-lead-union for this report",
		[]SkillMetadata{{Name: "daily-lead-union", Description: "Daily report"}, {Name: "records", Description: "Records"}}, events)
	if name != "daily-lead-union" || selected.Type != "skill.selected" || selected.Origin != "exact" {
		t.Fatalf("name=%q event=%+v", name, selected)
	}
	if mentionsSkill("notdaily-lead-unionized", "daily-lead-union") {
		t.Fatal("partial skill name matched")
	}
}

func TestSemanticSkillSelectionRejectsUnstagedName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", `Selected: unapproved`), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	var selected Event
	events := &recorder{spec: Spec{Version: 1, RunID: "skill", InputID: "input"}, emit: func(event Event) error {
		if event.Type == "skill.selected" {
			selected = event
		}
		return nil
	}}
	name := selectSkill(context.Background(), client, "fixture", "Find the right procedure",
		[]SkillMetadata{{Name: "daily", Description: "Daily report"}}, events)
	if name != "" || selected.Error != "invalid_selection" || events.modelCalls != 1 {
		t.Fatalf("name=%q event=%+v calls=%d", name, selected, events.modelCalls)
	}
}
