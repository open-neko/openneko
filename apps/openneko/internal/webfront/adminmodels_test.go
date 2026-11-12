package webfront

import (
	"strings"
	"testing"
)

const plantedModelKey = "sk-planted-4f1e9c2a"

func TestAdminModelsAdminOnly(t *testing.T) {
	f, _, _ := signinFront(t, liveScalekit)
	id := "00000000-0000-4000-8000-000000000000"
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/models", ""},
		{"POST", "/api/v1/admin/models", `{"name":"x","provider":"openai","secrets":{"apiKey":"k"}}`},
		{"PUT", "/api/v1/admin/models/" + id, `{}`},
		{"DELETE", "/api/v1/admin/models/" + id, ""},
		{"PUT", "/api/v1/admin/model-routes", `{"task":"turn"}`},
	} {
		if w := call(f, c.method, c.path, c.body, sessionCookie("member")); w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"admin only"}` {
			t.Errorf("%s %s as a member: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
		if w := call(f, c.method, c.path, c.body, nil); w.Code == 200 || w.Code == 201 {
			t.Errorf("%s %s as anonymous: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

func TestAdminModelsKeySealedAndMasked(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	admin := sessionCookie("admin")

	got := decode(t, call(f, "GET", "/api/v1/admin/models", "", admin))
	dm := got["defaultModel"].(map[string]any)
	if dm["provider"] != "google-gemini" || dm["model"] != "gemini-3.6-flash" || dm["source"] != "default" {
		t.Errorf("default model with no provider row: %v", dm)
	}
	if routes := got["routes"].([]any); len(routes) != 8 || routes[0].(map[string]any)["task"] != "default" || routes[0].(map[string]any)["modelId"] != nil {
		t.Errorf("routes %v", routes)
	}
	catalog := got["catalog"].(map[string]any)
	if len(catalog["providers"].([]any)) != 16 || catalog["defaults"].(map[string]any)["openai-compatible"] != "" {
		t.Errorf("catalog %v", catalog)
	}

	w := call(f, "POST", "/api/v1/admin/models", `{"name":"openai-main","provider":"openai","model":"gpt-5","secrets":{"apiKey":"`+plantedModelKey+`"}}`, admin)
	if w.Code != 201 || strings.Contains(w.Body.String(), plantedModelKey) {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	created := decode(t, w)
	model := created["model"].(map[string]any)
	id := model["id"].(string)
	if model["secretStatus"].(map[string]any)["apiKey"] != "sk-••••••••••••9c2a" || model["price"] != nil || model["enabled"] != true {
		t.Errorf("model %v", model)
	}
	if len(created["settings"].(map[string]any)["models"].([]any)) != 1 {
		t.Errorf("settings %v", created["settings"])
	}
	var raw string
	if err := db.QueryRow(t.Context(), `select secrets::text from llm_model where id = $1`, id).Scan(&raw); err != nil || strings.Contains(raw, plantedModelKey) || !strings.Contains(raw, "enc:v1:") {
		t.Errorf("stored secrets %q %v", raw, err)
	}
	if body := call(f, "GET", "/api/v1/admin/models", "", admin).Body.String(); strings.Contains(body, plantedModelKey) {
		t.Errorf("settings hold the key: %s", body)
	}

	w = call(f, "PUT", "/api/v1/admin/models/"+id, `{"model":"gpt-5-mini","price":{"inputUsdPer1M":0.5,"outputUsdPer1M":2}}`, admin)
	kept := decode(t, w)["model"].(map[string]any)
	if w.Code != 200 || kept["model"] != "gpt-5-mini" || kept["secretStatus"].(map[string]any)["apiKey"] == nil {
		t.Errorf("change keeps the key: %d %v", w.Code, kept)
	}
	if p := kept["price"].(map[string]any); p["inputUsdPer1M"] != 0.5 || p["outputUsdPer1M"] != 2.0 {
		t.Errorf("price %v", p)
	}
	w = call(f, "PUT", "/api/v1/admin/models/"+id, `{"enabled":false}`, admin)
	if m := decode(t, w)["model"].(map[string]any); m["enabled"] != false || m["price"] == nil || m["model"] != "gpt-5-mini" {
		t.Errorf("disable keeps the rest: %v", m)
	}
	w = call(f, "PUT", "/api/v1/admin/models/"+id, `{"secrets":{"apiKey":null}}`, admin)
	if w.Code != 400 || decode(t, w)["error"] != "API key is required." {
		t.Errorf("removing a required key: %d %s", w.Code, w.Body.String())
	}

	var payload string
	if err := db.QueryRow(t.Context(), `select payload::text from audit_chain where org_id = 'org' and entity_kind = 'llm_model' and event = 'model:added'`).Scan(&payload); err != nil ||
		strings.Contains(payload, plantedModelKey) || !strings.Contains(payload, `"secretsChanged": ["apiKey"]`) {
		t.Errorf("audit payload %q %v", payload, err)
	}
}

func TestAdminModelsRefusals(t *testing.T) {
	f, _, _ := signinFront(t, liveScalekit)
	admin := sessionCookie("admin")
	cheap := `{"name":"cheap","provider":"openai-compatible","model":"qwen3","config":{"url":"https://models.example.test/v1"},"price":{"inputUsdPer1M":0.5,"outputUsdPer1M":2}}`
	w := call(f, "POST", "/api/v1/admin/models", cheap, admin)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	id := decode(t, w)["model"].(map[string]any)["id"].(string)
	nameRule := `The name must start with a letter and use only a to z, 0 to 9 and hyphens, at most 40 characters. "default" is taken.`
	for _, c := range []struct {
		body   string
		status int
		want   string
	}{
		{`[]`, 400, "A JSON body is required."},
		{`{"name":"default","provider":"openai","secrets":{"apiKey":"k"}}`, 400, nameRule},
		{`{"name":"Big Model","provider":"openai","secrets":{"apiKey":"k"}}`, 400, nameRule},
		{`{"name":"x","provider":"no-such"}`, 400, "Choose a model provider from the list."},
		{`{"name":"x","provider":"openai"}`, 400, "API key is required."},
		{`{"name":"x","provider":"openai","model":"` + strings.Repeat("m", 129) + `","secrets":{"apiKey":"k"}}`, 400, "The model name is required, at most 128 characters."},
		{`{"name":"x","provider":"openai","config":[],"secrets":{"apiKey":"k"}}`, 400, "config must be an object."},
		{`{"name":"x","provider":"openai","secrets":"k"}`, 400, "secrets must be an object."},
		{`{"name":"x","provider":"openai","secrets":{"apiKey":"k"},"price":{"inputUsdPer1M":-1,"outputUsdPer1M":2}}`, 400, "Each price must be a number from 0 to 1000 USD per million tokens."},
		{`{"name":"x","provider":"openai","secrets":{"apiKey":"k"},"price":{"inputUsdPer1M":"1","outputUsdPer1M":2}}`, 400, "Each price must be a number from 0 to 1000 USD per million tokens."},
		{`{"name":"x","provider":"openai-compatible","model":"m","config":{"url":"https://u:p@models.example.test/v1"}}`, 400, "OpenAI-compatible Base URL must not carry credentials, a query or a fragment."},
		{`{"name":"x","provider":"azure-openai","secrets":{"apiKey":"k"},"config":{"resourceName":"bad_name","deploymentName":"d"}}`, 400, "Azure resource name contains invalid characters."},
		{`{"name":"x","provider":"openai-compatible","config":{"url":"https://models.example.test/v1"}}`, 400, "The model name is required, at most 128 characters."},
		{cheap, 409, `A model named "cheap" exists.`},
	} {
		w := call(f, "POST", "/api/v1/admin/models", c.body, admin)
		if w.Code != c.status || decode(t, w)["error"] != c.want {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/admin/models/nope", "/api/v1/admin/models/00000000-0000-4000-8000-000000000000"} {
		if w := call(f, "PUT", path, `{}`, admin); w.Code != 404 || decode(t, w)["error"] != "No such model." {
			t.Errorf("PUT %s: %d %s", path, w.Code, w.Body.String())
		}
		if w := call(f, "DELETE", path, "", admin); w.Code != 404 || decode(t, w)["error"] != "No such model." {
			t.Errorf("DELETE %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w = call(f, "PUT", "/api/v1/admin/models/"+id, `{"provider":"anthropic"}`, admin)
	if w.Code != 400 || decode(t, w)["error"] != "API key is required." {
		t.Errorf("a provider change drops the old key: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminModelRoutes(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	admin := sessionCookie("admin")
	add := func(body string) string {
		w := call(f, "POST", "/api/v1/admin/models", body, admin)
		if w.Code != 201 {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		return decode(t, w)["model"].(map[string]any)["id"].(string)
	}
	cheap := add(`{"name":"cheap","provider":"openai-compatible","model":"qwen3","config":{"url":"https://models.example.test/v1"}}`)
	spare := add(`{"name":"spare","provider":"openai","model":"gpt-5","secrets":{"apiKey":"k"}}`)
	unknown := "00000000-0000-4000-8000-000000000000"
	for _, c := range []struct {
		body   string
		status int
		want   string
	}{
		{`null`, 400, "A JSON body is required."},
		{`{"task":"no-such-task","modelId":"` + cheap + `"}`, 400, "Choose a task from the list."},
		{`{"task":"default","modelId":"` + cheap + `"}`, 400, "The default route always uses the default model. Set only its fallback."},
		{`{"task":"turn","modelId":"` + cheap + `","fallbackModelId":"` + cheap + `"}`, 400, "The fallback must be another model."},
		{`{"task":"turn","modelId":"cheap"}`, 400, "modelId must be a model id."},
		{`{"task":"turn","fallbackModelId":5}`, 400, "fallbackModelId must be a model id."},
		{`{"task":"turn","modelId":"` + unknown + `"}`, 404, "No such model."},
	} {
		w := call(f, "PUT", "/api/v1/admin/model-routes", c.body, admin)
		if w.Code != c.status || decode(t, w)["error"] != c.want {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body.String())
		}
	}

	w := call(f, "PUT", "/api/v1/admin/model-routes", `{"task":"turn","modelId":"`+cheap+`","fallbackModelId":"`+spare+`"}`, admin)
	routes := decode(t, w)["routes"].([]any)
	if w.Code != 200 || routes[1].(map[string]any)["modelId"] != cheap || routes[1].(map[string]any)["fallbackModelId"] != spare {
		t.Fatalf("turn route: %d %v", w.Code, routes)
	}
	if w := call(f, "PUT", "/api/v1/admin/model-routes", `{"task":"default","modelId":"default","fallbackModelId":"`+spare+`"}`, admin); w.Code != 200 {
		t.Errorf("default fallback: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", "/api/v1/admin/models/"+cheap, "", admin); w.Code != 409 || decode(t, w)["error"] != "A task route uses this model. Change the route first." {
		t.Errorf("delete while routed: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "PUT", "/api/v1/admin/model-routes", `{"task":"turn","modelId":"","fallbackModelId":null}`, admin); w.Code != 200 {
		t.Errorf("clear route: %d %s", w.Code, w.Body.String())
	}
	var left int
	if err := db.QueryRow(t.Context(), `select count(*) from llm_task_route where org_id = 'org' and task = 'turn'`).Scan(&left); err != nil || left != 0 {
		t.Errorf("cleared route rows %d %v", left, err)
	}
	w = call(f, "DELETE", "/api/v1/admin/models/"+cheap, "", admin)
	if w.Code != 200 || len(decode(t, w)["models"].([]any)) != 1 {
		t.Errorf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", "/api/v1/admin/models/"+cheap, "", admin); w.Code != 404 {
		t.Errorf("second delete: %d %s", w.Code, w.Body.String())
	}
	var events string
	if err := db.QueryRow(t.Context(), `select string_agg(event, ',' order by seq) from audit_chain where org_id = 'org' and entity_kind = 'llm_model'`).Scan(&events); err != nil ||
		events != "model:added,model:added,model:route_changed,model:route_changed,model:route_changed,model:removed" {
		t.Errorf("audit events %q %v", events, err)
	}
}

func TestAdminModelsDefaultFromProviderRow(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, model, enabled, config, secrets)
		values ('org', 'primary', 'anthropic', 'claude-sonnet-5-5', true, '{}', '{"apiKey":"sk-default"}')`)
	dm := decode(t, call(f, "GET", "/api/v1/admin/models", "", sessionCookie("admin")))["defaultModel"].(map[string]any)
	if dm["provider"] != "anthropic" || dm["model"] != "claude-sonnet-5-5" || dm["source"] != "org" {
		t.Errorf("default model %v", dm)
	}
	seed(t, db, `update llm_provider_config set secrets = '{}' where org_id = 'org' and scope = 'primary'`)
	dm = decode(t, call(f, "GET", "/api/v1/admin/models", "", sessionCookie("admin")))["defaultModel"].(map[string]any)
	if dm["provider"] != "" || dm["model"] != "" || dm["source"] != "default" {
		t.Errorf("default model with an invalid row %v", dm)
	}
}
