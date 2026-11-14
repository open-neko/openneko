package webfront

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

type reviewFixture struct {
	InputDeclarations  []packs.Input           `json:"inputDeclarations"`
	SecretDeclarations []packs.Secret          `json:"secretDeclarations"`
	OAuth              []packs.OAuthConnection `json:"oauth"`
	Store              json.RawMessage         `json:"store"`
	Inputs             []struct {
		Supplied json.RawMessage `json:"supplied"`
		Result   json.RawMessage `json:"result"`
		Error    *string         `json:"error"`
	} `json:"inputs"`
	Secrets []struct {
		Request json.RawMessage   `json:"request"`
		Result  map[string]string `json:"result"`
		Error   *string           `json:"error"`
	} `json:"secrets"`
	Tables []struct {
		Sources  []packs.Artifact    `json:"sources"`
		Queries  []packs.Artifact    `json:"queries"`
		Bindings map[string]string   `json:"bindings"`
		Result   []map[string]string `json:"result"`
		Error    *string             `json:"error"`
	} `json:"tables"`
	Checks []struct {
		Name      string            `json:"name"`
		Inputs    map[string]any    `json:"inputs"`
		Secrets   map[string]string `json:"secrets"`
		Health    json.RawMessage   `json:"health"`
		Artifacts []packs.Artifact  `json:"artifacts"`
		Error     *string           `json:"error"`
	} `json:"checks"`
	Hashes []struct {
		Operation string            `json:"operation"`
		Request   json.RawMessage   `json:"request"`
		Inputs    json.RawMessage   `json:"inputs"`
		Secrets   map[string]string `json:"secrets"`
		Runtime   json.RawMessage   `json:"runtime"`
		Plan      json.RawMessage   `json:"plan"`
		Hash      string            `json:"hash"`
	} `json:"hashes"`
}

func errText(err error) *string {
	if err == nil {
		return nil
	}
	s := err.Error()
	return &s
}

func sameErr(got error, want *string) bool {
	g := errText(got)
	return (g == nil) == (want == nil) && (g == nil || *g == *want)
}

func parsedObj(t *testing.T, raw []byte) *jsObj {
	t.Helper()
	v, err := jsParse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return jsObjOrEmpty(v)
}

func serviceHealthBundle(t *testing.T) *packs.Bundle {
	t.Helper()
	root := filepath.Join(t.TempDir(), "service-health")
	if err := os.CopyFS(root, os.DirFS(filepath.Join("..", "..", "..", "worker", "test", "fixtures", "service-health"))); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"actions", "policies"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b, err := packs.LoadBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPackReviewNodeFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "nodereview.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx reviewFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}

	inputBundle := &packs.Bundle{Manifest: &packs.Manifest{Inputs: fx.InputDeclarations}}
	for _, c := range fx.Inputs {
		got, err := resolvePackInputs(inputBundle, parsedObj(t, c.Supplied))
		if !sameErr(err, c.Error) || err == nil && jsStringify(got, false) != string(mustCompact(t, c.Result)) {
			t.Errorf("inputs %s: got %s %v, want %s %v", c.Supplied, jsStringify(got, false), err, c.Result, c.Error)
		}
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.json"), fx.Store, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret-key"), []byte("go-pack-review-fixture-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &API{SecretsDir: dir}
	secretBundle := &packs.Bundle{Manifest: &packs.Manifest{Metadata: packs.Metadata{ID: "svc"}, Secrets: fx.SecretDeclarations, OAuth: fx.OAuth}}
	for _, c := range fx.Secrets {
		got, err := a.resolvePackSecrets(secretBundle, parsedObj(t, c.Request))
		if !sameErr(err, c.Error) || err == nil && !maps.Equal(got, c.Result) {
			t.Errorf("secrets %s: got %v %v, want %v %v", c.Request, got, err, c.Result, c.Error)
		}
	}

	for i, c := range fx.Tables {
		b := &packs.Bundle{Manifest: &packs.Manifest{Metadata: packs.Metadata{ID: "tbl"}}, Artifacts: append(c.Sources, c.Queries...)}
		got, err := packQueryTables(b, c.Bindings)
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(c.Result)
		if !sameErr(err, c.Error) || err == nil && string(gotJSON) != string(wantJSON) {
			t.Errorf("tables %d: got %s %v, want %s %v", i, gotJSON, err, wantJSON, c.Error)
		}
	}

	base := serviceHealthBundle(t)
	for _, c := range fx.Checks {
		b := *base
		manifest := *base.Manifest
		if err := json.Unmarshal(c.Health, &manifest.Health); err != nil {
			t.Fatal(err)
		}
		readiness := jsObjOrEmpty(parsedObj(t, c.Health).vals["readiness"])
		manifest.Health.ReadinessKeys = readiness.ordered()
		b.Manifest, b.Artifacts = &manifest, c.Artifacts
		err := declarativePackChecks(&b, c.Inputs, c.Secrets, map[string]string{})
		if !sameErr(err, c.Error) {
			t.Errorf("check %q: got %v, want %v", c.Name, err, errOrNil(c.Error))
		}
	}

	plan := packs.PlanBundle(base, nil)
	for _, c := range fx.Hashes {
		if got := planJSON(t, plan); got != string(mustCompact(t, c.Plan)) {
			t.Fatalf("plan:\n got %s\nwant %s", got, c.Plan)
		}
		request := parsedObj(t, c.Request)
		actor, ok := request.vals["actorUserId"]
		if !ok {
			actor = nil
		}
		got, err := a.packReviewHash("org-fixture", base, &plan, c.Operation, actor, request, parsedObj(t, c.Inputs), c.Secrets, parsedObj(t, c.Runtime))
		if err != nil || got != c.Hash {
			t.Errorf("hash %s: got %s %v, want %s", c.Operation, got, err, c.Hash)
		}
	}
}

func errOrNil(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func mustCompact(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	v, err := jsParse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(jsStringify(v, false))
}

// reviewFront serves the service-health fixture pack as an embedded pack.
func reviewFront(t *testing.T) (*Front, *packWorker, map[string]string) {
	t.Helper()
	b := serviceHealthBundle(t)
	t.Setenv("OPENNEKO_PACKS_DIR", filepath.Dir(b.Root))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, wk := packFront(t)
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("review-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, wk, sessionCookie("admin")
}

func TestPackReviewRoute(t *testing.T) {
	f, wk, admin := reviewFront(t)
	db := f.API.DB
	keystore := "true"
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "PackGraphjinTarget") {
			_, _ = io.WriteString(w, `{"data":{"gj_config":{"serv":{"production":false,"auth":{"type":"jwt"},"secrets_keystore_configured":`+keystore+`}}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"errors":[{"message":"unexpected"}]}`)
	}))
	t.Cleanup(gj.Close)
	review := func(body string) (int, map[string]any) {
		t.Helper()
		w := call(f, "POST", "/api/v1/admin/packs/service-health/review", body, admin)
		return w.Code, decode(t, w)
	}

	body := `{"inputs":{"service.base_url":"https://h.test/"},"secrets":{"service.api_token":"t"},"actorUserId":"forged"}`
	if code, got := review(body); code != 400 || got["error"] != "selected pack data source is unavailable in this organization" {
		t.Fatalf("no data source: %d %v", code, got)
	}
	var sourceID string
	if err := db.QueryRow(context.Background(), `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode)
		values ('org', 'graphjin', 'GJ', $1, true, true, 'jwt') returning id::text`, gj.URL).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	w := call(f, "POST", "/api/v1/admin/packs/service-health/review", body, admin)
	code, got := w.Code, decode(t, w)
	if code != 200 {
		t.Fatalf("review: %d %v", code, got)
	}
	wantRuntime := `{"source":{"id":"` + sourceID + `","graphqlUrl":"` + gj.URL + `","authMode":"jwt"},"bindings":{},"bindingHashes":{},"readiness":["reporting"],"tables":[]}`
	if !strings.Contains(w.Body.String(), `"runtime":`+wantRuntime+`,`) {
		t.Errorf("runtime: want %s in %s", wantRuntime, w.Body.String())
	}
	inputs, _ := json.Marshal(got["inputs"])
	if string(inputs) != `{"service.base_url":"https://h.test","service.timezone":"UTC"}` || got["operation"] != "install" || got["source"] != "embedded" {
		t.Errorf("inputs %s operation %v source %v", inputs, got["operation"], got["source"])
	}
	if secrets, _ := json.Marshal(got["secrets"]); string(secrets) != `["service.api_token"]` {
		t.Errorf("secrets %s", secrets)
	}
	bundle := serviceHealthBundle(t)
	plan := packs.PlanBundle(bundle, nil)
	if !strings.Contains(w.Body.String(), `"plan":`+planJSON(t, plan)+`,"secrets":["service.api_token"],"reviewHash":`) {
		t.Errorf("plan: want %s in %s", planJSON(t, plan), w.Body.String())
	}
	request := parsedObj(t, []byte(body))
	want, err := f.API.packReviewHash("org", bundle, &plan, "install", "admin", request,
		jsObjOf("service.base_url", "https://h.test", "service.timezone", "UTC"), map[string]string{"service.api_token": "t"}, parsedObj(t, []byte(wantRuntime)))
	if err != nil || got["reviewHash"] != want {
		t.Errorf("review hash %v, want %s (%v)", got["reviewHash"], want, err)
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("worker calls: %v", calls)
	}

	keystore = "false"
	if code, got := review(body); code != 400 || got["error"] != graphjinNoKeystoreMessage {
		t.Errorf("no keystore: %d %v", code, got)
	}
	keystore = "true"

	for _, c := range []struct{ body, message string }{
		{`{"operation":"remove"}`, "invalid pack review operation"},
		{`{"operation":"configure"}`, "pack must be installed before reviewing this operation"},
		{`{"inputs":{"service.base_url":"https://h.test"}}`, "required pack secret service.api_token is missing"},
		{`{"inputs":{"service.other":1}}`, "unknown pack input service.other"},
		{`{"inputs":{"service.base_url":"https://h.test"},"secrets":{"service.api_token":"t"},"sourceBindings":{"x":"y"}}`, "unknown pack source binding"},
		{`{"inputs":{"service.base_url":"https://h.test"},"secrets":{"service.api_token":"t"},"dataSourceId":"00000000-0000-0000-0000-000000000000"}`, "selected pack data source is unavailable in this organization"},
	} {
		code, got := review(c.body)
		d, _ := got["diagnostic"].(map[string]any)
		if code != 400 || got["error"] != c.message || d["phase"] != "review" || d["nextStep"] != "Correct the reported configuration or artifact, then review the pack again." {
			t.Errorf("%s: %d %v", c.body, code, got)
		}
	}

	seed(t, db, `insert into workflow_definition (org_id, owner_user_id, name, goal) values ('org', '', 'Review service health', 'g')`)
	if code, got := review(body); code != 400 || got["error"] != "workflow target service-health.review already exists and is not owned by pack service-health" {
		t.Errorf("native target: %d %v", code, got)
	}

	if w := call(f, "POST", "/api/v1/admin/packs/service-health/review", body, sessionCookie("member")); w.Code != 403 {
		t.Errorf("member: %d", w.Code)
	}
}

func TestPackReviewSourceBindings(t *testing.T) {
	f, _, _ := reviewFront(t)
	a := f.API
	live := map[string]any{"name": "shop", "kind": "database", "type": "postgres", "host": "db", "port": 5432, "dbname": "shop", "read_only": true}
	running := map[string]any{"name": "shop", "kind": "database", "type": "postgres", "host": "db", "port": 5432, "dbname": "shop", "read_only": true}
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(raw), "PackGraphjinTarget"):
			_, _ = io.WriteString(w, `{"data":{"gj_config":{"serv":{"production":false,"auth":{"type":"jwt"}}}}}`)
		case strings.Contains(string(raw), "PackLiveConfig_"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"gj_config": map[string]any{"sources": []any{live}, "tables": []any{}, "relationships": []any{}}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"gj_config": map[string]any{"sources": []any{running}}}})
		}
	}))
	t.Cleanup(gj.Close)
	seed(t, a.DB, `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode) values ('org', 'graphjin', 'GJ', '`+gj.URL+`', true, true, 'jwt')`)
	bundle := &packs.Bundle{
		Manifest:  &packs.Manifest{Metadata: packs.Metadata{ID: "tbl"}, Artifacts: packs.Artifacts{GraphJin: &packs.GraphJinArtifacts{}}},
		Artifacts: []packs.Artifact{{Kind: packs.KindSource, Key: "source.db", TargetRef: "tbl.binding.db", Content: map[string]any{"name": "db", "kind": "database"}}},
	}
	bundle.Manifest.Compatibility.Databases = append(bundle.Manifest.Compatibility.Databases, struct {
		Engine   string `json:"engine"`
		Versions string `json:"versions"`
	}{Engine: "postgres"})
	runtime := func(request string, prior *jsObj) (*jsObj, error) {
		return a.packRuntime(context.Background(), "org", bundle, parsedObj(t, []byte(request)), prior)
	}
	got, err := runtime(`{"sourceBindings":{"source.db":"shop"}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	hash := graphjinConfigPatchHash(jsValueOf(live))
	if b := jsStringify(got.vals["bindings"], false); b != `{"source.db":"shop"}` || jsStringify(got.vals["bindingHashes"], false) != `{"source.db":"`+hash+`"}` {
		t.Errorf("runtime %s", jsStringify(got, false))
	}
	prior := parsedObj(t, []byte(`{"bindings":{"source.db":"shop","gone":"x"},"bindingHashes":{"source.db":"old"}}`))
	if _, err := runtime(`{}`, prior); err == nil || err.Error() != "binding source.db changed; explicitly reconfigure it" {
		t.Errorf("changed binding: %v", err)
	}
	prior = parsedObj(t, []byte(`{"bindings":{"source.db":"shop"},"bindingHashes":{"source.db":"`+hash+`"}}`))
	if _, err := runtime(`{}`, prior); err != nil {
		t.Errorf("same binding: %v", err)
	}
	running["host"] = "other"
	if _, err := runtime(`{"sourceBindings":{"source.db":"shop"}}`, nil); err == nil || err.Error() != "binding source.db differs from the selected live source" {
		t.Errorf("differs: %v", err)
	}
	running["host"], live["read_only"] = "db", false
	if _, err := runtime(`{"sourceBindings":{"source.db":"shop"}}`, nil); err == nil || err.Error() != "binding source.db requires an existing read-only database source" {
		t.Errorf("writable: %v", err)
	}
	live["read_only"], live["type"], running["type"] = true, "mysql", "mysql"
	if _, err := runtime(`{"sourceBindings":{"source.db":"shop"}}`, nil); err == nil || err.Error() != "binding source.db database engine is incompatible" {
		t.Errorf("engine: %v", err)
	}
}
