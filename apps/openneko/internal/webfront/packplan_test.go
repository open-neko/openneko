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
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

func planJSON(t *testing.T, p packs.Plan) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func planActions(t *testing.T, body string) map[string]string {
	t.Helper()
	var p packs.Plan
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range p.Entries {
		out[string(e.Kind)+":"+e.Key] = strings.TrimSuffix(e.Action+":"+e.Reason, ":")
	}
	return out
}

func slackBundle(t *testing.T) *packs.Bundle {
	t.Helper()
	b, err := packBundle("org", "slack", "")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func desiredHash(t *testing.T, b *packs.Bundle, key string) string {
	t.Helper()
	for _, a := range b.Artifacts {
		if a.Key == key {
			return a.Hash
		}
	}
	t.Fatalf("no artifact %s", key)
	return ""
}

func TestPackPlanNotInstalled(t *testing.T) {
	f, wk, admin := packCatalogFront(t)
	w := call(f, "GET", "/api/v1/admin/packs/slack/plan", "", admin)
	if want := planJSON(t, packs.PlanBundle(slackBundle(t), nil)); w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("plan:\n got %d %s\nwant %s", w.Code, w.Body.String(), want)
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("worker calls: %v", calls)
	}
	w = call(f, "GET", "/api/v1/admin/packs/no-such-pack/plan", "", admin)
	if d, _ := decode(t, w)["diagnostic"].(map[string]any); w.Code != 400 || d["phase"] != "inspection" {
		t.Errorf("unknown pack: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/admin/packs/slack/plan", "", sessionCookie("member")); w.Code != 403 {
		t.Errorf("member: %d", w.Code)
	}
}

func TestPackPlanReadsInstalledState(t *testing.T) {
	f, _, admin := packCatalogFront(t)
	db := f.API.DB
	bundle := slackBundle(t)
	skillDir := filepath.Join(t.TempDir(), "slack-messages")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Slack\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillHash, err := packs.HashMarkdownTree(skillDir)
	if err != nil || skillHash == nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into pack_action_definition (org_id, kind, description, definition, definition_hash, enabled)
		values ('org', 'slack.message', 'Send', '{"steps":[{"call":"chat.postMessage","n":1.5}]}', 'dh', true)`)
	seed(t, db, `insert into action_policy (org_id, name, mode, priority, applies_to_kinds, allowed_targets, limits)
		values ('org', 'Slack governed messages', 'approval_required', 10, '{slack.message}', '{"source":"slack"}', '{}')`)
	var actionRow, policyRow string
	if err := db.QueryRow(context.Background(), `select to_jsonb(t)::text from pack_action_definition t where kind = 'slack.message'`).Scan(&actionRow); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(context.Background(), `select to_jsonb(t)::text from action_policy t where name = 'Slack governed messages'`).Scan(&policyRow); err != nil {
		t.Fatal(err)
	}
	var action, policy map[string]any
	_ = json.Unmarshal([]byte(actionRow), &action)
	_ = json.Unmarshal([]byte(policyRow), &policy)
	actionHash, _ := packs.NativeStateHash(packs.KindAction, action)
	policyHash, _ := packs.NativeStateHash(packs.KindPolicy, policy)

	install := "00000000-0000-0000-0000-0000000000aa"
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, config)
		values ('`+install+`', 'org', 'slack', '0.1.0', 'installed', 'h', '{}')`)
	type art struct{ kind, key, target, desired, applied, ownership, metadata string }
	arts := []art{
		{"action", "action.slack_message", "slack.message", desiredHash(t, bundle, "action.slack_message"), actionHash, "managed", `{"stateHashVersion":1,"locator":{"kind":"slack.message"}}`},
		{"policy", "policy.slack_governed_messages", "slack.policy.governed_messages", "old", "stale", "managed", `{"stateHashVersion":1,"locator":{"name":"Slack governed messages"}}`},
		{"policy", "policy.slack_approved_channels", "slack.policy.approved_channels", "old", "applied", "managed", `{}`},
		{"skill", "skill.slack-messages", "slack-messages", "old", *skillHash, "managed", `{"stateHashVersion":1,"materializedTarget":"` + skillDir + `"}`},
		{"source", "source.slack", "slack", desiredHash(t, bundle, "source.slack"), "s", "managed", `{"stateHashVersion":1}`},
		{"workflow", "workflow.gone", "slack.workflow.gone", "w", "w", "managed", `{"stateHashVersion":1}`},
		{"workflow", "workflow.left", "slack.workflow.left", "w", "w", "detached", `{}`},
	}
	for _, a := range arts {
		if _, err := db.Exec(context.Background(), `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash, ownership, metadata)
			values ($1, 'org', $2, $3, $4, $5, $6, $7, $8::jsonb)`, install, a.kind, a.key, a.target, a.desired, a.applied, a.ownership, a.metadata); err != nil {
			t.Fatal(err)
		}
	}
	w := call(f, "GET", "/api/v1/admin/packs/slack/plan", "", admin)
	if w.Code != 200 {
		t.Fatalf("plan: %d %s", w.Code, w.Body.String())
	}
	got := planActions(t, w.Body.String())
	for key, want := range map[string]string{
		"action:action.slack_message":           "noop",
		"policy:policy.slack_governed_messages": "conflict:user_modified",
		"policy:policy.slack_approved_channels": "conflict:target_missing",
		"skill:skill.slack-messages":            "update",
		"source:source.slack":                   "conflict:target_missing",
		"workflow:workflow.gone":                "conflict:user_modified",
		"saved_query:saved_query.channels":      "create",
	} {
		if got[key] != want {
			t.Errorf("%s: got %q, want %q", key, got[key], want)
		}
	}
	if _, ok := got["workflow:workflow.left"]; ok {
		t.Errorf("a detached artifact that left the pack must not be planned")
	}
	current := func(h string) *string { return &h }
	want := packs.PlanBundle(bundle, []packs.InstalledArtifact{
		{Kind: "action", Key: "action.slack_message", TargetRef: "slack.message", DesiredHash: arts[0].desired, LastAppliedHash: actionHash, CurrentHash: current(actionHash), Ownership: "managed"},
		{Kind: "policy", Key: "policy.slack_governed_messages", TargetRef: "slack.policy.governed_messages", DesiredHash: "old", LastAppliedHash: "stale", CurrentHash: current(policyHash), Ownership: "managed"},
		{Kind: "policy", Key: "policy.slack_approved_channels", TargetRef: "slack.policy.approved_channels", DesiredHash: "old", LastAppliedHash: "applied", Ownership: "managed"},
		{Kind: "skill", Key: "skill.slack-messages", TargetRef: "slack-messages", DesiredHash: "old", LastAppliedHash: *skillHash, CurrentHash: skillHash, Ownership: "managed"},
		{Kind: "source", Key: "source.slack", TargetRef: "slack", DesiredHash: arts[4].desired, LastAppliedHash: "s", Ownership: "managed"},
		{Kind: "workflow", Key: "workflow.gone", TargetRef: "slack.workflow.gone", DesiredHash: "w", LastAppliedHash: "w", Ownership: "managed"},
		{Kind: "workflow", Key: "workflow.left", TargetRef: "slack.workflow.left", DesiredHash: "w", LastAppliedHash: "w", Ownership: "detached"},
	})
	if body := strings.TrimSpace(w.Body.String()); body != planJSON(t, want) {
		t.Errorf("plan:\n got %s\nwant %s", body, planJSON(t, want))
	}

	if err := os.WriteFile(filepath.Join(skillDir, "run.sh"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w = call(f, "GET", "/api/v1/admin/packs/slack/plan", "", admin)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "pack-owned skill contains a non-Markdown file") {
		t.Errorf("non-Markdown skill file: %d %s", w.Code, w.Body.String())
	}
}

func TestPackPlanReadsTheLiveGraphjin(t *testing.T) {
	f, _, admin := packCatalogFront(t)
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("plan-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := f.API.DB
	bundle := slackBundle(t)
	liveSource := map[string]any{"name": "slack", "kind": "api", "read_only": false, "access": map[string]any{"roles": []any{}},
		"specs": map[string]any{"slack": map[string]any{"base_url": "https://slack.com/api", "document": "openapi: 3.0.0\n"}}}
	query := "query Channels { channels { id } }\n"
	var asked []string
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(raw, &body)
		asked = append(asked, body.Query)
		if !strings.HasPrefix(r.Header.Get("authorization"), "Bearer ey") || r.URL.Path != "/api/v1/graphql" {
			w.WriteHeader(401)
			return
		}
		switch {
		case strings.Contains(body.Query, "PackGraphjinTarget"):
			_, _ = io.WriteString(w, `{"data":{"gj_config":{"serv":"{\"production\":false,\"auth\":{\"Type\":\"jwt\"}}"}}}`)
		case strings.Contains(body.Query, "PackLiveConfig_"):
			src, _ := json.Marshal([]any{liveSource})
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"gj_config": []any{map[string]any{
				"catalog_revision": "r1", "sources": string(src), "tables": []any{}, "relationships": []any{}}}}})
		case strings.Contains(body.Query, "PackSavedQuery_"):
			text := ""
			if body.Variables["id"] == "saved_query:channels" {
				text = query
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"gj_catalog": map[string]any{"id": body.Variables["id"], "graphql_query": text}}})
		default:
			_, _ = io.WriteString(w, `{"errors":[{"message":"unexpected"}]}`)
		}
	}))
	t.Cleanup(gj.Close)
	var sourceID string
	if err := db.QueryRow(context.Background(), `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode)
		values ('org', 'graphjin', 'GJ', $1, true, true, 'jwt') returning id::text`, gj.URL+"/").Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	install := "00000000-0000-0000-0000-0000000000bb"
	cfg, _ := json.Marshal(map[string]any{"_runtime": map[string]any{"source": map[string]any{"id": sourceID, "graphqlUrl": gj.URL + "/", "authMode": "jwt"}}})
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, config)
		values ('`+install+`', 'org', 'slack', '0.1.0', 'installed', 'h', '`+string(cfg)+`')`)
	sourceHash, _ := packs.LiveSourceStateHash(liveSource, "")
	queryHash := packs.SHA256([]byte(strings.TrimSpace(query)))
	for _, a := range []struct{ kind, key, target, desired, applied, metadata string }{
		{"source", "source.slack", "slack", desiredHash(t, bundle, "source.slack"), sourceHash, `{"stateHashVersion":1}`},
		{"saved_query", "saved_query.channels", "saved_query.channels", desiredHash(t, bundle, "saved_query.channels"), "edited", `{"stateHashVersion":1,"materializedTarget":"graphjin:saved_query:channels"}`},
		{"spec", "spec.slack", "spec.slack", "old", "x", `{"stateHashVersion":1,"materializedTarget":"graphjin:spec:specs/missing.yaml"}`},
	} {
		if _, err := db.Exec(context.Background(), `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash, metadata)
			values ($1, 'org', $2, $3, $4, $5, $6, $7::jsonb)`, install, a.kind, a.key, a.target, a.desired, a.applied, a.metadata); err != nil {
			t.Fatal(err)
		}
	}
	w := call(f, "GET", "/api/v1/admin/packs/slack/plan", "", admin)
	if w.Code != 200 {
		t.Fatalf("plan: %d %s", w.Code, w.Body.String())
	}
	got := planActions(t, w.Body.String())
	if got["source:source.slack"] != "noop" || got["saved_query:saved_query.channels"] != "conflict:user_modified" || got["spec:spec.slack"] != "conflict:target_missing" {
		t.Errorf("actions %v", got)
	}
	if !strings.Contains(w.Body.String(), queryHash) {
		t.Errorf("the saved query hash %s is not in the plan: %s", queryHash, w.Body.String())
	}
	live := 0
	for _, q := range asked {
		if strings.Contains(q, "PackLiveConfig_") {
			live++
		}
	}
	if live != 1 {
		t.Errorf("live config reads: %d, want 1 (%v)", live, asked)
	}

	seed(t, db, `update data_source set graphql_url = 'http://moved' where id::text = '`+sourceID+`'`)
	w = call(f, "GET", "/api/v1/admin/packs/slack/plan", "", admin)
	if got := planActions(t, w.Body.String()); w.Code != 200 || got["source:source.slack"] != "conflict:target_missing" {
		t.Errorf("a moved data source must read as missing: %d %v", w.Code, got)
	}
}
