package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const magicLinkPending = `{"provider":null,"pending":{"pluginName":"magic-link","providerLabel":"Email link","configuredEnv":%s}}`

func signinFront(t *testing.T, status string) (*Front, *setupWorker, *pgxpool.Pool) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, wk := newSetupFront(t, status, db)
	return f, wk, db
}

func signinPending(configured string) string {
	return strings.Replace(magicLinkPending, "%s", configured, 1)
}

const liveScalekit = `{"provider":{"pluginName":"scalekit"},"pending":null}`

func TestAdminSigninAdminOnly(t *testing.T) {
	f, wk, _ := signinFront(t, liveScalekit)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/signin/status", ""},
		{"POST", "/api/v1/admin/signin/provider", `{"provider":null}`},
		{"POST", "/api/v1/admin/signin/email-provider", `{"provider":"resend","apiKey":"planted-key","from":"a@b.co"}`},
		{"POST", "/api/v1/admin/signin/test", `{"email":"a@b.co"}`},
		{"GET", "/api/v1/admin/public-url", ""},
		{"PUT", "/api/v1/admin/public-url", `{"url":"https://x.example"}`},
		{"GET", "/api/v1/admin/channels/settings", ""},
		{"POST", "/api/admin/channels/settings", `{"channel":"slack","values":{}}`},
	} {
		if w := call(f, c.method, c.path, c.body, sessionCookie("member")); w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"admin only"}` {
			t.Errorf("%s %s as a member: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
		if w := call(f, c.method, c.path, c.body, nil); w.Code == 200 {
			t.Errorf("%s %s as anonymous: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if len(wk.calls) != 0 {
		t.Errorf("worker calls: %v", wk.calls)
	}
}

func TestAdminSigninStatus(t *testing.T) {
	f, _, db := signinFront(t, `{"provider":{"pluginName":"magic-link"},"pending":null}`)
	got := decode(t, call(f, "GET", "/api/v1/admin/signin/status", "", sessionCookie("admin")))
	users, _ := got["users"].(map[string]any)
	if pr, _ := got["provider"].(map[string]any); pr["pluginName"] != "magic-link" || got["pending"] != nil || users["total"] != 2.0 ||
		users["activeAdmins"] != 1.0 || got["selfEmail"] != "a@example.com" || got["selfNeedsEmail"] != false {
		t.Fatalf("status %v", got)
	}
	seedSignInSecrets(t, f.Gate.SecretsDir, signinPending(`["RESEND_API_KEY"]`))
	f.API.Gate.Reset()
	seed(t, db, `update app_user set email = 'owner@solo.openneko.invalid' where id = 'admin'`)
	got = decode(t, call(f, "GET", "/api/v1/admin/signin/status", "", nil))
	p, _ := got["pending"].(map[string]any)
	email, _ := got["selfEmail"].(string)
	if got["provider"] != nil || p["pluginName"] != "magic-link" || !strings.HasSuffix(email, soloEmailDomain) || got["selfNeedsEmail"] != true {
		t.Errorf("status after sign-in turned off %v", got)
	}
}

func TestAdminSigninProvider(t *testing.T) {
	f, _, _ := signinFront(t, `{"provider":null}`)
	admin := sessionCookie("admin")
	for _, c := range []struct {
		body string
		code int
	}{
		{`{"provider":"magic-link"}`, 200},
		{`{"provider":null}`, 200},
		{`{"provider":"@open-neko/plugin-magic-link"}`, 400},
		{`{}`, 400},
		{`not json`, 400},
	} {
		if w := call(f, "POST", "/api/v1/admin/signin/provider", c.body, admin); w.Code != c.code {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body.String())
		}
	}
	store, err := secrets.Read(f.API.SecretsDir)
	if err != nil {
		t.Fatal(err)
	}
	if store["signin"]["PROVIDER"] != "" || len(store["signin.magic-link"]["MAGIC_LINK_SIGNING_SECRET"]) != 64 {
		t.Errorf("store after provider changes: %v", store)
	}

	corruptSignInSecrets(t, f.API.SecretsDir)
	if w := call(f, "POST", "/api/v1/admin/signin/provider", `{"provider":"scalekit"}`, admin); w.Code != 502 || !strings.Contains(w.Body.String(), "invalid JSON") {
		t.Errorf("secrets failure: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminSigninEmailProvider(t *testing.T) {
	f, _, _ := signinFront(t, signinPending(`["RESEND_API_KEY"]`))
	admin := sessionCookie("admin")
	post := func(body string) (int, string) {
		w := call(f, "POST", "/api/v1/admin/signin/email-provider", body, admin)
		return w.Code, w.Body.String()
	}
	for body, message := range map[string]string{
		`{"provider":"sendgrid","from":"a <b@c.co>"}`:                                  "paste the sendgrid API key — none is stored yet",
		`{"provider":"smtp","apiKey":"k","from":"a <b@c.co>"}`:                         "provider must be resend, postmark, or sendgrid",
		`{"provider":"resend","apiKey":"k","from":""}`:                                 "from address is required",
		`{"provider":"resend","apiKey":"k","from":"` + strings.Repeat("a", 321) + `"}`: "from address is required",
		`{"provider":"resend","apiKey":null,"from":"a <b@c.co>"}`:                      "apiKey must be a string",
		`{"provider":"resend","apiKey":7,"from":"a <b@c.co>"}`:                         "apiKey must be a string",
		`nope`: "invalid JSON body",
	} {
		if code, got := post(body); code != 400 || got != `{"error":"`+message+`"}` {
			t.Errorf("%s: %d %s", body, code, got)
		}
	}
	if code, _ := post(`{"provider":"resend","from":"a <b@c.co>"}`); code != 200 {
		t.Errorf("stored key kept: %d", code)
	}
	store, err := secrets.Read(f.API.SecretsDir)
	if err != nil || store["signin.magic-link"]["MAGIC_LINK_FROM"] != "a <b@c.co>" || store["signin.magic-link"]["RESEND_API_KEY"] != "test-value" {
		t.Fatalf("after resend keep: %v %v", store, err)
	}
	if code, body := post(`{"provider":"postmark","apiKey":" pm_key ","from":" OpenNeko <signin@c.co> "}`); code != 200 || body != `{"ok":true}` {
		t.Fatalf("write: %d %s", code, body)
	}
	store, err = secrets.Read(f.API.SecretsDir)
	if err != nil || store["signin.magic-link"]["MAGIC_LINK_FROM"] != "OpenNeko <signin@c.co>" ||
		store["signin.magic-link"]["POSTMARK_SERVER_TOKEN"] != "pm_key" ||
		store["signin.magic-link"]["RESEND_API_KEY"] != "" ||
		store["signin.magic-link"]["SENDGRID_API_KEY"] != "" {
		t.Fatalf("after postmark write: %v %v", store, err)
	}

	if err := os.Chmod(f.API.SecretsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.API.SecretsDir, 0o700) })
	if err := os.Chmod(filepath.Join(f.API.SecretsDir, secrets.StoreFilename), 0o400); err != nil {
		t.Fatal(err)
	}
	if code, _ := post(`{"provider":"resend","apiKey":"k","from":"a <b@c.co>"}`); code != 502 {
		t.Errorf("read-only failure: %d", code)
	}
	_ = os.Chmod(f.API.SecretsDir, 0o700)
	_ = os.Chmod(filepath.Join(f.API.SecretsDir, secrets.StoreFilename), 0o600)

	seedSignInSecrets(t, f.Gate.SecretsDir, liveScalekit)
	f.API.Gate.Reset()
	if code, body := post(`{"provider":"resend","apiKey":"k","from":"a <b@c.co>"}`); code != 409 || body != `{"error":"email-link sign-in is not the chosen sign-in provider"}` {
		t.Errorf("other provider: %d %s", code, body)
	}
}

func TestAdminSigninEmailProviderLive(t *testing.T) {
	f, _, _ := signinFront(t, `{"provider":{"pluginName":"magic-link"},"pending":null}`)
	if w := call(f, "POST", "/api/v1/admin/signin/email-provider", `{"provider":"resend","from":"a <b@c.co>"}`, sessionCookie("admin")); w.Code != 200 {
		t.Errorf("live provider keeps its key: %d %s", w.Code, w.Body.String())
	}
	store, err := secrets.Read(f.API.SecretsDir)
	if err != nil || store["signin.magic-link"]["RESEND_API_KEY"] != "re_test" || store["signin.magic-link"]["MAGIC_LINK_FROM"] != "a <b@c.co>" {
		t.Errorf("store %v %v", store, err)
	}
}

type testEmailTransport struct {
	mu    sync.Mutex
	calls []map[string]any
	fail  bool
}

func (tr *testEmailTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	tr.calls = append(tr.calls, body)
	if tr.fail {
		return &http.Response{
			StatusCode: http.StatusUnprocessableEntity,
			Header:     http.Header{"Content-Type": {"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("sender not verified")),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"em_1"}`)),
	}, nil
}

func TestAdminSigninTest(t *testing.T) {
	f, _, _ := signinFront(t, `{"provider":{"pluginName":"magic-link"},"pending":null}`)
	tr := &testEmailTransport{}
	f.API.SignInClient = &http.Client{Transport: tr}
	admin := sessionCookie("admin")
	if w := call(f, "POST", "/api/v1/admin/signin/test", `{"email":" Admin@Company.com "}`, admin); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Fatalf("test: %d %s", w.Code, w.Body.String())
	}
	if len(tr.calls) != 1 {
		t.Fatalf("email calls: %d", len(tr.calls))
	}
	to, _ := tr.calls[0]["to"].([]any)
	text, _ := tr.calls[0]["text"].(string)
	if len(to) != 1 || to[0] != "admin@company.com" ||
		!strings.Contains(text, "https://neko.example/api/auth/callback?code=") ||
		!strings.Contains(text, "&state=delivery-test-") {
		t.Errorf("email payload %v", tr.calls[0])
	}
	for _, body := range []string{`{"email":"nope"}`, `{"email":"a b@c.co"}`, `{"email":"a@b"}`, `{}`, `nope`} {
		if w := call(f, "POST", "/api/v1/admin/signin/test", body, admin); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if len(tr.calls) != 1 {
		t.Errorf("a refused address reached the email sender: %v", tr.calls)
	}
	tr.fail = true
	w := call(f, "POST", "/api/v1/admin/signin/test", `{"email":"a@b.co"}`, admin)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "Resend rejected the sign-in email (422): sender not verified") {
		t.Errorf("provider failure: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminPublicURL(t *testing.T) {
	f, _, db := signinFront(t, signinPending("[]"))
	admin := sessionCookie("admin")
	if w := call(f, "GET", "/api/v1/admin/public-url", "", admin); w.Code != 200 || w.Body.String() != `{"saved":null,"environment":"https://neko.example"}` {
		t.Errorf("get: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "PUT", "/api/v1/admin/public-url", `{"url":" HTTPS://Proxy.Example.com:443/openneko/ "}`, admin); w.Code != 200 || w.Body.String() != `{"saved":"https://proxy.example.com/openneko","environment":"https://neko.example"}` {
		t.Errorf("save: %d %s", w.Code, w.Body.String())
	}
	var provider string
	if err := db.QueryRow(context.Background(), `select provider from llm_provider_config where org_id = 'org' and scope = 'public-url'`).Scan(&provider); err != nil || provider != "public-url" {
		t.Errorf("row %q: %v", provider, err)
	}
	if w := call(f, "PUT", "/api/v1/admin/public-url", `{"url":"ftp://x.example"}`, admin); w.Code != 400 || !strings.Contains(w.Body.String(), "Enter an http or https address") {
		t.Errorf("bad: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"url":""}`, `null`, `nope`} {
		call(f, "PUT", "/api/v1/admin/public-url", `{"url":"https://x.example"}`, admin)
		if w := call(f, "PUT", "/api/v1/admin/public-url", body, admin); w.Code != 200 || !strings.Contains(w.Body.String(), `"saved":null`) {
			t.Errorf("clear with %s: %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestNormalizePublicURL(t *testing.T) {
	for in, want := range map[string]string{
		" https://openneko.example.com/ ":        "https://openneko.example.com",
		"http://10.0.0.5:4000":                   "http://10.0.0.5:4000",
		"http://10.0.0.5:80/":                    "http://10.0.0.5",
		"https://proxy.example.com/openneko/":    "https://proxy.example.com/openneko",
		"https://proxy.example.com/a/../b//":     "https://proxy.example.com/b",
		"https://[::1]:8443":                     "https://[::1]:8443",
		"ftp://openneko.example.com":             "",
		"https://openneko.example.com/?a=1":      "",
		"https://openneko.example.com/#top":      "",
		"https://user:pass@openneko.example.com": "",
		"openneko.example.com":                   "",
		"https://":                               "",
	} {
		got, ok := normalizePublicURL(in)
		if (want == "") == ok || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
}
