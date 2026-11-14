package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type fakeScalekitMCP struct {
	mu           sync.Mutex
	refreshCount int
	deletedSIDs  []string
	seenTokens   []string
	toolCalls    []map[string]any
	useSSE       bool
	isError      bool
	failHTTP     bool
	toolReplies  map[string]string
}

func (f *fakeScalekitMCP) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var raw []byte
	if r.Body != nil {
		raw, _ = io.ReadAll(r.Body)
	}
	reply := func(code int, ct string, headers map[string]string, body string) *http.Response {
		h := http.Header{"Content-Type": {ct}}
		for k, v := range headers {
			h.Set(k, v)
		}
		return &http.Response{
			StatusCode: code,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}
	if r.URL.Path == "/token" {
		vals, _ := url.ParseQuery(string(raw))
		if vals.Get("grant_type") != "refresh_token" || vals.Get("refresh_token") == "" {
			return reply(http.StatusBadRequest, "application/json", nil, `{"error":"invalid_request"}`), nil
		}
		f.refreshCount++
		return reply(http.StatusOK, "application/json", nil, `{"access_token":"at-refreshed","refresh_token":"rt-rotated","expires_in":3600}`), nil
	}
	if r.Method == http.MethodDelete {
		f.deletedSIDs = append(f.deletedSIDs, r.Header.Get("Mcp-Session-Id"))
		return reply(http.StatusNoContent, "text/plain", nil, ""), nil
	}
	if f.failHTTP {
		return reply(http.StatusBadGateway, "text/plain", nil, "scalekit down"), nil
	}
	var rpc struct {
		ID     any            `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	_ = json.Unmarshal(raw, &rpc)
	switch rpc.Method {
	case "initialize":
		return reply(http.StatusOK, "application/json", map[string]string{"Mcp-Session-Id": "sess-1"},
			`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{},"serverInfo":{"name":"scalekit","version":"1.0.0"}}}`), nil
	case "notifications/initialized":
		return reply(http.StatusAccepted, "application/json", nil, ""), nil
	case "tools/call":
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.seenTokens = append(f.seenTokens, auth)
		name, _ := rpc.Params["name"].(string)
		args, _ := rpc.Params["arguments"].(map[string]any)
		f.toolCalls = append(f.toolCalls, map[string]any{
			"name":      name,
			"args":      args,
			"sessionId": r.Header.Get("Mcp-Session-Id"),
		})
		text := f.toolReplies[name]
		if text == "" {
			text = "ok"
		}
		resObj := map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": f.isError,
		}
		encoded, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      rpc.ID,
			"result":  resObj,
		})
		if f.useSSE {
			nl := string(byte(10))
			return reply(http.StatusOK, "text/event-stream", nil, "event: message"+nl+"data: "+string(encoded)+nl+nl), nil
		}
		return reply(http.StatusOK, "application/json", nil, string(encoded)), nil
	default:
		return reply(http.StatusNotFound, "application/json", nil, `{"error":"not found"}`), nil
	}
}

func TestSSOSetupSetScalekitIDsAndCredentials(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org-sso', 'SSO Org')`)
	dir := t.TempDir()
	api := &API{Gate: &Gate{SecretsDir: dir, DB: db}, DB: db, SecretsDir: dir}

	if err := api.ssoSetScalekitIDs(ctx, "org-sso", "env_1", "org_9", "dev"); err != nil {
		t.Fatal(err)
	}
	status, err := api.ssoGetStatus(ctx, "org-sso")
	if err != nil {
		t.Fatal(err)
	}
	if status.EnvironmentID == nil || *status.EnvironmentID != "env_1" ||
		status.OrganizationID == nil || *status.OrganizationID != "org_9" ||
		status.EnvironmentTier == nil || *status.EnvironmentTier != "dev" ||
		status.SignInConfigured {
		t.Fatalf("status after setScalekitIDs = %+v", status)
	}
	var scalekitOrgID string
	if err := db.QueryRow(ctx, `select coalesce(scalekit_org_id, '') from organization where id = 'org-sso'`).Scan(&scalekitOrgID); err != nil || scalekitOrgID != "org_9" {
		t.Fatalf("organization.scalekit_org_id = %q (%v)", scalekitOrgID, err)
	}
	store, err := secrets.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store["signin.scalekit"]["SCALEKIT_ENVIRONMENT_ID"] != "env_1" || store["signin.scalekit"]["SCALEKIT_ORGANIZATION_ID"] != "org_9" {
		t.Fatalf("signin.scalekit = %v", store["signin.scalekit"])
	}

	if err := api.ssoSetSignInCredentials(ctx, "org-sso", "https://acme.scalekit.dev", "skc_1", "secret"); err != nil {
		t.Fatal(err)
	}
	store, err = secrets.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store["signin"]["PROVIDER"] != "scalekit" ||
		store["signin.scalekit"]["SCALEKIT_ENVIRONMENT_URL"] != "https://acme.scalekit.dev" ||
		store["signin.scalekit"]["SCALEKIT_CLIENT_ID"] != "skc_1" ||
		store["signin.scalekit"]["SCALEKIT_CLIENT_SECRET"] != "secret" {
		t.Fatalf("store after setSignInCredentials = %v", store)
	}
	if err := api.ssoSetSignInCredentials(ctx, "org-sso", "", "x", "y"); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required error, got %v", err)
	}
}

func TestSSOSetupWorkspaceToolsAndRefresh(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mcp := &fakeScalekitMCP{
		useSSE:      true,
		toolReplies: map[string]string{"list_environments": "ID: env_1 | Name: Dev Env | Type: DEV | Domain: dev.scalekit.com | Status: active"},
	}
	api := &API{
		Gate:         &Gate{SecretsDir: dir},
		SecretsDir:   dir,
		SignInClient: &http.Client{Transport: mcp},
	}

	if _, err := api.callScalekitWorkspaceTool(ctx, "list_environments", nil); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("unconnected error = %v", err)
	}

	if err := writeScalekitOperatorCredential(dir, &scalekitOperatorCredential{
		Tokens: map[string]any{
			"access_token":   "at-old",
			"refresh_token":  "rt-old",
			"expires_at":     "0",
			"token_endpoint": "https://as.scalekit.com/token",
			"client_id":      "dcr-client-1",
			"client_secret":  "",
		},
		ConnectedAt: "2026-01-01T00:00:00.000Z",
	}); err != nil {
		t.Fatal(err)
	}

	envs, err := api.ssoListEnvironments(ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 || envs[0].ID != "env_1" || envs[0].Name == nil || *envs[0].Name != "Dev Env" ||
		envs[0].Tier == nil || *envs[0].Tier != "DEV" || envs[0].Domain == nil || *envs[0].Domain != "dev.scalekit.com" {
		t.Fatalf("environments = %+v", envs)
	}
	if mcp.refreshCount != 1 || len(mcp.seenTokens) != 1 || mcp.seenTokens[0] != "at-refreshed" {
		t.Fatalf("refreshCount=%d seenTokens=%v", mcp.refreshCount, mcp.seenTokens)
	}
	if len(mcp.deletedSIDs) != 1 || mcp.deletedSIDs[0] != "sess-1" {
		t.Fatalf("deletedSIDs = %v", mcp.deletedSIDs)
	}
	stored, err := readScalekitOperatorCredential(dir)
	if err != nil || stored == nil || stored.Tokens["access_token"] != "at-refreshed" || stored.Tokens["refresh_token"] != "rt-rotated" || scalekitCredentialNearExpiry(stored, time.Now()) {
		t.Fatalf("stored credential after refresh = %+v (%v)", stored, err)
	}

	mcp.isError = true
	mcp.toolReplies["list_environments"] = "forbidden"
	if _, err := api.ssoListEnvironments(ctx, "org-1"); err == nil || err.Error() != "forbidden" {
		t.Fatalf("tool error = %v", err)
	}
}

func TestSSOSetupFullFlowAndPoll(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org-1', 'Org 1'), ('org-2', 'Org 2')`)
	dir := t.TempDir()
	mcp := &fakeScalekitMCP{
		toolReplies: map[string]string{
			"list_organizations":            "Organization 1:" + string(byte(10)) + "  ID: org_sk_1" + string(byte(10)) + "  Name: Acme Corp" + string(byte(10)) + "Organization 2:" + string(byte(10)) + "  ID: org_sk_2" + string(byte(10)) + "  Name: Beta",
			"get_environment_credentials":   "SCALEKIT_ENVIRONMENT_URL=https://acme.scalekit.dev" + string(byte(10)) + "SCALEKIT_CLIENT_ID=skc_123",
			"generate_admin_portal_link":    "Portal ready" + string(byte(10)) + "Link: https://portal.scalekit.com/setup/123",
			"list_organization_connections": "connection 1: status: IN_PROGRESS provider: OKTA",
		},
	}
	api := &API{
		Gate:         &Gate{SecretsDir: dir, DB: db},
		DB:           db,
		SecretsDir:   dir,
		SignInClient: &http.Client{Transport: mcp},
	}
	if err := writeScalekitOperatorCredential(dir, &scalekitOperatorCredential{
		Tokens: map[string]any{
			"access_token": "at-valid",
			"expires_at":   "4102444800000",
		},
		ConnectedAt: "2026-01-01T00:00:00.000Z",
	}); err != nil {
		t.Fatal(err)
	}

	orgs, err := api.ssoListOrganizations(ctx, "org-1", "env_1")
	if err != nil || len(orgs) != 2 || orgs[0].ID != "org_sk_1" || orgs[0].Name == nil || *orgs[0].Name != "Acme Corp" {
		t.Fatalf("listOrganizations = %+v (%v)", orgs, err)
	}
	creds, err := api.ssoGetEnvironmentCredentials(ctx, "org-1", "env_1")
	if err != nil || creds.EnvironmentURL != "https://acme.scalekit.dev" || creds.ClientID != "skc_123" {
		t.Fatalf("getEnvironmentCredentials = %+v (%v)", creds, err)
	}

	if err := api.ssoSetScalekitIDs(ctx, "org-1", "env_1", "org_sk_1", "dev"); err != nil {
		t.Fatal(err)
	}
	link, err := api.ssoGeneratePortalLink(ctx, "org-1")
	if err != nil || link.PortalLink != "https://portal.scalekit.com/setup/123" || link.ExpiresAt != nil {
		t.Fatalf("generatePortalLink = %+v (%v)", link, err)
	}

	// Polling org-2 (not_started) is a no-op; polling org-1 (awaiting_portal) transitions to polling.
	if err := api.SSOSetupPoll(ctx); err != nil {
		t.Fatal(err)
	}
	st1, err := api.ssoGetStatus(ctx, "org-1")
	if err != nil || st1.Status != "polling" || st1.Provider == nil || *st1.Provider != "OKTA" {
		t.Fatalf("status after in-progress poll = %+v (%v)", st1, err)
	}

	// Next poll when connection reports COMPLETED transitions to connected.
	mcp.toolReplies["list_organization_connections"] = "connection 1: status: COMPLETED provider: OKTA"
	if err := api.SSOSetupPoll(ctx); err != nil {
		t.Fatal(err)
	}
	st2, err := api.ssoGetStatus(ctx, "org-1")
	if err != nil || st2.Status != "connected" || st2.SetupCompletedAt == nil {
		t.Fatalf("status after completed poll = %+v (%v)", st2, err)
	}

	// Once connected, SSOSetupPoll makes no further MCP calls.
	beforeCalls := len(mcp.toolCalls)
	if err := api.SSOSetupPoll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mcp.toolCalls) != beforeCalls {
		t.Fatalf("expected no tool calls when connected, got %d -> %d", beforeCalls, len(mcp.toolCalls))
	}
}
