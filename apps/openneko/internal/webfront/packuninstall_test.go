package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

type fakePackGraphjin struct {
	mu        sync.Mutex
	mutations []string
	failApply bool
	dropLater bool
	dropped   bool
}

func (g *fakePackGraphjin) take() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.mutations
	g.mutations = nil
	return out
}

func (g *fakePackGraphjin) server(t *testing.T, liveSource map[string]any, query string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(raw, &body)
		reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"gj_config": v}}) }
		switch {
		case strings.Contains(body.Query, "PackGraphjinTarget"):
			reply(map[string]any{"serv": map[string]any{"production": false, "auth": map[string]any{"type": "jwt"}}})
		case strings.Contains(body.Query, "PackLiveConfig_"):
			g.mu.Lock()
			sources := []any{liveSource}
			if g.dropped {
				sources = []any{}
			}
			g.mu.Unlock()
			reply(map[string]any{"catalog_revision": "r1", "sources": sources, "tables": []any{}, "relationships": []any{}})
		case strings.Contains(body.Query, "PackSavedQuery_"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"gj_catalog": map[string]any{"graphql_query": query}}})
		case strings.HasPrefix(body.Query, "mutation"):
			g.mu.Lock()
			g.mutations = append(g.mutations, string(raw))
			fail := g.failApply
			g.mu.Unlock()
			if strings.Contains(body.Query, `mode: "preview"`) {
				reply(map[string]any{"valid": true, "preview_id": "p1"})
			} else if fail {
				reply(map[string]any{"applied": false, "errors_json": "apply refused"})
			} else {
				g.mu.Lock()
				g.dropped = g.dropLater
				g.mu.Unlock()
				reply(map[string]any{"applied": true, "catalog_revision": "r2"})
			}
		default:
			_, _ = io.WriteString(w, `{"errors":[{"message":"unexpected"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type uninstallFixture struct {
	f         *Front
	wk        *packWorker
	admin     map[string]string
	gj        *fakePackGraphjin
	install   string
	skillDir  string
	actionRow func() map[string]any
}

func newUninstallFixture(t *testing.T) *uninstallFixture {
	f, wk, admin := packCatalogFront(t)
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("uninstall-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := f.API.DB
	ctx := context.Background()
	bundle := slackBundle(t)
	liveSource := map[string]any{"name": "slack", "kind": "api", "read_only": false, "access": map[string]any{"roles": []any{}}}
	query := "query Channels { channels { id } }\n"
	gj := &fakePackGraphjin{}
	srv := gj.server(t, liveSource, query)
	var sourceID string
	if err := db.QueryRow(ctx, `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode)
		values ('org', 'graphjin', 'GJ', $1, true, true, 'jwt') returning id::text`, srv.URL).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	workspace, err := orgWorkspace("org")
	if err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(workspace.Skills, "slack-messages")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Slack\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillHash, _ := packs.HashMarkdownTree(skillDir)
	seed(t, db, `insert into pack_action_definition (org_id, kind, description, definition, definition_hash, enabled, readiness)
		values ('org', 'slack.message', 'Send', '{}', 'dh', true, 'ready')`)
	seed(t, db, `insert into action_policy (org_id, name, mode, priority, applies_to_kinds, allowed_targets, limits, enabled)
		values ('org', 'Slack governed messages', 'approval_required', 10, '{slack.message}', '{}', '{}', true)`)
	row := func(query string) map[string]any {
		var raw string
		if err := db.QueryRow(ctx, query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(raw), &out)
		return out
	}
	actionRow := func() map[string]any {
		return row(`select to_jsonb(t)::text from pack_action_definition t where kind = 'slack.message'`)
	}
	actionHash, _ := packs.NativeStateHash(packs.KindAction, actionRow())
	policyHash, _ := packs.NativeStateHash(packs.KindPolicy, row(`select to_jsonb(t)::text from action_policy t where name = 'Slack governed messages'`))
	sourceHash, _ := packs.LiveSourceStateHash(liveSource, "")
	install := "00000000-0000-0000-0000-0000000000cc"
	cfg, _ := json.Marshal(map[string]any{"_runtime": map[string]any{"source": map[string]any{"id": sourceID, "graphqlUrl": srv.URL, "authMode": "jwt"}}})
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, config)
		values ($1, 'org', 'slack', '0.1.0', 'installed', 'h', $2::jsonb)`, install, string(cfg))
	for _, a := range []struct{ kind, key, target, applied, metadata string }{
		{"action", "action.slack_message", "slack.message", actionHash, `{"stateHashVersion":1,"locator":{"kind":"slack.message"}}`},
		{"policy", "policy.slack_governed_messages", "slack.policy.governed_messages", policyHash, `{"stateHashVersion":1,"locator":{"name":"Slack governed messages"}}`},
		{"skill", "skill.slack-messages", "slack-messages", *skillHash, `{"stateHashVersion":1,"materializedTarget":"` + skillDir + `"}`},
		{"source", "source.slack", "slack", sourceHash, `{"stateHashVersion":1}`},
		{"saved_query", "saved_query.channels", "saved_query.channels", packs.SHA256([]byte(strings.TrimSpace(query))), `{"stateHashVersion":1,"materializedTarget":"graphjin:saved_query:slack_channels"}`},
	} {
		seed(t, db, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash, metadata)
			values ($1, 'org', $2, $3, $4, $5, $6, $7::jsonb)`, install, a.kind, a.key, a.target, desiredHash(t, bundle, a.key), a.applied, a.metadata)
	}
	if err := secrets.Write(secrets.Store{"pack.slack": {"SLACK_BOT_TOKEN": "xoxb-1"}, "other": {"KEY": "v"}}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	return &uninstallFixture{f: f, wk: wk, admin: admin, gj: gj, install: install, skillDir: skillDir, actionRow: actionRow}
}

func (x *uninstallFixture) scalar(t *testing.T, query string, args ...any) string {
	t.Helper()
	var out string
	if err := x.f.API.DB.QueryRow(context.Background(), query, args...).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPackUninstallRoute(t *testing.T) {
	x := newUninstallFixture(t)
	w := call(x.f, "POST", "/api/v1/admin/packs/slack/uninstall", `{"idempotencyKey":"k1"}`, x.admin)
	if w.Code != 200 {
		t.Fatalf("uninstall: %d %s", w.Code, w.Body.String())
	}
	if got := decode(t, w); got["status"] != "removed" || got["packId"] != "slack" {
		t.Errorf("status %v", got)
	}
	if calls := x.wk.take(); len(calls) != 0 {
		t.Errorf("the worker was called: %v", calls)
	}
	mutations := x.gj.take()
	if len(mutations) != 2 {
		t.Fatalf("mutations: %v", mutations)
	}
	for _, want := range []string{`source_patches: [{ name: \"slack\", read_only: true, access: { read: \"blocked\", write: \"blocked\", delete: \"blocked\" } }]`,
		`remove_saved_queries: [\"slack_channels\"]`, `expected_catalog_revision: \"r1\"`} {
		if !strings.Contains(mutations[1], want) {
			t.Errorf("apply lacks %s: %s", want, mutations[1])
		}
	}
	if !strings.Contains(mutations[1], `preview_id: \"p1\"`) {
		t.Errorf("apply without the preview id: %s", mutations[1])
	}

	action := x.actionRow()
	if action["enabled"] != false || action["readiness"] != "blocked" || action["readiness_reason"] != "pack_uninstalled" {
		t.Errorf("action row %v", action)
	}
	if got := x.scalar(t, `select enabled::text from action_policy where name = 'Slack governed messages'`); got != "false" {
		t.Errorf("policy enabled %s", got)
	}
	actionHash, _ := packs.NativeStateHash(packs.KindAction, action)
	if got := x.scalar(t, `select last_applied_hash from pack_artifact where artifact_key = 'action.slack_message'`); got != actionHash {
		t.Errorf("action receipt %s, want %s", got, actionHash)
	}
	if got := x.scalar(t, `select string_agg(distinct ownership || '/' || readiness, ',') from pack_artifact where pack_install_id = $1`, x.install); got != "retired/not_applicable" {
		t.Errorf("receipts %s", got)
	}
	if got := x.scalar(t, `select (metadata->>'retainedForGraphjinCompatibility') || (metadata->>'retiredMissing') from pack_artifact where artifact_kind = 'source'`); got != "truefalse" {
		t.Errorf("source receipt %s", got)
	}
	retiredQuery, _ := packs.CanonicalHash(map[string]any{"retired": true, "identity": "saved_query:saved_query.channels"})
	if got := x.scalar(t, `select last_applied_hash || ' ' || (metadata->>'retiredMissing') from pack_artifact where artifact_kind = 'saved_query'`); got != retiredQuery+" true" {
		t.Errorf("saved query receipt %s", got)
	}
	if _, err := os.Stat(x.skillDir); !os.IsNotExist(err) {
		t.Errorf("the pack skill folder is still there: %v", err)
	}
	if matches, _ := filepath.Glob(x.skillDir + ".*"); len(matches) != 0 {
		t.Errorf("staged backups remain: %v", matches)
	}
	store, err := secrets.Read(x.f.API.SecretsDir)
	if err != nil || store["pack.slack"] != nil || store["other"]["KEY"] != "v" {
		t.Errorf("secrets %v %v", store, err)
	}
	if got := x.scalar(t, `select status || '/' || phase || '/' || compensation_status || '/' || operation_type from pack_operation where idempotency_key = 'k1'`); got != "succeeded/complete/not_required/uninstall" {
		t.Errorf("operation %s", got)
	}
	if got := x.scalar(t, `select (removed_at is not null)::text || (operation_id is not null)::text from pack_install where id = $1`, x.install); got != "truetrue" {
		t.Errorf("install %s", got)
	}

	w = call(x.f, "POST", "/api/v1/admin/packs/slack/uninstall", `{"idempotencyKey":"k1"}`, x.admin)
	if w.Code != 200 || decode(t, w)["status"] != "removed" || len(x.gj.take()) != 0 {
		t.Errorf("replay: %d %s", w.Code, w.Body.String())
	}
	w = call(x.f, "POST", "/api/v1/admin/packs/slack/uninstall", `{"idempotencyKey":"k1"}`, map[string]string{"Cookie": sessionCookie("member")["Cookie"]})
	if w.Code != 403 {
		t.Errorf("member: %d", w.Code)
	}
	w = call(x.f, "POST", "/api/v1/admin/packs/magento/uninstall", `{"idempotencyKey":"k1"}`, x.admin)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "idempotency key was already used for pack slack") {
		t.Errorf("key of another pack: %d %s", w.Code, w.Body.String())
	}
	w = call(x.f, "POST", "/api/v1/admin/packs/magento/uninstall", `{}`, x.admin)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"phase":"uninstall"`) || !strings.Contains(w.Body.String(), "pack magento is not installed") {
		t.Errorf("not installed: %d %s", w.Code, w.Body.String())
	}
}

func TestPackUninstallRefusesDrift(t *testing.T) {
	x := newUninstallFixture(t)
	seed(t, x.f.API.DB, `update pack_action_definition set description = 'Edited' where kind = 'slack.message'`)
	w := call(x.f, "POST", "/api/v1/admin/packs/slack/uninstall", `{}`, x.admin)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "pack uninstall has 1 drift conflict(s); modified artifacts were preserved") {
		t.Fatalf("drift: %d %s", w.Code, w.Body.String())
	}
	if got := x.scalar(t, `select string_agg(artifact_key || '=' || ownership, ',' order by artifact_key) from pack_artifact where ownership <> 'managed'`); got != "action.slack_message=modified" {
		t.Errorf("ownership %s", got)
	}
	if got := x.scalar(t, `select status || '/' || coalesce(last_error, '') from pack_install where id = $1`, x.install); !strings.HasPrefix(got, "installed/pack uninstall has 1 drift") {
		t.Errorf("install %s", got)
	}
	if got := x.scalar(t, `select count(*)::text from pack_operation`); got != "0" || len(x.gj.take()) != 0 {
		t.Errorf("an operation started: %s", got)
	}
}

func TestPackUninstallRollsBack(t *testing.T) {
	x := newUninstallFixture(t)
	x.gj.failApply = true
	w := call(x.f, "POST", "/api/v1/admin/packs/slack/uninstall", `{"idempotencyKey":"k2"}`, x.admin)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "pack GraphJin config apply failed: apply refused") {
		t.Fatalf("apply failure: %d %s", w.Code, w.Body.String())
	}
	if got := x.scalar(t, `select status || '/' || failure_phase || '/' || compensation_status from pack_operation where idempotency_key = 'k2'`); got != "failed/uninstall/succeeded" {
		t.Errorf("operation %s", got)
	}
	if got := x.scalar(t, `select status || '/' || last_error from pack_install where id = $1`, x.install); got != "installed/pack GraphJin config apply failed: apply refused" {
		t.Errorf("install %s", got)
	}
	if _, err := os.Stat(filepath.Join(x.skillDir, "SKILL.md")); err != nil {
		t.Errorf("skill folder: %v", err)
	}
	if store, _ := secrets.Read(x.f.API.SecretsDir); store["pack.slack"]["SLACK_BOT_TOKEN"] != "xoxb-1" {
		t.Errorf("secrets %v", store)
	}
	if x.actionRow()["enabled"] != true {
		t.Errorf("the action was switched off")
	}
	x.gj.failApply = false
	w = call(x.f, "POST", "/api/v1/admin/packs/slack/uninstall", `{"idempotencyKey":"k2"}`, x.admin)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "pack GraphJin config apply failed") {
		t.Errorf("replay of a failed key: %d %s", w.Code, w.Body.String())
	}

	x.gj.dropLater = true
	w = call(x.f, "POST", "/api/v1/admin/packs/slack/uninstall", `{"idempotencyKey":"k3"}`, x.admin)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "pack GraphJin artifact source:source.slack disappeared during uninstall") {
		t.Fatalf("late failure: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(x.skillDir, "SKILL.md")); err != nil {
		t.Errorf("skill folder after the late failure: %v", err)
	}
	if matches, _ := filepath.Glob(x.skillDir + ".*"); len(matches) != 0 {
		t.Errorf("staged backups remain: %v", matches)
	}
	if store, _ := secrets.Read(x.f.API.SecretsDir); store["pack.slack"]["SLACK_BOT_TOKEN"] != "xoxb-1" {
		t.Errorf("secrets after the late failure %v", store)
	}
	if got := x.scalar(t, `select status || '/' || compensation_status from pack_operation where idempotency_key = 'k3'`); got != "failed/succeeded" {
		t.Errorf("late operation %s", got)
	}
	if got := x.scalar(t, `select count(*)::text from pack_artifact where ownership = 'retired'`); got != "0" || x.actionRow()["enabled"] != true {
		t.Errorf("a receipt was retired: %s", got)
	}
}
