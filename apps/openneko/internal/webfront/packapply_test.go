package webfront

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

func TestDeclarativeGraphjinUpdateMatchesNode(t *testing.T) {
	abs, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "packs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_PACKS_DIR", abs)
	raw, err := os.ReadFile("testdata/nodepackupdate.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Pack, Mode string
		Inputs     map[string]any
		Secrets    map[string]string
		Update     map[string]any
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 15 {
		t.Fatalf("cases: %d", len(cases))
	}
	for _, c := range cases {
		bundle, err := packBundle("org", c.Pack, "")
		if err != nil {
			t.Fatal(err)
		}
		var api *packAPIArtifacts
		var retired []string
		access := "authenticated"
		switch c.Mode {
		case "files":
			retired = []string{"old_source"}
		case "public":
			access = "public"
			fallthrough
		default:
			if api, err = graphjinAPIArtifacts(bundle, c.Inputs); err != nil {
				t.Fatal(err)
			}
		}
		got, err := declarativeGraphjinUpdate(bundle, c.Inputs, c.Secrets, retired, map[string]string{}, api, access)
		if err != nil {
			t.Fatalf("%s %s: %v", c.Pack, c.Mode, err)
		}
		gotText, _ := packs.CanonicalJSON(plainJSON(jsValueOf(got)))
		wantText, _ := packs.CanonicalJSON(c.Update)
		if gotText != wantText {
			t.Errorf("%s %s:\n got %s\nwant %s", c.Pack, c.Mode, gotText, wantText)
		}
	}
}

type applyFixture struct {
	*uninstallFixture
	workspace orgRoots
}

func newApplyFixture(t *testing.T) *applyFixture {
	f, wk, admin := packCatalogFront(t)
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("apply-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile(filepath.Join(os.Getenv("OPENNEKO_PACKS_DIR"), "slack", "graphjin", "specs", "slack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	liveSource := map[string]any{"name": "slack", "kind": "api", "read_only": false,
		"specs": map[string]any{"slack": map[string]any{"base_url": "https://slack.com", "document": string(spec)}}}
	gj := &fakePackGraphjin{}
	srv := gj.server(t, liveSource, "query Channels { channels { id } }\n")
	seed(t, f.API.DB, `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode)
		values ('org', 'graphjin', 'GJ', $1, true, true, 'jwt')`, srv.URL)
	if err := secrets.Write(secrets.Store{"other": {"KEY": "v"}}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	workspace, err := orgWorkspace("org")
	if err != nil {
		t.Fatal(err)
	}
	return &applyFixture{uninstallFixture: &uninstallFixture{f: f, wk: wk, admin: admin, gj: gj}, workspace: workspace}
}

const slackInstall = `{"secrets":{"slack.bot_token":"xoxb-1"},"idempotencyKey":"%s"}`

func (x *applyFixture) post(t *testing.T, action, body string) map[string]any {
	t.Helper()
	w := call(x.f, "POST", "/api/v1/admin/packs/slack/"+action, body, x.admin)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
	}
	return decode(t, w)
}

func TestPackApplyRoute(t *testing.T) {
	x := newApplyFixture(t)
	got := x.post(t, "install", strings.Replace(slackInstall, "%s", "k1", 1))
	if got["status"] != "installed" || got["packId"] != "slack" {
		t.Fatalf("status %v", got)
	}
	if calls := x.wk.take(); len(calls) != 0 {
		t.Errorf("the worker was called: %v", calls)
	}
	mutations := x.gj.take()
	if len(mutations) != 2 || !strings.Contains(mutations[0], `mode: \"preview\"`) {
		t.Fatalf("mutations: %v", mutations)
	}
	for _, want := range []string{`name: \"pack_api_executor\"`, `update_saved_queries: [{ name: \"slack_channels\"`,
		`expose_as: \"slack_post_message\"`, `token: \"xoxb-1\"`, `preview_id: \"p1\"`} {
		if !strings.Contains(mutations[1], want) {
			t.Errorf("apply lacks %s", want)
		}
	}
	bundle := slackBundle(t)
	if n := x.scalar(t, `select count(*)::text from pack_artifact where org_id = 'org' and ownership = 'managed'
		and (metadata->>'stateHashVersion')::int = 1 and readiness = 'ready'`); n != "8" || len(bundle.Artifacts) != 8 {
		t.Errorf("artifacts %s of %d", n, len(bundle.Artifacts))
	}
	if got := x.scalar(t, `select metadata->>'materializedTarget' from pack_artifact where artifact_key = 'saved_query.channels'`); got != "graphjin:saved_query:slack_channels" {
		t.Errorf("saved query target %s", got)
	}
	if got := x.scalar(t, `select string_agg(name || '=' || enabled::text, ',' order by name) from action_policy`); got != "Slack approved channels=false,Slack governed messages=false" {
		t.Errorf("policies %s", got)
	}
	if got := x.scalar(t, `select readiness || ',' || enabled::text from pack_action_definition where kind = 'slack.message'`); got != "ready,true" {
		t.Errorf("action %s", got)
	}
	if _, err := os.Stat(filepath.Join(x.workspace.Skills, "slack-messages", "SKILL.md")); err != nil {
		t.Errorf("skill: %v", err)
	}
	store, _ := secrets.Read(x.f.API.SecretsDir)
	if store["pack.slack"]["SLACK_BOT_TOKEN"] != "xoxb-1" || store["other"]["KEY"] != "v" {
		t.Errorf("secrets %v", store)
	}
	if got := x.scalar(t, `select status || ',' || phase || ',' || compensation_status from pack_operation where idempotency_key = 'k1'`); got != "succeeded,complete,not_required" {
		t.Errorf("operation %s", got)
	}
	w := call(x.f, "GET", "/api/v1/admin/packs/slack/plan", "", x.admin)
	for _, e := range decode(t, w)["entries"].([]any) {
		if entry := e.(map[string]any); entry["action"] != "noop" {
			t.Errorf("after install: %v", entry)
		}
	}

	if got := x.post(t, "install", strings.Replace(slackInstall, "%s", "k1", 1)); got["status"] != "installed" || len(x.gj.take()) != 0 {
		t.Errorf("replay %v", got)
	}
	if got := x.post(t, "configure", `{"idempotencyKey":"k2"}`); got["status"] != "installed" || len(x.gj.take()) != 0 {
		t.Errorf("no-op configure %v", got)
	}
	if n := x.scalar(t, `select count(*)::text from pack_operation`); n != "1" {
		t.Errorf("operations %s", n)
	}

	w = call(x.f, "POST", "/api/v1/admin/packs/slack/configure", `{"reviewHash":"stale","secrets":{"slack.bot_token":"xoxb-2"}}`, x.admin)
	if d, _ := decode(t, w)["diagnostic"].(map[string]any); w.Code != 400 || d["phase"] != "configure" || !strings.Contains(w.Body.String(), "review is missing or stale") {
		t.Errorf("stale review: %d %s", w.Code, w.Body.String())
	}
	if w := call(x.f, "POST", "/api/v1/admin/packs/slack/install", `{}`, sessionCookie("member")); w.Code != 403 {
		t.Errorf("member: %d", w.Code)
	}

	seed(t, x.f.API.DB, `insert into workflow_definition (org_id, owner_user_id, name, goal) values ('org', '', 'Old digest', 'g')`)
	var row string
	if err := x.f.API.DB.QueryRow(context.Background(), `select to_jsonb(t)::text from workflow_definition t where name = 'Old digest'`).Scan(&row); err != nil {
		t.Fatal(err)
	}
	oldHash, _ := nativeHash(packs.KindWorkflow, row)
	seed(t, x.f.API.DB, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash, metadata)
		select id, 'org', 'workflow', 'workflow.old', 'slack.workflow.old', 'd', $1, '{"stateHashVersion":1,"locator":{"name":"Old digest"}}'
		from pack_install where pack_id = 'slack'`, oldHash)
	if got := x.post(t, "upgrade", `{"idempotencyKey":"k3"}`); got["status"] != "installed" {
		t.Errorf("upgrade %v", got)
	}
	if got := x.scalar(t, `select enabled::text || ',' || cron_enabled::text from workflow_definition where name = 'Old digest'`); got != "false,false" {
		t.Errorf("retired workflow %s", got)
	}
	if got := x.scalar(t, `select ownership || ',' || (metadata->>'retiredMissing') from pack_artifact where artifact_key = 'workflow.old'`); got != "retired,false" {
		t.Errorf("retired receipt %s", got)
	}
	if got := x.scalar(t, `select operation_type || ',' || status from pack_operation where idempotency_key = 'k3'`); got != "upgrade,succeeded" {
		t.Errorf("upgrade operation %s", got)
	}
}

func TestPackApplyRollsBack(t *testing.T) {
	x := newApplyFixture(t)
	x.gj.failApply = true
	w := call(x.f, "POST", "/api/v1/admin/packs/slack/install", strings.Replace(slackInstall, "%s", "k1", 1), x.admin)
	if d, _ := decode(t, w)["diagnostic"].(map[string]any); w.Code != 400 || d["phase"] != "install" {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	if got := x.scalar(t, `select status || ',' || failure_phase || ',' || compensation_status from pack_operation where idempotency_key = 'k1'`); got != "failed,apply,succeeded" {
		t.Errorf("operation %s", got)
	}
	if got := x.scalar(t, `select status from pack_install where pack_id = 'slack'`); got != "failed" {
		t.Errorf("install row %s", got)
	}
	store, _ := secrets.Read(x.f.API.SecretsDir)
	if _, has := store["pack.slack"]; has || store["other"]["KEY"] != "v" {
		t.Errorf("secrets not restored: %v", store)
	}
	if _, err := os.Stat(filepath.Join(x.workspace.Skills, "slack-messages")); err == nil {
		t.Error("skill written")
	}
	if w := call(x.f, "POST", "/api/v1/admin/packs/slack/install", strings.Replace(slackInstall, "%s", "k1", 1), x.admin); w.Code != 400 {
		t.Errorf("replay of a failed key: %d", w.Code)
	}
}

func TestPackDefinitionInstall(t *testing.T) {
	f, wk, admin := packCatalogFront(t)
	w := call(f, "POST", "/api/v1/admin/packs/magento/install", `{"deferConfiguration":true,"idempotencyKey":"d1"}`, admin)
	if got := decode(t, w); w.Code != 200 || got["status"] != "installed" {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("the worker was called: %v", calls)
	}
	var config, phase string
	if err := f.API.DB.QueryRow(context.Background(), `select i.config::text, o.phase from pack_install i join pack_operation o on o.pack_install_id = i.id
		where i.pack_id = 'magento'`).Scan(&config, &phase); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config, `"_definitionOnly": true`) || phase != "definition_installed" {
		t.Errorf("config %s phase %s", config, phase)
	}
	w = call(f, "POST", "/api/v1/admin/packs/slack/install", `{"deferConfiguration":true,"version":"9.9.9"}`, admin)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "reload the pack list") {
		t.Errorf("version check: %d %s", w.Code, w.Body.String())
	}
	call(f, "POST", "/api/v1/admin/packs/magento/configure", `{"inputs":{}}`, admin)
	if calls := wk.take(); len(calls) != 1 || calls[0].uri != "/admin/packs/magento/configure" {
		t.Errorf("Magento configure goes to the worker: %v", calls)
	}
}

func TestInstallGraphjinFiles(t *testing.T) {
	abs, _ := filepath.Abs(filepath.Join("..", "..", "..", "..", "packs"))
	t.Setenv("OPENNEKO_PACKS_DIR", abs)
	bundle := slackBundle(t)
	root := t.TempDir()
	configFile := filepath.Join(root, "graphjin.yml")
	query := filepath.Join(root, "queries", "slack_channels.gql")
	spec := filepath.Join(root, "specs", "slack.yaml")
	if err := os.MkdirAll(filepath.Dir(query), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(query, []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installGraphjinFiles(bundle, configFile, map[string]any{}, map[string]bool{}); err == nil || !strings.Contains(err.Error(), "is not owned by this pack") {
		t.Fatalf("unowned target: %v", err)
	}
	if _, err := os.Stat(spec); err == nil {
		t.Error("the spec stayed after the failure")
	}
	files, err := installGraphjinFiles(bundle, configFile, map[string]any{}, map[string]bool{query: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(query); !strings.Contains(string(got), "query") || files.targets["saved_query:saved_query.channels"] != query {
		t.Errorf("query %q targets %v", got, files.targets)
	}
	if err := files.restore(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(query); string(got) != "someone else's" {
		t.Errorf("restore: %q", got)
	}
	if _, err := os.Stat(spec); err == nil {
		t.Error("the new spec stayed after restore")
	}
}
