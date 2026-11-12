package webfront

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

func modelSettingsFront(t *testing.T) (*Front, *pgxpool.Pool) {
	t.Helper()
	f, db, _ := hostFront(t)
	if err := config.EnsureSecretKey(f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", "")
	t.Setenv("OPENNEKO_STACK_MODE", "")
	old := provisionRetryDelay
	provisionRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { provisionRetryDelay = old })
	return f, db
}

func TestModelSettingsAdminOnly(t *testing.T) {
	f, db := modelSettingsFront(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/settings/agent", ""},
		{"PUT", "/api/v1/settings/agent", `{"globalCap":9}`},
		{"GET", "/api/v1/settings/data-source", ""},
		{"PUT", "/api/v1/settings/data-source", `{"graphqlUrl":"http://x.example/graphql"}`},
		{"POST", "/api/v1/settings/data-source/test", `{"graphqlUrl":"http://127.0.0.1:1/graphql"}`},
		{"GET", "/api/v1/settings/provider", ""},
		{"PUT", "/api/v1/settings/provider", `{"scope":"primary","provider":"openai","secrets":{"apiKey":"` + plantedProviderKey + `"}}`},
	} {
		if w := call(f, c.method, c.path, c.body, sessionCookie("member")); w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"admin only"}` {
			t.Errorf("%s %s as a member: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	var rows int
	if err := db.QueryRow(t.Context(), `select (select count(*) from llm_provider_config) + (select count(*) from data_source where graphql_url like 'http://x.example%')`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("a member wrote %d rows (%v)", rows, err)
	}
}

func TestAgentSettings(t *testing.T) {
	f, db := modelSettingsFront(t)
	admin := sessionCookie("admin")
	put := func(body string) (int, string) {
		w := call(f, "PUT", "/api/v1/settings/agent", body, admin)
		return w.Code, strings.TrimSpace(w.Body.String())
	}

	w := call(f, "GET", "/api/v1/settings/agent", "", admin)
	want := `{"agent":{"source":"default","backend":"ax","globalCap":3,"terminal":true},"defaults":{"globalCap":3,"terminal":true},"options":[{"value":"ax","label":"Ax","description":"OpenNeko's Go agent. Calls tools from code, so one model step can run many tools."}],"terminalAvailable":false}`
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
		t.Errorf("defaults: %d %s", w.Code, w.Body.String())
	}
	t.Setenv("OPENNEKO_SANDBOX_URL", "unix:///run/openneko/sandbox.sock")
	if got := decode(t, call(f, "GET", "/api/v1/settings/agent", "", admin)); got["terminalAvailable"] != true {
		t.Errorf("a sandbox URL makes the terminal available: %v", got)
	}

	for body, wantErr := range map[string]string{
		`{"terminal":"off"}`:                           "terminal must be true or false",
		`{"terminal":null}`:                            "terminal must be true or false",
		`{"backend":"removed-runtime","globalCap":30}`: "Unsupported agent backend: removed-runtime",
		`{"backend":null}`:                             "Unsupported agent backend: null",
		`{"zeta":1,"globalCap":2,"alpha":2}`:           "Unsupported agent setting: zeta, alpha",
		`[1]`:                                          "Agent settings payload must be an object",
		`{"globalCap":`:                                "The request body is not valid JSON.",
	} {
		if code, got := put(body); code != 400 || got != `{"error":"`+wantErr+`"}` {
			t.Errorf("%s: %d %s", body, code, got)
		}
	}
	var n int
	if err := db.QueryRow(t.Context(), `select count(*) from llm_provider_config where scope = 'agent'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused save wrote a row: %d %v", n, err)
	}

	if code, got := put(`{"terminal":false}`); code != 200 || got != `{"source":"org","backend":"ax","globalCap":3,"terminal":false}` {
		t.Errorf("terminal off: %d %s", code, got)
	}
	if code, got := put(`{"globalCap":"4"}`); code != 200 || got != `{"source":"org","backend":"ax","globalCap":4,"terminal":false}` {
		t.Errorf("a later save keeps the terminal off: %d %s", code, got)
	}
	if code, got := put(`{"globalCap":null,"backend":"ax"}`); code != 200 || got != `{"source":"org","backend":"ax","globalCap":4,"terminal":false}` {
		t.Errorf("a null cap keeps the stored cap: %d %s", code, got)
	}
	if code, got := put(`{"globalCap":"invalid","terminal":true}`); code != 200 || got != `{"source":"org","backend":"ax","globalCap":3,"terminal":true}` {
		t.Errorf("an invalid cap falls back: %d %s", code, got)
	}
	if code, got := put(`{"globalCap":12.7}`); code != 200 || got != `{"source":"org","backend":"ax","globalCap":12,"terminal":true}` {
		t.Errorf("a fractional cap: %d %s", code, got)
	}
	var provider, cfg string
	if err := db.QueryRow(t.Context(), `select provider, config::text from llm_provider_config where org_id = 'org' and scope = 'agent'`).Scan(&provider, &cfg); err != nil {
		t.Fatal(err)
	}
	if provider != "ax" || cfg != `{"backend": "ax", "terminal": true, "globalCap": 12}` {
		t.Errorf("stored row: %s %s", provider, cfg)
	}
	if got := decode(t, call(f, "GET", "/api/v1/settings/agent", "", admin)); got["agent"].(map[string]any)["source"] != "org" {
		t.Errorf("after a save: %v", got)
	}
}

func TestDataSourceSettings(t *testing.T) {
	f, db := modelSettingsFront(t)
	admin := sessionCookie("admin")
	if _, err := db.Exec(t.Context(), `delete from data_source where org_id = 'org'`); err != nil {
		t.Fatal(err)
	}
	w := call(f, "GET", "/api/v1/settings/data-source", "", admin)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"source":"unset","kind":"graphjin","graphqlUrl":"","mcpUrl":"","label":"primary"}` {
		t.Errorf("unset: %d %s", w.Code, w.Body.String())
	}

	for body, wantErr := range map[string]string{
		`{}`:                         "GraphQL URL is required.",
		`{"graphqlUrl":"   "}`:       "GraphQL URL is required.",
		`{"graphqlUrl":"not a url"}`: "GraphQL URL is not a valid URL.",
		`{"graphqlUrl":"ftp://x.example/graphql"}`:  "GraphQL URL must use http or https.",
		`{"graphqlUrl":"","mcpUrl":"javascript:x"}`: "GraphQL URL is required. MCP URL must use http or https.",
	} {
		w := call(f, "PUT", "/api/v1/settings/data-source", body, admin)
		if w.Code != 400 || decode(t, w)["error"] != wantErr {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	var n int
	if err := db.QueryRow(t.Context(), `select count(*) from data_source where org_id = 'org'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused save wrote a row: %d %v", n, err)
	}

	w = call(f, "PUT", "/api/v1/settings/data-source", `{"graphqlUrl":" http://gj.example:8080/api/v1/graphql ","mcpUrl":"  ","label":" Sales "}`, admin)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"source":"org","kind":"graphjin","graphqlUrl":"http://gj.example:8080/api/v1/graphql","mcpUrl":"","label":"Sales"}` {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "graphjin", "client.json"))
	if err != nil || string(raw) != "{\n  \"expires_at\": \"0001-01-01T00:00:00Z\",\n  \"server\": \"http://gj.example:8080\",\n  \"token\": \"\"\n}" {
		t.Errorf("client.json: %q %v", raw, err)
	}

	w = call(f, "PUT", "/api/v1/settings/data-source", `{"graphqlUrl":"https://gj2.example/graphql","mcpUrl":"https://gj2.example/mcp"}`, admin)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"source":"org","kind":"graphjin","graphqlUrl":"https://gj2.example/graphql","mcpUrl":"https://gj2.example/mcp","label":"primary"}` {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var mcp, label string
	if err := db.QueryRow(t.Context(), `select count(*) over (), mcp_url, label from data_source where org_id = 'org'`).Scan(&n, &mcp, &label); err != nil || n != 1 || mcp != "https://gj2.example/mcp" || label != "primary" {
		t.Errorf("one row, updated in place: %d %s %s %v", n, mcp, label, err)
	}
	if got := decode(t, call(f, "GET", "/api/v1/settings/data-source", "", admin)); got["source"] != "org" || got["graphqlUrl"] != "https://gj2.example/graphql" {
		t.Errorf("read back: %v", got)
	}
}

func TestDataSourceTest(t *testing.T) {
	f, _ := modelSettingsFront(t)
	admin := sessionCookie("admin")
	var mu sync.Mutex
	var reply, mcpStatus = `{"data":{"__typename":"Query"}}`, 200
	var gotQuery, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/mcp" {
			gotAccept = r.Header.Get("Accept")
			w.WriteHeader(mcpStatus)
			return
		}
		gotQuery, _ = body["query"].(string)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	probe := func(body string) (int, string) {
		w := call(f, "POST", "/api/v1/settings/data-source/test", body, admin)
		return w.Code, strings.TrimSpace(w.Body.String())
	}

	if code, got := probe(`{"graphqlUrl":"` + srv.URL + `/graphql"}`); code != 200 || got != `{"graphqlOk":true,"mcpOk":null}` || gotQuery != "{ __typename }" {
		t.Errorf("graphql only: %d %s %q", code, got, gotQuery)
	}
	if code, got := probe(`{"graphqlUrl":"` + srv.URL + `/graphql","mcpUrl":"` + srv.URL + `/mcp"}`); code != 200 || got != `{"graphqlOk":true,"mcpOk":true}` || gotAccept != "application/json, text/event-stream" {
		t.Errorf("with MCP: %d %s %q", code, got, gotAccept)
	}
	mcpStatus = 500
	if code, got := probe(`{"graphqlUrl":"` + srv.URL + `/graphql","mcpUrl":"` + srv.URL + `/mcp"}`); code != 200 || got != `{"graphqlOk":true,"mcpOk":false}` {
		t.Errorf("MCP fails: %d %s", code, got)
	}
	for r, wantErr := range map[string]string{
		`not json`:             "GraphQL URL did not return JSON (status 200).",
		`5`:                    "GraphQL URL did not return JSON (status 200).",
		`{"hello":"world"}`:    "GraphQL URL did not return a valid GraphQL response.",
		`{"errors":[{"x":1}]}`: "",
	} {
		reply = r
		code, got := probe(`{"graphqlUrl":"` + srv.URL + `/graphql"}`)
		if wantErr == "" && code != 200 || wantErr != "" && (code != 400 || got != `{"error":"`+wantErr+`"}`) {
			t.Errorf("reply %s: %d %s", r, code, got)
		}
	}
	if code, got := probe(`{"graphqlUrl":"http://127.0.0.1:1/graphql"}`); code != 400 || !strings.HasPrefix(got, `{"error":"GraphQL URL unreachable: `) {
		t.Errorf("unreachable: %d %s", code, got)
	}
	if code, got := probe(`{"graphqlUrl":"file:///etc/passwd"}`); code != 400 || got != `{"error":"GraphQL URL must use http or https."}` {
		t.Errorf("file URL: %d %s", code, got)
	}
}

func TestProviderSettings(t *testing.T) {
	f, db := modelSettingsFront(t)
	admin := sessionCookie("admin")
	put := func(body string) (int, map[string]any, string) {
		w := call(f, "PUT", "/api/v1/settings/provider", body, admin)
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		return w.Code, got, w.Body.String()
	}
	stored := func(scope string) (provider, model string, secrets map[string]string) {
		var raw []byte
		var m *string
		if err := db.QueryRow(t.Context(), `select provider, model, secrets from llm_provider_config where org_id = 'org' and scope = $1`, scope).Scan(&provider, &m, &raw); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &secrets)
		if m != nil {
			model = *m
		}
		return
	}

	got := decode(t, call(f, "GET", "/api/v1/settings/provider", "", admin))
	primary, research := got["primary"].(map[string]any), got["research"].(map[string]any)
	if primary["source"] != "default" || primary["provider"] != "google-gemini" || research["source"] != "default" || research["model"] != "sonar-deep-research" {
		t.Errorf("defaults: %v", got)
	}
	options := got["options"].(map[string]any)
	defaults := got["defaults"].(map[string]any)
	fields := got["fields"].(map[string]any)
	if len(options["primary"].([]any)) != 16 || len(options["research"].([]any)) != 2 ||
		defaults["primary"].(map[string]any)["openai"] != "gpt-4.1-mini" || defaults["research"].(map[string]any)["disabled"] != "" ||
		len(fields["research"].(map[string]any)["disabled"].([]any)) != 0 || len(fields["primary"].(map[string]any)["azure-openai"].([]any)) != 3 {
		t.Errorf("catalogue: %v", got)
	}

	code, saved, body := put(`{"scope":"primary","provider":"anthropic","model":"claude-opus-4-7","enabled":true,"config":{},"secrets":{"apiKey":"` + plantedProviderKey + `"}}`)
	if code != 200 || saved["source"] != "org" || saved["provider"] != "anthropic" || strings.Contains(body, plantedProviderKey) {
		t.Fatalf("save primary: %d %s", code, body)
	}
	if _, _, secrets := stored("primary"); !config.IsEncrypted(secrets["apiKey"]) {
		t.Errorf("the key is stored in the clear: %v", secrets)
	}
	w := call(f, "GET", "/api/v1/settings/provider", "", admin)
	status := decode(t, w)["primary"].(map[string]any)["secretStatus"].(map[string]any)
	if strings.Contains(w.Body.String(), plantedProviderKey) || !strings.Contains(status["apiKey"].(string), "•") {
		t.Errorf("read back: %s", w.Body.String())
	}

	if code, _, body := put(`{"scope":"primary","provider":"anthropic","model":"claude-sonnet","secrets":{"apiKey":"  "},"config":{"max_output_tokens":4096}}`); code != 200 {
		t.Fatalf("a blank key keeps the stored key: %d %s", code, body)
	}
	if _, model, secrets := stored("primary"); model != "claude-sonnet" || secrets["apiKey"] == "" {
		t.Errorf("after a blank key: %s %v", model, secrets)
	}
	if code, saved, _ := put(`{"scope":"primary","provider":"anthropic","model":"","secrets":{"apiKey":null}}`); code != 400 || saved["error"] != "API key is required." {
		t.Errorf("a null key deletes it: %d %v", code, saved)
	}

	if code, saved, _ := put(`{"scope":"primary","provider":"openai","secrets":{"apiKey":"sk-new-openai"}}`); code != 200 || saved["model"] != "gpt-4.1-mini" || saved["config"].(map[string]any)["max_output_tokens"] != nil {
		t.Errorf("a provider change takes the default model and drops the old config: %d %v", code, saved)
	}
	if provider, _, secrets := stored("primary"); provider != "openai" || len(secrets) != 1 {
		t.Errorf("a provider change wipes the old secrets: %s %v", provider, secrets)
	}

	for body, wantErr := range map[string]string{
		`{"scope":"primary","provider":"made-up"}`:                       "Unsupported primary provider: made-up",
		`{"scope":"research","provider":"openai"}`:                       "Unsupported research provider: openai",
		`{"scope":"other","provider":"openai"}`:                          "Unsupported provider scope: other",
		`{"scope":"primary","provider":"openai-compatible","model":"m"}`: "Base URL is required.",
		`{"scope":"research","provider":"perplexity","enabled":true}`:    "API key is required.",
		`[]`: "Provider settings payload must be an object",
	} {
		if code, saved, _ := put(body); code != 400 || saved["error"] != wantErr {
			t.Errorf("%s: %d %v", body, code, saved)
		}
	}

	if code, saved, _ := put(`{"scope":"research","provider":"disabled","model":"","enabled":false,"config":{},"secrets":{}}`); code != 200 || saved["enabled"] != false || saved["scope"] != "research" {
		t.Errorf("research off: %d %v", code, saved)
	}
	if provider, model, _ := stored("research"); provider != "disabled" || model != "" {
		t.Errorf("research row: %s %q", provider, model)
	}
}

type catalogServer struct {
	*httptest.Server
	mu     sync.Mutex
	tokens []string
}

func newCatalogServer(t *testing.T, secretKey []byte, answer bool) *catalogServer {
	c := &catalogServer{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		c.mu.Lock()
		c.tokens = append(c.tokens, token)
		c.mu.Unlock()
		parts := strings.Split(token, ".")
		valid := false
		if len(parts) == 3 {
			mac := hmac.New(sha256.New, []byte(base64.StdEncoding.EncodeToString(secretKey)))
			mac.Write([]byte(parts[0] + "." + parts[1]))
			valid = base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) == parts[2]
		}
		if !answer || !valid {
			_, _ = w.Write([]byte(`{"data":{"gj_catalog":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"gj_catalog":{"id":"help:discovery"}}}`))
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *catalogServer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.tokens)
}

const sourcesConfig = `app_name: openneko
auth:
  type: jwt
  jwt:
    secret: "REPLACE_WITH_PER_ORG_SECRET_B64"
sources:
  - name: sales
    kind: postgres
    read_only: true
`

func TestProvisionHostConfig(t *testing.T) {
	f, db := modelSettingsFront(t)
	admin := sessionCookie("admin")
	key, err := config.SigningSecret(f.API.SecretsDir, "graphjin:org")
	if err != nil {
		t.Fatal(err)
	}
	authMode := func() string {
		var m string
		if err := db.QueryRow(t.Context(), `select auth_mode from data_source where org_id = 'org' and name = 'default'`).Scan(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	setSource := func(url, mode, label string) {
		seed(t, db, `delete from data_source where org_id = 'org'`)
		seed(t, db, `insert into data_source (org_id, kind, graphql_url, auth_mode, label, is_default) values ('org', 'graphjin', $1, $2, $3, true)`, url, mode, label)
	}
	saveAgent := func() {
		t.Helper()
		if w := call(f, "PUT", "/api/v1/settings/agent", `{"globalCap":3}`, admin); w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
	}

	cfgPath := filepath.Join(t.TempDir(), "agentic.yml")
	if err := os.WriteFile(cfgPath, []byte(sourcesConfig), 0o640); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", cfgPath)
	gj := newCatalogServer(t, key, true)
	setSource(gj.URL+"/api/v1/graphql", "none", "Sales")
	saveAgent()
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "REPLACE_WITH") || !strings.Contains(string(raw), base64.StdEncoding.EncodeToString(key)) || !strings.Contains(string(raw), "allow_mutations: true") {
		t.Errorf("the config was not reconciled:\n%s", raw)
	}
	if info, _ := os.Stat(cfgPath); info.Mode().Perm() != 0o640 {
		t.Errorf("the config mode changed: %v", info.Mode())
	}
	if authMode() != "jwt" || gj.count() != 1 {
		t.Errorf("a source that answers gj_catalog with the service token turns jwt on: %s after %d probes", authMode(), gj.count())
	}
	claims, _ := base64.RawURLEncoding.DecodeString(strings.Split(gj.tokens[0], ".")[1])
	if !strings.Contains(string(claims), `"role":"service"`) || !strings.Contains(string(claims), `"org_id":"org"`) {
		t.Errorf("token claims: %s", claims)
	}

	saveAgent()
	if gj.count() != 1 {
		t.Errorf("a jwt source with an unchanged config is not probed again: %d", gj.count())
	}

	setSource(gj.URL+"/api/v1/graphql", "development", "Sales")
	if err := os.WriteFile(cfgPath, []byte(sourcesConfig), 0o640); err != nil {
		t.Fatal(err)
	}
	saveAgent()
	if authMode() != "development" || gj.count() != 1 {
		t.Errorf("a development source keeps its mode: %s, %d probes", authMode(), gj.count())
	}

	silent := newCatalogServer(t, key, false)
	setSource(silent.URL+"/graphql", "none", "Sales")
	if err := os.WriteFile(cfgPath, []byte(sourcesConfig), 0o640); err != nil {
		t.Fatal(err)
	}
	saveAgent()
	if authMode() != "none" || silent.count() != 6 {
		t.Errorf("a rewritten config gives six probes: %s, %d", authMode(), silent.count())
	}
	saveAgent()
	if silent.count() != 7 {
		t.Errorf("an external source with an unchanged config gets one probe: %d", silent.count())
	}

	t.Setenv("OPENNEKO_STACK_MODE", "demo")
	setSource("http://127.0.0.1:1/api/v1/graphql", "none", "AdventureWorks")
	saveAgent()
	if authMode() != "jwt" {
		t.Errorf("the packaged demo source turns jwt on with no probe: %s", authMode())
	}

	t.Setenv("OPENNEKO_STACK_MODE", "")
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", filepath.Join(t.TempDir(), "missing.yml"))
	setSource(gj.URL+"/api/v1/graphql", "none", "Sales")
	if w := call(f, "PUT", "/api/v1/settings/provider", `{"scope":"primary","provider":"openai","secrets":{"apiKey":"k"}}`, admin); w.Code != 200 {
		t.Fatalf("a missing config file does not fail the save: %d %s", w.Code, w.Body.String())
	}
	if authMode() != "jwt" {
		t.Errorf("a primary provider save provisions too: %s", authMode())
	}
	before := gj.count()
	setSource(gj.URL+"/api/v1/graphql", "none", "Sales")
	if w := call(f, "PUT", "/api/v1/settings/provider", `{"scope":"research","provider":"disabled","enabled":false}`, admin); w.Code != 200 || gj.count() != before || authMode() != "none" {
		t.Errorf("a research save does not provision: %d, %d probes, %s", w.Code, gj.count()-before, authMode())
	}
}
