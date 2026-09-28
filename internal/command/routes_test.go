package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
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
	for _, event := range events(t, copyOfEvents) {
		if event.Type == "model.request.started" {
			modelEvents = append(modelEvents, event.Origin+":"+event.Name)
		}
	}
	if got := strings.Join(modelEvents, ","); got != "cheap:fixture,work:fixture,work:fixture" {
		t.Fatalf("model receipts=%s", got)
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
