package webfront

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

func TestPackConnectionHeaderMatchesNode(t *testing.T) {
	for _, c := range [][3]string{
		{"shop", "account", "X-OpenNeko-Pack-a91b4b8c184e13cc8b194d71"},
		{"dr</ve>", "a&b", "X-OpenNeko-Pack-c4863902214561540b4246f0"},
	} {
		if got := packConnectionHeader(c[0], c[1]); got != c[2] {
			t.Errorf("%s %s: %s", c[0], c[1], got)
		}
	}
}

func TestPackUserConnectionHeaders(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	t.Setenv("OPENNEKO_GRAPHJIN_URL", "http://127.0.0.1:8089")
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", "")
	t.Setenv("NODE_ENV", "")

	gj := &fakeGraphjinMCP{}
	gjSrv := httptest.NewServer(gj)
	defer gjSrv.Close()
	var mu sync.Mutex
	var forms []string
	reply := `{"access_token":"at-new","expires_in":3600,"scope":"read"}`
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		forms = append(forms, r.PostForm.Encode())
		body := reply
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(body))
	}))
	defer provider.Close()

	gql, mcpURL := gjSrv.URL+"/api/v1/graphql", gjSrv.URL+"/api/v1/mcp"
	var sourceID, installID string
	if err := db.QueryRow(ctx, `insert into data_source (org_id, kind, name, graphql_url, mcp_url, auth_mode, enabled, is_default)
		values ('org', 'graphjin', 'shop', $1, $2, 'jwt', true, true) returning id::text`, gql, mcpURL).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	runtime := `"_runtime":{"source":{"id":"` + sourceID + `","graphqlUrl":"` + gql + `","authMode":"jwt"}}`
	if err := db.QueryRow(ctx, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config)
		values ('org', 'shop', '1', 'installed', 'h', $1) returning id::text`,
		`{`+runtime+`,"_userOAuth":[{"key":"account","providerLabel":"Shop","tokenUrl":"`+provider.URL+
			`/token","clientSecret":"shop-secret","scopes":["read"]}]}`).Scan(&installID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.SecretsDir, "secrets.json"),
		[]byte(`{"pack.shop":{"OAUTH_ACCOUNT_CLIENT_ID":"cid","SHOP_SECRET":"shh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := func(access, expires string) {
		t.Helper()
		sealed, err := config.EncryptValue(a.SecretsDir, `{"accessToken":"`+access+`","refreshToken":"rt-1","expiresAt":"`+expires+`","scopes":["read"]}`)
		if err != nil {
			t.Fatal(err)
		}
		seed(t, db, `insert into pack_user_connection (org_id, user_id, pack_install_id, connection_key, credentials)
			values ('org', 'member', $1::uuid, 'account', $2)
			on conflict (org_id, user_id, pack_install_id, connection_key) do update set credentials = excluded.credentials`, installID, sealed)
	}
	later := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	store("at-old", later)

	header := packConnectionHeader("shop", "account")
	bearer := func(userID, role string) map[string]string {
		t.Helper()
		token, err := a.mintGraphjinToken(graphjinClaims{orgID: "org", userID: userID, role: role}, 5*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]string{"authorization": "Bearer " + token, "accept": "application/json"}
	}
	headersFor := func(base string, in map[string]string, expected []connectionBinding) (map[string]string, error) {
		return a.packUserConnectionHeaders(ctx, base, in, expected)
	}
	got := func(base string, in map[string]string) string {
		t.Helper()
		out, err := headersFor(base, in, nil)
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if out["accept"] != "application/json" && in["accept"] != "" {
			t.Fatalf("lost a header: %v", out)
		}
		return out[header]
	}

	if v := got(mcpURL, bearer("member", "member")); v != "at-old" {
		t.Fatalf("member on MCP: %q", v)
	}
	if v := got(gql, bearer("member", "member")); v != "at-old" {
		t.Fatalf("member on GraphQL: %q", v)
	}
	if v := got("https://evil.test", bearer("member", "member")); v != "" {
		t.Fatal("unknown endpoint got the token")
	}
	if v := got(mcpURL, bearer("other", "member")); v != "" {
		t.Fatal("another user got the member's token")
	}
	if v := got(mcpURL, bearer("", "service")); v != "" {
		t.Fatal("the service actor got a user's token")
	}
	if v := got(mcpURL, map[string]string{strings.ToLower(header): "forged", "X-OPENNEKO-PACK-x": "y"}); v != "" {
		t.Fatal("a supplied pack header passed")
	}
	if out, _ := headersFor(mcpURL, map[string]string{"X-OpenNeko-Pack-x": "y", "accept": "a"}, nil); len(out) != 1 {
		t.Fatalf("pack headers kept: %v", out)
	}
	forged := bearer("member", "member")
	forged["authorization"] = forged["authorization"][:strings.LastIndex(forged["authorization"], ".")+1] + "c2lnbmF0dXJl"
	if v := got(mcpURL, forged); v != "" {
		t.Fatal("a token with a bad signature passed")
	}
	expired, _ := a.mintGraphjinToken(graphjinClaims{orgID: "org", userID: "member", role: "member"}, -time.Second)
	if v := got(mcpURL, map[string]string{"Authorization": "Bearer " + expired}); v != "" {
		t.Fatal("an expired token passed")
	}

	var revision string
	if err := db.QueryRow(ctx, `select revision::text from pack_user_connection where user_id = 'member'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	approved := []connectionBinding{{InstallID: installID, ConnectionKey: "account", Revision: revision}}
	if out, err := headersFor(mcpURL, bearer("member", "member"), approved); err != nil || out[header] != "at-old" {
		t.Fatalf("approved binding: %v %v", out, err)
	}
	for _, c := range []struct {
		base string
		in   map[string]string
		bind []connectionBinding
		want string
	}{
		{mcpURL, bearer("member", "member"), []connectionBinding{{InstallID: installID, ConnectionKey: "account", Revision: "d0000000-0000-4000-8000-000000000009"}},
			"The approved account connection changed; request approval again"},
		{"https://evil.test", bearer("member", "member"), approved, "The approved pack connection is no longer available"},
		{mcpURL, map[string]string{}, approved, "The approved pack connection is no longer available"},
		{mcpURL, bearer("member", "member"), []connectionBinding{{InstallID: installID, ConnectionKey: "other", Revision: revision}},
			"The approved pack connection is no longer available"},
	} {
		if _, err := headersFor(c.base, c.in, c.bind); err == nil || err.Error() != c.want {
			t.Errorf("binding %v: %v", c.bind, err)
		}
	}

	store("at-old", time.Now().Add(30*time.Second).UTC().Format("2006-01-02T15:04:05.000Z"))
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := headersFor(mcpURL, bearer("member", "member"), nil)
			if err != nil {
				t.Error(err)
				return
			}
			results[i] = out[header]
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r != "at-new" {
			t.Fatalf("after refresh: %v", results)
		}
	}
	if len(forms) != 1 || forms[0] != "client_id=cid&client_secret=shh&grant_type=refresh_token&refresh_token=rt-1" {
		t.Fatalf("refreshes: %v", forms)
	}
	var sealed string
	if err := db.QueryRow(ctx, `select credentials from pack_user_connection where user_id = 'member'`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	plain, err := config.MaybeDecryptValue(a.SecretsDir, sealed)
	if !strings.HasPrefix(sealed, "enc:v1:") || err != nil || !strings.Contains(plain, `"accessToken":"at-new","refreshToken":"rt-1"`) {
		t.Fatalf("stored: %s %s %v", sealed[:10], plain, err)
	}

	store("at-old", "not a date")
	reply = `{"access_token":"at-narrow","expires_in":3600,"scope":"other"}`
	if v := got(mcpURL, bearer("member", "member")); v != "" {
		t.Fatal("a token without the required scope was sent")
	}
	if _, err := headersFor(mcpURL, bearer("member", "member"), approved); err == nil ||
		err.Error() != "Reconnect your account to grant the required permissions" {
		t.Fatalf("narrow refresh with a binding: %v", err)
	}
	reply = `{"access_token":"at-new","expires_in":3600}`
	store("at-old", later)

	tools, err := turnTools(turnBinding{OrgID: "org", ThreadID: threadMine, RunID: runMine, Kind: "work"},
		a.runTools(turnBinding{OrgID: "org", ThreadID: threadMine, RunID: runMine, Kind: "work"}), []string{"neko_graphjin"})
	if err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'data_source', 'shop' from user_group where org_id = 'org' and slug = 'everyone'`)
	if _, rpcErr := callTool(t, tools, "graphjin_execute_graphql", map[string]any{"query": "{ orders { id } }"}); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	for i, h := range gj.headers {
		if h.Get(header) != "at-old" {
			t.Fatalf("GraphJin request %d had no account token: %v", i, h)
		}
	}
	if gj.count() < 2 {
		t.Fatalf("GraphJin requests: %d", gj.count())
	}

	seed(t, db, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config) values ('org', 'legacy', '1', 'installed', 'h', $1)`,
		`{`+runtime+`,"_oauth":{"x":1}}`)
	if _, err := headersFor(mcpURL, bearer("member", "member"), nil); err == nil ||
		err.Error() != "Ask an admin to upgrade the OAuth pack to confirm its account scope" {
		t.Fatalf("legacy OAuth pack: %v", err)
	}
	if _, rpcErr := callTool(t, tools, "graphjin_execute_graphql", map[string]any{"query": "{ orders { id } }"}); rpcErr == nil ||
		!strings.Contains(rpcErr.Message, "Ask an admin to upgrade the OAuth pack") {
		t.Fatalf("legacy OAuth pack on a call: %v", rpcErr)
	}
	seed(t, db, `delete from pack_install where pack_id = 'legacy'`)

	seed(t, db, `update app_user set disabled_at = now() where id = 'member'`)
	if _, err := headersFor(mcpURL, bearer("member", "member"), nil); err == nil ||
		err.Error() != "Sign in with an active account to manage personal connections" {
		t.Fatalf("disabled user: %v", err)
	}
}
