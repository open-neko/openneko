package webfront

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func TestAgentEgressHostsSetting(t *testing.T) {
	f, db := modelSettingsFront(t)
	abs, _ := filepath.Abs("../../../../packs")
	t.Setenv("OPENNEKO_PACKS_DIR", abs)
	seed(t, db, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config) values
		('org', 'resend', '1', 'installed', 'h', '{}'), ('org', 'slack', '1', 'removed', 'h', '{}')`)
	admin := sessionCookie("admin")
	put := func(body string) (int, string) {
		w := call(f, "PUT", "/api/v1/settings/agent", body, admin)
		return w.Code, strings.TrimSpace(w.Body.String())
	}

	got := decode(t, call(f, "GET", "/api/v1/settings/agent", "", admin))
	if hosts := got["agent"].(map[string]any)["egressHosts"]; hosts == nil || len(hosts.([]any)) != 0 {
		t.Errorf("the default list is not empty: %v", got)
	}
	if s, _ := got["egressSuggestions"].([]any); len(s) != 1 || s[0] != "api.resend.com" {
		t.Errorf("suggestions = %v, want the installed pack's host only", got["egressSuggestions"])
	}

	for body, want := range map[string]string{
		`{"egressHosts":"pypi.org"}`:           "egressHosts must be a list of host names",
		`{"egressHosts":[1]}`:                  "egressHosts must be a list of host names",
		`{"egressHosts":null}`:                 "egressHosts must be a list of host names",
		`{"egressHosts":["https://pypi.org"]}`: `\"https://pypi.org\" is not a host name, *.domain, or an address, with an optional :port`,
		`{"egressHosts":["*"]}`:                `\"*\" is not a host name, *.domain, or an address, with an optional :port`,
		`{"egressHosts":["pypi.org:99999"]}`:   `\"pypi.org:99999\" is not a host name, *.domain, or an address, with an optional :port`,
	} {
		if code, got := put(body); code != 400 || got != `{"error":"`+want+`"}` {
			t.Errorf("%s: %d %s", body, code, got)
		}
	}

	if w := call(f, "PUT", "/api/v1/settings/agent", `{"egressHosts":["evil.example"]}`, sessionCookie("member")); w.Code != 403 {
		t.Errorf("a member changed the list: %d %s", w.Code, w.Body.String())
	}
	if code, got := put(`{"egressHosts":["PyPI.org","pypi.org:443","*.githubusercontent.com","files.example.com:8443"]}`); code != 200 ||
		!strings.Contains(got, `"egressHosts":["pypi.org","*.githubusercontent.com","files.example.com:8443"]`) {
		t.Fatalf("save: %d %s", code, got)
	}
	if code, got := put(`{"globalCap":5}`); code != 200 || !strings.Contains(got, `"egressHosts":["pypi.org","*.githubusercontent.com","files.example.com:8443"]`) {
		t.Errorf("a later save dropped the list: %d %s", code, got)
	}
	if code, got := put(`{"egressHosts":[],"terminal":false}`); code != 200 || !strings.Contains(got, `"egressHosts":[]`) {
		t.Errorf("clear: %d %s", code, got)
	}

	rows, err := db.Query(context.Background(), `select event, payload::text from audit_chain where org_id = 'org' and entity_kind = 'agent_settings' order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for rows.Next() {
		var event, payload string
		if err := rows.Scan(&event, &payload); err != nil {
			t.Fatal(err)
		}
		events = append(events, event+" "+payload)
	}
	rows.Close()
	want := []string{
		`agent:egress_changed {"next": ["pypi.org", "*.githubusercontent.com", "files.example.com:8443"], "previous": [], "actorUserId": "admin"}`,
		`agent:terminal_changed {"next": false, "previous": true, "actorUserId": "admin"}`,
		`agent:egress_changed {"next": [], "previous": ["pypi.org", "*.githubusercontent.com", "files.example.com:8443"], "actorUserId": "admin"}`,
	}
	if !slices.Equal(events, want) {
		t.Errorf("audit chain:\n%s\nwant:\n%s", strings.Join(events, "\n"), strings.Join(want, "\n"))
	}

	if _, err := db.Exec(context.Background(), `update llm_provider_config set config = config || '{"egressHosts":["pypi.org","http://bad"]}' where org_id = 'org' and scope = 'agent'`); err != nil {
		t.Fatal(err)
	}
	if hosts, err := f.API.orgEgressHosts(context.Background(), "org"); err != nil || len(hosts) != 0 {
		t.Errorf("a stored list that fails the check gave %v, %v", hosts, err)
	}
}

func TestAxEnvironCarriesTheEgressList(t *testing.T) {
	env := strings.Join(axEnviron(plainCfg, axTurnOptions{OrgID: "org", RunID: "run-9", Shell: true, EgressHosts: []string{"pypi.org", "x.example:8443"}}, "/w", agentLimits{}), "\n") + "\n"
	for _, want := range []string{"OPENNEKO_HARNESS_SHELL=1", "OPENNEKO_HARNESS_RUN_ID=run-9", "OPENNEKO_HARNESS_EGRESS_HOSTS=pypi.org,x.example:8443"} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf("env lacks %s:\n%s", want, env)
		}
	}
	env = strings.Join(axEnviron(plainCfg, axTurnOptions{OrgID: "org", EgressHosts: []string{"pypi.org"}}, "/w", agentLimits{}), "\n") + "\n"
	if strings.Contains(env, "EGRESS") {
		t.Errorf("a turn with no terminal got an egress list:\n%s", env)
	}
}

func TestScriptStepsSendTheEgressList(t *testing.T) {
	var mu sync.Mutex
	var got []*sandbox.Egress
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body struct {
				Egress *sandbox.Egress `json:"egress"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			got = append(got, body.Egress)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 0, "output": "ok"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ws := stepTestWorkspace(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(ws.SkillsRoot, "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	steps := mustJS(t, []byte(`[{"id":"s1","description":"fetch","script":{"skill":"tool","command":["true"]}}]`))
	results, err := runScriptSteps(context.Background(), scriptStepsInput{Steps: jsArray(steps), Workspace: ws,
		Sandbox: &sandbox.Config{URL: server.URL}, Egress: sandbox.Egress{Org: "org", Label: "wf-run-1", Hosts: []string{"PyPI.org"}},
		Emit: func(context.Context, any) error { return nil }})
	if err != nil || len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("results = %+v, %v", results, err)
	}
	if len(got) != 1 || got[0] == nil || got[0].Org != "org" || got[0].Label != "wf-run-1" || !slices.Equal(got[0].Hosts, []string{"pypi.org"}) {
		t.Fatalf("the step sent egress %+v", got)
	}
}
