package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-neko/harness/internal/agent"
)

func TestHostRoutesSelectAxStagesAndPinResume(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	t.Setenv("HARNESS_CHEAP_KEY", "cheap-secret")
	t.Setenv("HARNESS_WORK_KEY", "work-secret")
	var requested []string
	serve := func(routeKey, expectedModel, expectedKey string, responses []string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+expectedKey {
				t.Errorf("wrong credential for %s", expectedModel)
			}
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Model != expectedModel {
				t.Errorf("route sent %q to %s", body.Model, expectedModel)
			}
			requested = append(requested, routeKey)
			if len(responses) == 0 {
				http.Error(w, "too many calls", 400)
				return
			}
			content := responses[0]
			responses = responses[1:]
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
		}))
	}
	cheap := serve("cheap", "fixture", "cheap-secret", []string{`{"javascriptCode":"final('Find reference', {})"}`})
	defer cheap.Close()
	work := serve("work", "fixture", "work-secret", []string{`{"javascriptCode":"final('Report reference', {reference:'REF-42'});"}`, `{"answer":"REF-42"}`})
	defer work.Close()
	config := func(responder string) string {
		b, _ := json.Marshal(routeConfig{Context: "cheap", Executor: "work", Responder: responder, Routes: []modelRoute{
			{Key: "cheap", Model: "fixture", URL: cheap.URL, APIKeyEnv: "HARNESS_CHEAP_KEY"},
			{Key: "work", Model: "fixture", URL: work.URL, APIKeyEnv: "HARNESS_WORK_KEY"},
		}})
		return string(b)
	}
	t.Setenv("HARNESS_MODEL_ROUTES", config("work"))
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(request), &out)
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v output=%s", code, err, out.String())
	}
	if got := strings.Join(requested, ","); got != "cheap,work,work" {
		t.Fatalf("stage routes=%s", got)
	}
	rawEvents := out.String()
	copyOfEvents := bytes.NewBufferString(rawEvents)
	var modelEvents []string
	var stageUsage []string
	for _, event := range events(t, copyOfEvents) {
		if event.Type == "model.request.started" {
			modelEvents = append(modelEvents, event.Origin+":"+event.Name)
		}
		if event.Type == "model.stage_usage" {
			if event.StageUsage == nil || event.StageUsage.Requests != 1 || event.StageUsage.Reported != 1 || event.StageUsage.TotalTokens != 15 || event.StageUsage.Coverage != "complete" {
				t.Fatalf("invalid stage projection: %+v", event)
			}
			stageUsage = append(stageUsage, event.Name)
		}
	}
	if got := strings.Join(modelEvents, ","); got != "cheap:fixture,work:fixture,work:fixture" {
		t.Fatalf("model receipts=%s", got)
	}
	if got := strings.Join(stageUsage, ","); got != "distiller,executor,responder" {
		t.Fatalf("stage usage=%s", got)
	}
	for _, secret := range []string{"cheap-secret", "work-secret"} {
		if strings.Contains(rawEvents, secret) {
			t.Fatal("credential leaked in events")
		}
	}
	var replay bytes.Buffer
	code, err = run(context.Background(), strings.NewReader(request), &replay)
	if code != 0 || err != nil || replay.String() != rawEvents || len(requested) != 3 {
		t.Fatalf("same-route replay failed: code=%d err=%v", code, err)
	}
	t.Setenv("HARNESS_MODEL_ROUTES", config("cheap"))
	var changed bytes.Buffer
	code, err = run(context.Background(), strings.NewReader(request), &changed)
	if code != 1 || err == nil || len(requested) != 3 {
		t.Fatalf("changed route replay admitted: code=%d err=%v", code, err)
	}
}

func TestSkillSelectionUsesApprovedCheapRoute(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	t.Setenv("HARNESS_CHEAP_KEY", "cheap-secret")
	t.Setenv("HARNESS_WORK_KEY", "work-secret")
	var requested []string
	var requests []string
	serve := func(route, key string, responses []string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+key {
				t.Errorf("wrong credential on %s", route)
			}
			body, _ := io.ReadAll(r.Body)
			requested = append(requested, route)
			requests = append(requests, string(body))
			if len(responses) == 0 {
				http.Error(w, "too many calls", 400)
				return
			}
			content := responses[0]
			responses = responses[1:]
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		}))
	}
	cheap := serve("cheap", "cheap-secret", []string{`{"selected":"daily-lead-union"}`, `{"javascriptCode":"final('Find',{})"}`})
	defer cheap.Close()
	work := serve("work", "work-secret", []string{`{"javascriptCode":"final('Answer',{})"}`, `{"answer":"Done"}`})
	defer work.Close()
	raw, _ := json.Marshal(routeConfig{Context: "cheap", Executor: "work", Responder: "work", Skill: "cheap", Routes: []modelRoute{
		{Key: "cheap", Model: "fixture", URL: cheap.URL, APIKeyEnv: "HARNESS_CHEAP_KEY"},
		{Key: "work", Model: "fixture", URL: work.URL, APIKeyEnv: "HARNESS_WORK_KEY"},
	}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	input := `{"version":1,"run_id":"skill-route","input_id":"input","skill_query":"Build the daily lead artifact","prompt":"Build the daily lead artifact"}`
	tools := agent.Tools{SkillCatalog: []agent.SkillMetadata{{Name: "daily-lead-union", Description: "Create the daily lead report"}, {Name: "records", Description: "Manage records"}},
		Capabilities: []agent.Capability{{Name: "skill_read", Version: "1", Origin: "skills", Effect: "read", Description: "Read staged skill instructions.",
			InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}},
	}
	var output bytes.Buffer
	code, err := executeWithTools(context.Background(), strings.NewReader(input), &output, tools)
	if code != 0 || err != nil {
		t.Fatalf("run code=%d err=%v output=%s", code, err, output.String())
	}
	if got := strings.Join(requested, ","); got != "cheap,cheap,work,work" {
		t.Fatalf("routes=%s", got)
	}
	if !strings.Contains(strings.Join(requests[2:], "\n"), "Candidate staged skill: daily-lead-union") {
		t.Fatal("executor and responder lost selected skill hint")
	}
	var selected, skillCall bool
	decoder := json.NewDecoder(&output)
	for {
		var event agent.Event
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if event.Type == "skill.selected" && event.Name == "daily-lead-union" && event.Origin == "semantic" {
			selected = true
		}
		if event.Type == "model.request.started" && event.Stage == "skill_selection" && event.Origin == "cheap" {
			skillCall = true
		}
	}
	if !selected || !skillCall {
		t.Fatalf("missing skill route telemetry: selected=%t call=%t", selected, skillCall)
	}
	var replay bytes.Buffer
	code, err = executeWithTools(context.Background(), strings.NewReader(input), &replay, tools)
	if code != 0 || err != nil || len(requested) != 4 {
		t.Fatalf("skill route replay dispatched again: code=%d err=%v calls=%d replay=%s", code, err, len(requested), replay.String())
	}
	var changed bytes.Buffer
	code, err = executeWithTools(context.Background(), strings.NewReader(strings.Replace(input, "daily lead artifact", "different artifact", 1)), &changed, tools)
	if code != 1 || err == nil || len(requested) != 4 {
		t.Fatalf("changed skill query accepted: code=%d err=%v calls=%d", code, err, len(requested))
	}
}

func TestRejectCallerRoutingAndUnapprovedStage(t *testing.T) {
	t.Setenv("HARNESS_MODEL_ROUTES", `{"context":"small","executor":"work","responder":"work","routes":[{"key":"small","model":"fixture","url":"http://127.0.0.1:1","api_key_env":"HARNESS_CHEAP_KEY"}]}`)
	t.Setenv("HARNESS_CHEAP_KEY", "synthetic")
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(request), &out)
	if code != 2 || err == nil || out.Len() != 0 {
		t.Fatalf("unapproved route admitted: code=%d err=%v", code, err)
	}
	code, err = run(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Hi","host_routing_digest":"other"}`), &out)
	if code != 2 || err == nil || out.Len() != 0 {
		t.Fatalf("caller route admitted: code=%d err=%v", code, err)
	}
}

func TestSkillCatalogLoadingFollowsTrustedRoute(t *testing.T) {
	if enabled, err := RouteHasSkill(""); err != nil || enabled {
		t.Fatalf("legacy route enabled skill selection: %t %v", enabled, err)
	}
	without := `{"context":"cheap","executor":"cheap","responder":"cheap","routes":[{"key":"cheap","model":"fixture","url":"https://example.invalid/v1","api_key_env":"HARNESS_CHEAP_KEY"}]}`
	if enabled, err := RouteHasSkill(without); err != nil || enabled {
		t.Fatalf("stage-only profile enabled skill selection: %t %v", enabled, err)
	}
	with := strings.Replace(without, `"responder":"cheap",`, `"responder":"cheap","skill":"cheap",`, 1)
	if enabled, err := RouteHasSkill(with); err != nil || !enabled {
		t.Fatalf("skill route unavailable: %t %v", enabled, err)
	}
}
