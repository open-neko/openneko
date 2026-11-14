package webfront

import (
	"bytes"
	"log"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestRefreshPackOAuth(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	provider := &fakeProvider{token: `{"access_token":"at-new","expires_in":3600,"scope":"openid email"}`}
	srv := httptest.NewTLSServer(provider)
	t.Cleanup(srv.Close)
	saved := providerClient
	providerClient = srv.Client()
	t.Cleanup(func() { providerClient = saved })
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	deploymentWorkspacePack(t, srv.URL)
	f, wk := newSetupFront(t, `{"provider":{"pluginName":"magic-link"}}`, db)
	a := f.API
	gj := &fakePackGraphjin{}
	gjSrv := gj.server(t, map[string]any{"name": "other", "kind": "api"}, "")
	var sourceID string
	if err := db.QueryRow(t.Context(), `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode)
		values ('org', 'graphjin', 'GJ', $1, true, true, 'jwt') returning id::text`, gjSrv.URL).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}

	if n, err := a.RefreshPackOAuth(t.Context()); n != 0 || err != nil {
		t.Fatalf("no pack: %d %v", n, err)
	}
	seed(t, db, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config) values
		('org', 'google-workspace', '0.2.0', 'installed', 'h', jsonb_build_object('_runtime', jsonb_build_object('bindings', '{}'::jsonb,
		'source', jsonb_build_object('id', $1::text, 'graphqlUrl', $2::text, 'authMode', 'jwt'))))`, sourceID, gjSrv.URL)
	section := func(expires time.Time, pending bool) {
		s := map[string]string{"OAUTH_WORKSPACE_CLIENT_ID": "cid", "GOOGLE_WORKSPACE_OAUTH_CLIENT_SECRET": "cs-secret",
			"GOOGLE_WORKSPACE_ACCESS_TOKEN": "at-old", "GOOGLE_WORKSPACE_REFRESH_TOKEN": "rt-secret",
			"OAUTH_WORKSPACE_EXPIRES_AT": expires.UTC().Format("2006-01-02T15:04:05.000Z"), "OAUTH_WORKSPACE_SCOPES": "openid"}
		if pending {
			s["OAUTH_WORKSPACE_SYNC_PENDING"] = "true"
		}
		if err := secrets.Write(secrets.Store{"pack.google-workspace": s, "other": {"KEY": "v"}}, a.SecretsDir); err != nil {
			t.Fatal(err)
		}
	}
	stored := func() map[string]string {
		store, err := secrets.Read(a.SecretsDir)
		if err != nil {
			t.Fatal(err)
		}
		if store["other"]["KEY"] != "v" {
			t.Errorf("other section %v", store["other"])
		}
		return store["pack.google-workspace"]
	}

	section(time.Now().Add(time.Hour), false)
	if n, err := a.RefreshPackOAuth(t.Context()); n != 0 || err != nil || len(provider.forms) != 0 || len(gj.take()) != 0 {
		t.Errorf("fresh token: %d %v", n, err)
	}

	section(time.Now().Add(time.Hour), true)
	if n, err := a.RefreshPackOAuth(t.Context()); n != 0 || err != nil || len(provider.forms) != 0 {
		t.Errorf("pending sync: %d %v", n, err)
	}
	if m := gj.take(); len(m) != 2 || !strings.Contains(m[1], `token: \"at-old\"`) {
		t.Errorf("pending sync mutations %v", m)
	}
	if _, ok := stored()["OAUTH_WORKSPACE_SYNC_PENDING"]; ok {
		t.Error("pending mark stays after the sync")
	}

	section(time.Now().Add(time.Minute), false)
	gj.failApply = true
	if n, err := a.RefreshPackOAuth(t.Context()); n != 0 || err == nil || !strings.Contains(err.Error(), "apply refused") {
		t.Errorf("failed sync: %d %v", n, err)
	}
	if s := stored(); s["GOOGLE_WORKSPACE_ACCESS_TOKEN"] != "at-new" || s["OAUTH_WORKSPACE_SYNC_PENDING"] != "true" {
		t.Errorf("after a failed sync %v", s)
	}
	gj.take()
	gj.failApply = false

	section(time.Now().Add(time.Minute), false)
	provider.forms = nil
	if n, err := a.RefreshPackOAuth(t.Context()); n != 1 || err != nil {
		t.Fatalf("due token: %d %v", n, err)
	}
	if len(provider.forms) != 1 || provider.forms[0].Get("grant_type") != "refresh_token" || provider.forms[0].Get("refresh_token") != "rt-secret" ||
		provider.forms[0].Get("client_id") != "cid" || provider.forms[0].Get("client_secret") != "cs-secret" {
		t.Errorf("token form %v", provider.forms)
	}
	if m := gj.take(); len(m) != 2 || !strings.Contains(m[1], `token: \"at-new\"`) {
		t.Errorf("refresh mutations %v", m)
	}
	s := stored()
	expires, _ := time.Parse(time.RFC3339Nano, s["OAUTH_WORKSPACE_EXPIRES_AT"])
	if s["GOOGLE_WORKSPACE_ACCESS_TOKEN"] != "at-new" || s["GOOGLE_WORKSPACE_REFRESH_TOKEN"] != "rt-secret" ||
		s["OAUTH_WORKSPACE_SCOPES"] != "openid email" || time.Until(expires) < 50*time.Minute || s["OAUTH_WORKSPACE_SYNC_PENDING"] != "" {
		t.Errorf("stored after refresh %v", s)
	}

	section(time.Now().Add(-time.Minute), false)
	provider.token = `{"error":"invalid_grant"}`
	if n, err := a.RefreshPackOAuth(t.Context()); n != 0 || err == nil || !strings.Contains(err.Error(), "1 pack OAuth refresh operation(s) failed") {
		t.Errorf("provider refusal: %d %v", n, err)
	}
	if len(wk.calls) != 0 {
		t.Errorf("the worker was called: %v", wk.calls)
	}
	for _, secret := range []string{"cs-secret", "rt-secret", "at-new", "at-old"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("%s appears in the log", secret)
		}
	}
}
