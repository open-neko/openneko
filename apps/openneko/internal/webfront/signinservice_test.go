package webfront

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
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
				Body:       io.NopCloser(strings.NewReader(`{"sub":"usr_1","email":"alice@example.com","given_name":"Alice","family_name":"Smith","organization_id":"org_1","groups":["g1","g2"],"roles":["g2","r1"]}`)),
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
	if id.Sub != "usr_1" || id.Email != "alice@example.com" || id.Name == nil || *id.Name != "Alice Smith" || id.OrgID != "org_1" || len(id.Groups) != 3 {
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
	seed(t, db, `insert into organization (id, name, solo_admin_user_id) values ('org', 'Org', 'admin')`)
	seed(t, db, `insert into app_user (id, org_id, email, name) values ('admin', 'org', 'owner@solo.openneko.invalid', 'Solo')`)
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source)
		select 'org', id, 'admin', 'local' from user_group where org_id = 'org' and slug = 'administrators'`)

	dir := t.TempDir()
	g := newGate(t, "")
	g.SecretsDir = dir
	g.DB = db
	var sentLink string
	g.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
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
	a := &API{Gate: g, DB: db, SecretsDir: dir, PublicURL: "https://neko.example"}

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
