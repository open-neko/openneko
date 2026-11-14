package webfront

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
	"github.com/open-neko/neko/apps/openneko/internal/workeradmin"
)

const (
	nodeParityToken   = "eyJlbWFpbCI6InBlcnNvbkBjb21wYW55LmNvbSIsInN0YXRlIjoiY3NyZi1zdGF0ZS0xMjMiLCJleHBpcmVzQXQiOjE3MDAwMDA2MDB9.rap5I_IEVMh5Efo259LA1omopZxBCKjjKF1cdMetKdE"
	nodeParityAuthURL = "https://foo.scalekit.com/oauth/authorize?response_type=code&client_id=cid&redirect_uri=https%3A%2F%2Fapp.example.com%2Fapi%2Fauth%2Fcallback&state=s-1&scope=openid+profile+email&login_hint=alice%40example.com"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSignInNodeParityVectors(t *testing.T) {
	secret := strings.Repeat("s", 64)
	now := time.UnixMilli(1_700_000_000_000)
	tok, err := mintMagicLinkToken("person@company.com", "csrf-state-123", secret, 600, now)
	if err != nil || tok != nodeParityToken {
		t.Fatalf("mint token = %q (%v), want %q", tok, err, nodeParityToken)
	}
	claims, err := verifyMagicLinkToken(nodeParityToken, "csrf-state-123", secret, now.Add(60*time.Second))
	if err != nil || claims.Email != "person@company.com" || claims.State != "csrf-state-123" || claims.ExpiresAt != 1_700_000_600 {
		t.Fatalf("verify token = %+v (%v)", claims, err)
	}

	msg := buildSignInEmail(
		"OpenNeko <signin@company.com>",
		"person@company.com",
		"https://neko.example/api/auth/callback?code="+tok+"&state=csrf-state-123",
		10,
	)
	if msg.Subject != "Your OpenNeko sign-in link" || len(msg.HTML) != 3750 ||
		!strings.Contains(msg.Text, `Open this link to sign in as person@company.com:

https://neko.example/api/auth/callback?code=`+tok+`&state=csrf-state-123

The link works once and expires in 10 minutes.`) {
		t.Errorf("email parity mismatch: subject=%q htmlLen=%d text=%q", msg.Subject, len(msg.HTML), msg.Text)
	}

	authURL, err := buildScalekitAuthorizationURL("https://foo.scalekit.com/", "cid", "https://app.example.com/api/auth/callback", "s-1", "alice@example.com", "")
	if err != nil || authURL != nodeParityAuthURL {
		t.Errorf("authURL = %q (%v), want %q", authURL, err, nodeParityAuthURL)
	}
}

func TestMagicLinkTokens(t *testing.T) {
	secret := strings.Repeat("s", 64)
	now := time.UnixMilli(1_700_000_000_000)

	if _, err := mintMagicLinkToken("a@b.co", "s", "too-short", 600, now); err == nil || !strings.Contains(err.Error(), "at least 32 characters") {
		t.Errorf("short secret: %v", err)
	}

	parts := strings.Split(nodeParityToken, ".")
	tamperedClaims := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"attacker@company.com","state":"csrf-state-123","expiresAt":1700000600}`))
	badPayload := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	missingFields := base64.RawURLEncoding.EncodeToString([]byte(`{"email":""}`))

	for name, tc := range map[string]struct {
		token  string
		state  string
		secret string
		at     time.Time
		want   string
	}{
		"tampered payload":   {tamperedClaims + "." + parts[1], "csrf-state-123", secret, now, "not valid"},
		"tampered signature": {parts[0] + ".AAAA", "csrf-state-123", secret, now, "not valid"},
		"wrong secret":       {nodeParityToken, "csrf-state-123", strings.Repeat("x", 64), now, "not valid"},
		"state mismatch":     {nodeParityToken, "other-state", secret, now, "browser that requested it"},
		"expired exact":      {nodeParityToken, "csrf-state-123", secret, now.Add(600 * time.Second), "expired"},
		"expired past":       {nodeParityToken, "csrf-state-123", secret, now.Add(601 * time.Second), "expired"},
		"empty":              {"", "csrf-state-123", secret, now, "not valid"},
		"no dot":             {"abc", "csrf-state-123", secret, now, "not valid"},
		"extra dot":          {"a.b.c", "csrf-state-123", secret, now, "not valid"},
		"non-json payload":   {badPayload + "." + macOf(secret, badPayload), "csrf-state-123", secret, now, "not valid"},
		"missing fields":     {missingFields + "." + macOf(secret, missingFields), "csrf-state-123", secret, now, "not valid"},
	} {
		if _, err := verifyMagicLinkToken(tc.token, tc.state, tc.secret, tc.at); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}

	if clampMagicLinkTTL(0, false) != 600 || clampMagicLinkTTL(math.NaN(), true) != 600 ||
		clampMagicLinkTTL(5, true) != 60 || clampMagicLinkTTL(86400, true) != 600 || clampMagicLinkTTL(120, true) != 120 {
		t.Error("clampMagicLinkTTL failed")
	}
}

func TestMagicLinkEmailProviders(t *testing.T) {
	if _, _, err := resolveEmailProvider(nil, "", "", ""); err == nil || !strings.Contains(err.Error(), "no email provider configured") {
		t.Errorf("none: %v", err)
	}
	if _, _, err := resolveEmailProvider(nil, "re_1", "pm_1", ""); err == nil || !strings.Contains(err.Error(), "multiple email providers configured (resend, postmark)") {
		t.Errorf("multiple: %v", err)
	}

	for in, want := range map[string]signInFromAddress{
		"signin@company.com":                     {Email: "signin@company.com"},
		"OpenNeko <signin@company.com>":          {Email: "signin@company.com", Name: "OpenNeko"},
		`"OpenNeko Sign-In" <signin@company.com>`: {Email: "signin@company.com", Name: "OpenNeko Sign-In"},
		"<signin@company.com>":                   {Email: "signin@company.com"},
	} {
		if got := parseSignInFrom(in); got != want {
			t.Errorf("parseSignInFrom(%q) = %+v, want %+v", in, got, want)
		}
	}

	escaped := buildSignInEmail("OpenNeko <signin@company.com>", `"<script>x</script>"@company.com`, "https://neko.example/cb?a=1&b=2", 10)
	if strings.Contains(escaped.HTML, "<script>") || !strings.Contains(escaped.HTML, "&lt;script&gt;") {
		t.Error("HTML escaping failed")
	}

	var gotURL, gotAuth, gotPostmark string
	var gotBody map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		gotPostmark = r.Header.Get("X-Postmark-Server-Token")
		raw, _ := io.ReadAll(r.Body)
		gotBody = nil
		_ = json.Unmarshal(raw, &gotBody)
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       io.NopCloser(strings.NewReader("{}")),
		}, nil
	})}

	_, sendPostmark, err := resolveEmailProvider(client, "", "pm_tok", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := sendPostmark(context.Background(), escaped); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://api.postmarkapp.com/email" || gotPostmark != "pm_tok" || gotBody["MessageStream"] != "outbound" {
		t.Errorf("postmark request: %q %q %v", gotURL, gotPostmark, gotBody)
	}

	_, sendSendgrid, err := resolveEmailProvider(client, "", "", "SG.key")
	if err != nil {
		t.Fatal(err)
	}
	if err := sendSendgrid(context.Background(), escaped); err != nil {
		t.Fatal(err)
	}
	fromObj, _ := gotBody["from"].(map[string]any)
	if gotURL != "https://api.sendgrid.com/v3/mail/send" || gotAuth != "Bearer SG.key" ||
		fromObj["email"] != "signin@company.com" || fromObj["name"] != "OpenNeko" {
		t.Errorf("sendgrid request: %q %q %v", gotURL, gotAuth, gotBody)
	}
}

func TestScalekitClientAndProvider(t *testing.T) {
	for _, bad := range []string{"", "http://foo.scalekit.com", "foo.scalekit.com"} {
		if _, err := normalizeScalekitEnvironmentURL(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
	env := map[string]string{
		"SCALEKIT_ENVIRONMENT_URL": "https://env.scalekit.com",
		"SCALEKIT_CLIENT_ID":       "cid",
		"SCALEKIT_CLIENT_SECRET":   "csecret",
	}
	if _, err := beginScalekitAuth("https://app.example/cb", "st", "", map[string]string{"SCALEKIT_CLIENT_ID": "cid"}); err == nil ||
		!strings.Contains(err.Error(), "SCALEKIT_ENVIRONMENT_URL, SCALEKIT_CLIENT_SECRET not set") {
		t.Errorf("missing env: %v", err)
	}

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/oauth/token":
			if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("cid:csecret")) {
				t.Errorf("bad basic auth: %q", r.Header.Get("Authorization"))
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"at-1"}`)),
			}, nil
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer at-1" {
				t.Errorf("bad bearer: %q", r.Header.Get("Authorization"))
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"sub":"usr_1","email":"alice@example.com","given_name":"Alice","family_name":"Smith","organization_id":"org_1","groups":["g1",{"id":"obj-group","name":"Ignored"},"","g2"],"roles":[{"role":"admin"},"g2","r1",42]}`)),
			}, nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})}

	id, err := completeScalekitAuth(context.Background(), client, "code-1", "https://app.example/cb", "st", env)
	if err != nil {
		t.Fatal(err)
	}
	if id.Sub != "usr_1" || id.Email != "alice@example.com" || id.Name == nil || *id.Name != "Alice Smith" || id.OrgID != "org_1" ||
		len(id.Groups) != 3 || id.Groups[0].ExternalID != "g1" || id.Groups[1].ExternalID != "g2" || id.Groups[2].ExternalID != "r1" {
		t.Errorf("identity = %+v", id)
	}

	errClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 400,
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","error_description":"Code expired"}`)),
		}, nil
	})}
	if _, err := completeScalekitAuth(context.Background(), errClient, "code-1", "https://app.example/cb", "st", env); err == nil ||
		!strings.Contains(err.Error(), "Scalekit token exchange returned HTTP 400 (invalid_grant: Code expired)") {
		t.Errorf("oauth error: %v", err)
	}

	htmlClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 502,
			Body:       io.NopCloser(strings.NewReader("<html>bad gateway")),
		}, nil
	})}
	if _, err := completeScalekitAuth(context.Background(), htmlClient, "code-1", "https://app.example/cb", "st", env); err == nil ||
		!strings.Contains(err.Error(), "Scalekit token exchange returned non-JSON (HTTP 502): <html>bad gateway") {
		t.Errorf("non-json error: %v", err)
	}
}

func TestMigrateLegacySignInSecrets(t *testing.T) {
	dir := t.TempDir()
	initial := map[string]any{
		"@open-neko/plugin-scalekit": map[string]string{
			"SCALEKIT_ENVIRONMENT_URL": "https://env.scalekit.com",
			"SCALEKIT_CLIENT_ID":       "cid",
			"SCALEKIT_CLIENT_SECRET":   "sec",
			"EXTRA_IGNORED":            "nope",
		},
		"@open-neko/plugin-magic-link": map[string]string{
			"MAGIC_LINK_SIGNING_SECRET": strings.Repeat("k", 64),
		},
		"_operators": map[string]any{
			"deployment": map[string]any{
				"@open-neko/plugin-scalekit": map[string]any{
					"tokens":      map[string]string{"access_token": "wk-at"},
					"connectedAt": "2026-04-19T00:00:00.000Z",
				},
			},
		},
	}
	raw, err := json.MarshalIndent(initial, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secrets.Path(dir), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	a := &API{SecretsDir: dir, Gate: newGate(t, "")}
	a.Gate.SecretsDir = dir
	moved, err := a.MigrateLegacySignInSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(moved, ",") != "@open-neko/plugin-scalekit,@open-neko/plugin-magic-link" {
		t.Fatalf("moved = %v", moved)
	}

	store, err := secrets.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store["signin"]["PROVIDER"] != "scalekit" ||
		store["signin.scalekit"]["SCALEKIT_CLIENT_ID"] != "cid" ||
		store["signin.scalekit"]["EXTRA_IGNORED"] != "" ||
		store["signin.magic-link"]["MAGIC_LINK_SIGNING_SECRET"] != strings.Repeat("k", 64) ||
		len(store["@open-neko/plugin-scalekit"]) != 0 ||
		len(store["@open-neko/plugin-magic-link"]) != 0 {
		t.Errorf("migrated store = %v", store)
	}

	afterRaw, err := os.ReadFile(secrets.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	var afterFull struct {
		Operators map[string]map[string]json.RawMessage `json:"_operators"`
	}
	if err := json.Unmarshal(afterRaw, &afterFull); err != nil {
		t.Fatal(err)
	}
	if _, ok := afterFull.Operators["deployment"]["scalekit"]; !ok {
		t.Errorf("scalekit operator credential not moved: %s", afterRaw)
	}
	if _, ok := afterFull.Operators["deployment"]["@open-neko/plugin-scalekit"]; ok {
		t.Errorf("legacy operator credential still present: %s", afterRaw)
	}

	again, err := a.MigrateLegacySignInSecrets()
	if err != nil || len(again) != 0 {
		t.Errorf("second migration = %v, %v", again, err)
	}

	dir2 := t.TempDir()
	if err := secrets.Write(secrets.Store{
		"@open-neko/plugin-magic-link": {
			"MAGIC_LINK_FROM":           "signin@acme.com",
			"MAGIC_LINK_SIGNING_SECRET": strings.Repeat("k", 64),
		},
	}, dir2); err != nil {
		t.Fatal(err)
	}
	a2 := &API{SecretsDir: dir2}
	if _, err := a2.MigrateLegacySignInSecrets(); err != nil {
		t.Fatal(err)
	}
	store2, err := secrets.Read(dir2)
	if err != nil || store2["signin"]["PROVIDER"] != "magic-link" || store2["signin.magic-link"]["MAGIC_LINK_SIGNING_SECRET"] != strings.Repeat("k", 64) {
		t.Errorf("magic-link migration = %v, %v", store2, err)
	}
}

func TestMagicLinkEndToEndAndSoloGate(t *testing.T) {
	db := testpg.Migrated(t)
	ctx := context.Background()
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	seed(t, db, `insert into app_user (id, org_id, email, name) values ('admin', 'org', 'owner@solo.openneko.invalid', 'Solo')`)
	seed(t, db, `update organization set solo_admin_user_id = 'admin' where id = 'org'`)
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source)
		select 'org', id, 'admin', 'local' from user_group where org_id = 'org' and slug = 'administrators'`)

	dir := t.TempDir()
	g := newGate(t, "")
	g.SecretsDir = dir
	g.PublicURL = "https://neko.example"
	g.DB = db
	var sentLink string
	signInClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &body)
		for _, line := range strings.Split(body.Text, string(byte(10))) {
			if strings.HasPrefix(line, "https://") {
				sentLink = strings.TrimSpace(line)
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"1"}`))}, nil
	})}
	a := &API{Gate: g, DB: db, SecretsDir: dir, PublicURL: "https://neko.example", SignInClient: signInClient}

	if err := a.selectSignInProvider("magic-link"); err != nil {
		t.Fatal(err)
	}
	from := "OpenNeko <signin@acme.com>"
	key := "re_live_123"
	if err := a.setSignInProviderValues("magic-link", map[string]*string{
		"MAGIC_LINK_FROM": &from,
		"RESEND_API_KEY":  &key,
	}); err != nil {
		t.Fatal(err)
	}

	status := g.Status(ctx)
	if status.Live() || !strings.Contains(string(status.Pending), `"needsAdminUser":true`) {
		t.Fatalf("unclaimed solo owner must keep sign-in pending: %+v", status)
	}

	seed(t, db, `update app_user set email = 'ada@acme.com' where id = 'admin'`)
	g.Reset()
	status = g.Status(ctx)
	if !status.Live() {
		t.Fatalf("claimed admin should make magic-link live: %s", status.Pending)
	}

	authURL, err := a.beginSignIn(ctx, "https://neko.example/api/auth/callback", "st-42", "Ada@Acme.com")
	if err != nil || authURL != "https://neko.example/signin?notice=link-sent" {
		t.Fatalf("beginSignIn = %q, %v", authURL, err)
	}
	parsedLink, err := url.Parse(sentLink)
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.completeSignInAuth(ctx, parsedLink.Query().Get("code"), "https://neko.example/api/auth/callback", "st-42")
	if err != nil || id.Sub != "magic-link:ada@acme.com" || id.Email != "ada@acme.com" {
		t.Fatalf("completeSignInAuth = %+v, %v", id, err)
	}
}

func TestSignInWithUnixWorkerEndpoint(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	workerBase, workerClient, err := workeradmin.Endpoint("unix://" + filepath.Join(t.TempDir(), "worker.sock"))
	if err != nil {
		t.Fatal(err)
	}

	var sentLink string
	providerSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/token":
			_, _ = io.WriteString(w, `{"access_token":"at-tcp"}`)
		case "/userinfo":
			_, _ = io.WriteString(w, `{"sub":"usr_tcp","email":"a@example.com","name":"Ada","groups":["Administrators",{"id":"ignored"}]}`)
		case "/emails":
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(raw, &body)
			for _, line := range strings.Split(body.Text, string(byte(10))) {
				if strings.HasPrefix(line, "https://") {
					sentLink = strings.TrimSpace(line)
				}
			}
			_, _ = io.WriteString(w, `{"id":"mail_tcp"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(providerSrv.Close)

	tcpBaseTransport := providerSrv.Client().Transport.(*http.Transport).Clone()
	tcpBaseTransport.TLSClientConfig.ServerName = "example.com"
	var dialer net.Dialer
	tcpBaseTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, providerSrv.Listener.Addr().String())
	}
	tcpClient := &http.Client{Transport: tcpBaseTransport, Timeout: 5 * time.Second}

	savedDefault := defaultSignInClient
	defaultSignInClient = tcpClient
	t.Cleanup(func() { defaultSignInClient = savedDefault })

	dir := t.TempDir()
	g := newGate(t, workerBase)
	g.SecretsDir = dir
	g.PublicURL = "https://neko.example"
	g.Client = workerClient
	g.DB = db
	a := &API{Gate: g, DB: db, Version: "5.0.0", PublicURL: "https://neko.example", SecureCookies: true, SecretsDir: dir}
	f := &Front{
		App:  fstest.MapFS{"index.html": {Data: []byte("app")}},
		Gate: g,
		API:  a,
		Node: &recorder{},
	}

	// 1. Scalekit begin and complete with SignInClient nil (uses defaultSignInClient on TCP, never Gate.Client on unix:).
	if err := secrets.Write(secrets.Store{
		"signin": {"PROVIDER": "scalekit"},
		"signin.scalekit": {
			"SCALEKIT_ENVIRONMENT_URL": providerSrv.URL,
			"SCALEKIT_CLIENT_ID":       "cid",
			"SCALEKIT_CLIENT_SECRET":   "csecret",
		},
	}, dir); err != nil {
		t.Fatal(err)
	}
	g.Reset()
	w := call(f, "GET", "/api/v1/auth/begin?loginHint=a@example.com&returnTo=/admin", "", nil)
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), providerSrv.URL+"/oauth/authorize?") {
		t.Fatalf("scalekit begin = %d %q %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	stateCookie := cookieFrom(w, StateCookie)
	locURL, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := locURL.Query().Get("state")
	w = call(f, "GET", "/api/v1/auth/callback?code=c-tcp&state="+url.QueryEscape(state), "", map[string]string{
		"Cookie": StateCookie + "=" + stateCookie.Value,
	})
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/admin" || cookieFrom(w, SessionCookie) == nil {
		t.Fatalf("scalekit callback = %d %q %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}

	// 2. Magic-link begin and complete with SignInClient set to the TCP client while Gate.Client is on unix:.
	a.SignInClient = tcpClient
	if err := secrets.Write(secrets.Store{
		"signin": {"PROVIDER": "magic-link"},
		"signin.magic-link": {
			"MAGIC_LINK_SIGNING_SECRET": strings.Repeat("m", 64),
			"MAGIC_LINK_FROM":           "OpenNeko <signin@acme.com>",
			"RESEND_API_KEY":            "re_tcp_1",
		},
	}, dir); err != nil {
		t.Fatal(err)
	}
	g.Reset()
	w = call(f, "GET", "/api/v1/auth/begin?loginHint=m@example.com&returnTo=/chat", "", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "https://neko.example/signin?notice=link-sent" || sentLink == "" {
		t.Fatalf("magic-link begin = %d %q sentLink=%q", w.Code, w.Header().Get("Location"), sentLink)
	}
	mlStateCookie := cookieFrom(w, StateCookie)
	parsedLink, err := url.Parse(sentLink)
	if err != nil {
		t.Fatal(err)
	}
	w = call(f, "GET", "/api/v1/auth/callback?"+parsedLink.RawQuery, "", map[string]string{
		"Cookie": StateCookie + "=" + mlStateCookie.Value,
	})
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/chat" || cookieFrom(w, SessionCookie) == nil {
		t.Fatalf("magic-link callback = %d %q %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
}

func TestMagicLinkRequiresPublicURL(t *testing.T) {
	t.Setenv("OPENNEKO_PUBLIC_URL", "")
	db := testpg.Migrated(t)
	seedOrg(t, db)
	dir := t.TempDir()
	g := newGate(t, "")
	g.SecretsDir = dir
	g.PublicURL = ""
	g.DB = db

	var sentLinks []string
	signInClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &body)
		for _, line := range strings.Split(body.Text, string(byte(10))) {
			if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
				sentLinks = append(sentLinks, strings.TrimSpace(line))
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"1"}`))}, nil
	})}
	a := &API{Gate: g, DB: db, Version: "5.0.0", PublicURL: "", SecureCookies: true, SecretsDir: dir, SignInClient: signInClient}
	f := &Front{
		App:  fstest.MapFS{"index.html": {Data: []byte("app")}},
		Gate: g,
		API:  a,
		Node: &recorder{},
	}

	if err := secrets.Write(secrets.Store{
		"signin": {"PROVIDER": "magic-link"},
		"signin.magic-link": {
			"MAGIC_LINK_SIGNING_SECRET": strings.Repeat("k", 64),
			"MAGIC_LINK_FROM":           "OpenNeko <signin@acme.com>",
			"RESEND_API_KEY":            "re_live_123",
		},
	}, dir); err != nil {
		t.Fatal(err)
	}
	admin := sessionCookie("admin")

	// 1. With no OPENNEKO_PUBLIC_URL and no stored public URL, status is pending and names OPENNEKO_PUBLIC_URL; begin sends no mail.
	g.Reset()
	authStatusBody := decode(t, call(f, "GET", "/api/v1/auth/status", "", nil))
	if authStatusBody["provider"] != nil || authStatusBody["pending"] == nil {
		t.Fatalf("auth/status without public URL = %v", authStatusBody)
	}
	adminStatus := decode(t, call(f, "GET", "/api/v1/admin/signin/status", "", admin))
	pendingObj, _ := adminStatus["pending"].(map[string]any)
	missingRaw, _ := json.Marshal(pendingObj["missingEnv"])
	if adminStatus["provider"] != nil || !strings.Contains(string(missingRaw), `"OPENNEKO_PUBLIC_URL"`) {
		t.Fatalf("admin/signin/status without public URL = %v", adminStatus)
	}

	evilReq := httptest.NewRequest("GET", "http://evil.example.com/api/v1/auth/begin?loginHint=a@example.com", nil)
	evilReq.Host = "evil.example.com"
	evilW := httptest.NewRecorder()
	f.ServeHTTP(evilW, evilReq)
	if evilW.Code != http.StatusServiceUnavailable || len(sentLinks) != 0 {
		t.Fatalf("auth/begin without public URL = %d %s, sent=%v", evilW.Code, evilW.Body.String(), sentLinks)
	}
	if _, err := a.beginSignIn(context.Background(), "http://evil.example.com/api/auth/callback", "st-1", "a@example.com"); err == nil || !strings.Contains(err.Error(), "OPENNEKO_PUBLIC_URL") || len(sentLinks) != 0 {
		t.Fatalf("beginSignIn without public URL = %v, sent=%v", err, sentLinks)
	}
	testW := call(f, "POST", "/api/v1/admin/signin/test", `{"email":"a@example.com"}`, admin)
	if testW.Code != http.StatusBadGateway || !strings.Contains(testW.Body.String(), "OPENNEKO_PUBLIC_URL") || len(sentLinks) != 0 {
		t.Fatalf("admin/signin/test without public URL = %d %s, sent=%v", testW.Code, testW.Body.String(), sentLinks)
	}

	// 2. Saving a public URL makes magic-link ready and ignores the request Host header.
	if w := call(f, "PUT", "/api/v1/admin/public-url", `{"url":"https://saved.example.com"}`, admin); w.Code != 200 {
		t.Fatalf("save public-url = %d %s", w.Code, w.Body.String())
	}
	adminStatus = decode(t, call(f, "GET", "/api/v1/admin/signin/status", "", admin))
	if adminStatus["provider"] == nil || adminStatus["pending"] != nil {
		t.Fatalf("admin/signin/status with saved public URL = %v", adminStatus)
	}
	evilReq = httptest.NewRequest("GET", "http://evil.example.com/api/v1/auth/begin?loginHint=a@example.com", nil)
	evilReq.Host = "evil.example.com"
	evilW = httptest.NewRecorder()
	f.ServeHTTP(evilW, evilReq)
	if evilW.Code != http.StatusFound || len(sentLinks) != 1 || !strings.HasPrefix(sentLinks[0], "https://saved.example.com/api/auth/callback?") || strings.Contains(sentLinks[0], "evil.example.com") {
		t.Fatalf("auth/begin with saved public URL = %d %q sent=%v", evilW.Code, evilW.Header().Get("Location"), sentLinks)
	}

	// 3. Clearing the stored public URL and setting OPENNEKO_PUBLIC_URL to localhost stays allowed for a local install.
	if w := call(f, "PUT", "/api/v1/admin/public-url", `{"url":""}`, admin); w.Code != 200 {
		t.Fatalf("clear public-url = %d %s", w.Code, w.Body.String())
	}
	t.Setenv("OPENNEKO_PUBLIC_URL", "http://localhost:3000")
	g.Reset()
	adminStatus = decode(t, call(f, "GET", "/api/v1/admin/signin/status", "", admin))
	if adminStatus["provider"] == nil || adminStatus["pending"] != nil {
		t.Fatalf("admin/signin/status with localhost OPENNEKO_PUBLIC_URL = %v", adminStatus)
	}
	sentLinks = nil
	evilReq = httptest.NewRequest("GET", "http://evil.example.com/api/v1/auth/begin?loginHint=a@example.com", nil)
	evilReq.Host = "evil.example.com"
	evilW = httptest.NewRecorder()
	f.ServeHTTP(evilW, evilReq)
	if evilW.Code != http.StatusFound || len(sentLinks) != 1 || !strings.HasPrefix(sentLinks[0], "http://localhost:3000/api/auth/callback?") || strings.Contains(sentLinks[0], "evil.example.com") {
		t.Fatalf("auth/begin with localhost OPENNEKO_PUBLIC_URL = %d %q sent=%v", evilW.Code, evilW.Header().Get("Location"), sentLinks)
	}
}

func TestGateSecretsDirSetAtConstruction(t *testing.T) {
	gateDir := t.TempDir()
	apiDir := t.TempDir()
	g := &Gate{SecretsDir: gateDir}
	a := &API{Gate: g, SecretsDir: apiDir}
	if got := a.secretsDir(); got != apiDir {
		t.Fatalf("a.secretsDir() = %q, want %q", got, apiDir)
	}
	if g.SecretsDir != gateDir {
		t.Fatalf("g.SecretsDir mutated to %q, want %q", g.SecretsDir, gateDir)
	}
	emptyGate := &Gate{}
	a2 := &API{Gate: emptyGate, SecretsDir: apiDir}
	_ = a2.secretsDir()
	if emptyGate.SecretsDir != "" {
		t.Fatalf("emptyGate.SecretsDir lazily mutated to %q", emptyGate.SecretsDir)
	}
}

