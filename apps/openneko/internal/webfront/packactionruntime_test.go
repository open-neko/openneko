package webfront

import (
	"context"
	"encoding/json"
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
