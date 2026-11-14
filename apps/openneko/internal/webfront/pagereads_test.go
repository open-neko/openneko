package webfront

import (
	"encoding/json"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

func TestDeviceRequestRead(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	seed(t, db, `insert into app_device_request (org_id, device_code_verifier, user_code, name, expires_at)
		values ('org', 'hash-read', 'BCDFGHJK', 'My Tablet', now() + interval '5 minutes')`)
	w := call(f, "GET", "/api/v1/auth/device/approve?code=bcdf-ghjk", "", nil)
	var found struct {
		Request *pendingDevice `json:"request"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &found) != nil || found.Request == nil ||
		found.Request.UserCode != "BCDF-GHJK" || found.Request.Name != "My Tablet" {
		t.Fatalf("found: %d %s", w.Code, w.Body.String())
	}
	w = call(f, "GET", "/api/v1/auth/device/approve?code=ZZZZZZZZ", "", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &found) != nil || found.Request != nil {
		t.Fatalf("missing: %d %s", w.Code, w.Body.String())
	}
}

func TestPersonalPackAccountsList(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	f.API.SecretsDir = t.TempDir()
	if err := secrets.Write(secrets.Store{
		"pack.google-workspace": {
			"OAUTH_WORKSPACE_CLIENT_ID": "cid",
			"GOOGLE_CLIENT_SECRET":      "csec",
		},
	}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	var installID string
	if err := db.QueryRow(t.Context(), `
		insert into pack_install (org_id, pack_id, version, status, manifest_hash, config)
		values ('org', 'google-workspace', '0.2.0', 'installed', 'h', $1::jsonb)
		returning id::text`,
		`{"_userOAuth":[{"key":"workspace","providerLabel":"Google Workspace","scopes":["openid"],"clientSecret":"GOOGLE_CLIENT_SECRET"}]}`).Scan(&installID); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into pack_user_connection (org_id, user_id, pack_install_id, connection_key, credentials, account_label)
		values ('org', 'admin', $1::uuid, 'workspace', 'enc:v1:cipher', 'admin@example.com')`, installID)
	w := call(f, "GET", "/api/v1/my/pack-accounts", "", sessionCookie("admin"))
	var body struct {
		Connections []personalPackStatus `json:"connections"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Connections) != 1 {
		t.Fatalf("admin list: %d %s", w.Code, w.Body.String())
	}
	c := body.Connections[0]
	if c.PackID != "google-workspace" || c.Key != "workspace" || !c.Configured || !c.Connected ||
		c.AccountLabel == nil || *c.AccountLabel != "admin@example.com" {
		t.Errorf("connection: %+v", c)
	}
	// Member sees their own disconnected state, not the admin's account.
	w = call(f, "GET", "/api/v1/my/pack-accounts", "", sessionCookie("member"))
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Connections) != 1 ||
		body.Connections[0].Connected || body.Connections[0].AccountLabel != nil {
		t.Fatalf("member disconnected: %d %s", w.Code, w.Body.String())
	}
	// Member without a pack grant sees no ungranted pack.
	seed(t, db, `delete from item_grant where org_id = 'org' and item_type = 'pack'`)
	w = call(f, "GET", "/api/v1/my/pack-accounts", "", sessionCookie("member"))
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Connections) != 0 {
		t.Fatalf("member ungranted: %d %s", w.Code, w.Body.String())
	}
}

func TestOnboardingWizardRead(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	if w := call(f, "GET", "/api/v1/onboarding/wizard", "", sessionCookie("member")); w.Code != 403 {
		t.Fatalf("member: %d %s", w.Code, w.Body.String())
	}
	w := call(f, "GET", "/api/v1/onboarding/wizard", "", sessionCookie("admin"))
	var step map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &step) != nil || step["kind"] != "settings" {
		t.Fatalf("before setup: %d %s", w.Code, w.Body.String())
	}
	seed(t, db, `update organization set setup_complete_at = now(), name = 'Acme' where id = 'org'`)
	seed(t, db, `insert into onboarding_wizard (org_id, company_note, fiscal_year_start_month, active_seats, priorities)
		values ('org', 'We sell bikes', 4, array['CEO','CFO'], array['Margin'])`)
	w = call(f, "GET", "/api/v1/onboarding/wizard", "", sessionCookie("admin"))
	var wiz struct {
		Kind     string        `json:"kind"`
		Initial  wizardInitial `json:"initial"`
		TeamMode bool          `json:"teamMode"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &wiz) != nil || wiz.Kind != "wizard" || !wiz.TeamMode ||
		wiz.Initial.CompanyName != "Acme" || wiz.Initial.CompanyNote != "We sell bikes" || wiz.Initial.FiscalYearStartMonth != 4 {
		t.Fatalf("wizard: %d %s", w.Code, w.Body.String())
	}
	seed(t, db, `insert into customer_profile (org_id, version, is_current, business_profile)
		values ('org', 1, true, '# Acme')`)
	seed(t, db, `insert into operator_profile (org_id, user_id, role_template, focus_areas)
		values ('org', 'admin', 'VP Ops', array['Stock'])`)
	w = call(f, "GET", "/api/v1/onboarding/wizard", "", sessionCookie("admin"))
	var persona struct {
		Kind                string   `json:"kind"`
		InitialRoleTemplate string   `json:"initialRoleTemplate"`
		InitialFocusAreas   []string `json:"initialFocusAreas"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &persona) != nil || persona.Kind != "persona" ||
		persona.InitialRoleTemplate != "VP Ops" || len(persona.InitialFocusAreas) != 1 || persona.InitialFocusAreas[0] != "Stock" {
		t.Fatalf("persona: %d %s", w.Code, w.Body.String())
	}
}
