package webfront

import (
	"strings"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/inference"
)

func TestDecideEndpointAnswersOnTheDecisionsRoute(t *testing.T) {
	f, db, g := hostFront(t)
	member := sessionCookie("member")
	seedPrimary(t, db, "openai", "gpt-5", `{}`, `{"apiKey":"primary-key"}`)
	seed(t, db, `insert into llm_model (org_id, name, provider, model, config, secrets, enabled) values
		('org', 'decider', 'google-gemini-decisions', 'gemini-decision-1', '{}', '{"apiKey":"decision-key"}', true)`)
	seed(t, db, `insert into llm_task_route (org_id, task, model_id) select 'org', 'decide', id from llm_model where name = 'decider'`)
	g.reply = map[string]any{"terrain": "arid"}
	g.usage = inference.Usage{Requests: 1, Reported: 1, InputTokens: 90, OutputTokens: 1, TotalTokens: 91, Coverage: "complete"}

	w := call(f, "POST", "/api/v1/decide", `{"signature":"place:string -> terrain:class \"arid, temperate\"","instruction":"Pick the terrain.","values":{"place":"Fujairah"}}`, member)
	if w.Code != 200 {
		t.Fatalf("decide: %d %s", w.Code, w.Body.String())
	}
	got := decode(t, w)
	if got["outputs"].(map[string]any)["terrain"] != "arid" || got["model"] != "gemini-decision-1" || got["tokens"] != float64(91) {
		t.Errorf("answer %v", got)
	}
	if c := g.calls[0]; c.key != "decision-key" || c.request.Route.Provider != "google-gemini-decisions" || c.request.Values["place"] != "Fujairah" {
		t.Errorf("call %+v", c)
	}
	var task string
	if err := db.QueryRow(t.Context(), `select task from spend_ledger where org_id = 'org'`).Scan(&task); err != nil || task != "decide" {
		t.Errorf("ledger task %q %v", task, err)
	}

	for name, body := range map[string]string{
		"a text output": `{"signature":"q:string -> answer:string","values":{"q":"?"}}`,
		"no values":     `{"signature":"q:string -> ok:boolean"}`,
		"no signature":  `{"values":{}}`,
	} {
		if w := call(f, "POST", "/api/v1/decide", body, member); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if len(g.calls) != 1 {
		t.Errorf("a refused request reached the model: %d calls", len(g.calls))
	}
}

func TestWorldRecords(t *testing.T) {
	f, _, _ := signinFront(t, liveScalekit)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	const path = "/api/v1/worlds/records/mapping"

	if w := call(f, "GET", path, "", member); w.Code != 404 {
		t.Fatalf("a missing record: %d", w.Code)
	}
	if w := call(f, "PUT", path, `{"value":{"terrain":"arid"}}`, member); w.Code != 403 {
		t.Fatalf("a member wrote a record of the organization: %d", w.Code)
	}
	if w := call(f, "PUT", path, `{"value":{"terrain":"arid"}}`, admin); w.Code != 200 {
		t.Fatalf("admin save: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "PUT", path, `{"value":{"terrain":"snow"}}`, admin); w.Code != 200 {
		t.Fatalf("admin save again: %d %s", w.Code, w.Body.String())
	}
	if got := decode(t, call(f, "GET", path, "", member)); got["value"].(map[string]any)["terrain"] != "snow" || got["updatedAt"] == "" {
		t.Errorf("record %v", got)
	}

	const mine = "/api/v1/worlds/records/placements?scope=mine"
	if w := call(f, "PUT", mine, `{"value":[{"id":"crane-1","x":4}]}`, member); w.Code != 200 {
		t.Fatalf("own save: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", mine, "", member); w.Code != 200 || !strings.Contains(w.Body.String(), "crane-1") {
		t.Errorf("own record: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", mine, "", admin); w.Code != 404 {
		t.Errorf("another person read the record: %d", w.Code)
	}
	if w := call(f, "GET", "/api/v1/worlds/records/placements", "", member); w.Code != 404 {
		t.Errorf("a personal record showed as a record of the organization: %d", w.Code)
	}
	for _, bad := range []string{"/api/v1/worlds/records/Bad_Name", "/api/v1/worlds/records/x?scope=mine"} {
		if w := call(f, "PUT", bad, `{}`, admin); w.Code != 400 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}
