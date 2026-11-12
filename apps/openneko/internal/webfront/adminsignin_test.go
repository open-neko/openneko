package webfront

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const magicLinkPending = `{"provider":null,"pending":{"pluginName":"magic-link","providerLabel":"Email link","configuredEnv":%s}}`

func signinFront(t *testing.T, status string) (*Front, *setupWorker, *pgxpool.Pool) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, wk := newSetupFront(t, status, db)
	wk.reply["/admin/auth/provider"] = `{"ok":true}`
	wk.reply["/admin/auth/secrets"] = `{"ok":true}`
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
	f, wk, db := signinFront(t, `{"provider":{"pluginName":"magic-link"},"pending":null}`)
	got := decode(t, call(f, "GET", "/api/v1/admin/signin/status", "", sessionCookie("admin")))
	users, _ := got["users"].(map[string]any)
	if pr, _ := got["provider"].(map[string]any); pr["pluginName"] != "magic-link" || got["pending"] != nil || users["total"] != 2.0 ||
		users["activeAdmins"] != 1.0 || got["selfEmail"] != "a@example.com" || got["selfNeedsEmail"] != false {
		t.Fatalf("status %v", got)
	}
	wk.mu.Lock()
	wk.status = signinPending(`["RESEND_API_KEY"]`)
	wk.mu.Unlock()
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
	f, wk, _ := signinFront(t, signinPending("[]"))
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
	want := []string{`POST /admin/auth/provider {"provider":"magic-link"}`, `POST /admin/auth/provider {"provider":null}`}
	if strings.Join(wk.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls %v", wk.calls)
	}
	wk.fail = `{"error":"no such provider"}`
	if w := call(f, "POST", "/api/v1/admin/signin/provider", `{"provider":"scalekit"}`, admin); w.Code != 502 || w.Body.String() != `{"error":"no such provider"}` {
		t.Errorf("worker failure: %d %s", w.Code, w.Body.String())
	}
	wk.fail = "plain text"
	if w := call(f, "POST", "/api/v1/admin/signin/provider", `{"provider":"scalekit"}`, admin); w.Code != 502 || w.Body.String() != `{"error":"sign-in provider change failed (502)"}` {
		t.Errorf("worker failure without JSON: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminSigninEmailProvider(t *testing.T) {
	f, wk, _ := signinFront(t, signinPending(`["RESEND_API_KEY"]`))
	admin := sessionCookie("admin")
	post := func(body string) (int, string) {
		w := call(f, "POST", "/api/v1/admin/signin/email-provider", body, admin)
		return w.Code, w.Body.String()
	}
	if code, body := post(`{"provider":"postmark","apiKey":" pm_key ","from":" OpenNeko <signin@c.co> "}`); code != 200 || body != `{"ok":true}` {
		t.Fatalf("write: %d %s", code, body)
	}
	if code, _ := post(`{"provider":"resend","from":"a <b@c.co>"}`); code != 200 {
		t.Errorf("stored key kept: %d", code)
	}
	want := []string{
		`POST /admin/auth/secrets {"values":{"MAGIC_LINK_FROM":"OpenNeko \u003csignin@c.co\u003e","POSTMARK_SERVER_TOKEN":"pm_key","RESEND_API_KEY":null,"SENDGRID_API_KEY":null}}`,
		`POST /admin/auth/secrets {"values":{"MAGIC_LINK_FROM":"a \u003cb@c.co\u003e","POSTMARK_SERVER_TOKEN":null,"SENDGRID_API_KEY":null}}`,
	}
	if strings.Join(wk.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls %v", wk.calls)
	}
	wk.calls = nil
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
	if len(wk.calls) != 0 {
		t.Errorf("a refused body reached the worker: %v", wk.calls)
	}
	wk.fail = `{"error":"secrets store is read-only"}`
	if code, body := post(`{"provider":"resend","apiKey":"k","from":"a <b@c.co>"}`); code != 502 || body != `{"error":"secrets store is read-only"}` {
		t.Errorf("worker failure: %d %s", code, body)
	}

	wk.mu.Lock()
	wk.status = liveScalekit
	wk.mu.Unlock()
	f.API.Gate.Reset()
	if code, body := post(`{"provider":"resend","apiKey":"k","from":"a <b@c.co>"}`); code != 409 || body != `{"error":"email-link sign-in is not the chosen sign-in provider"}` {
		t.Errorf("other provider: %d %s", code, body)
	}
}

func TestAdminSigninEmailProviderLive(t *testing.T) {
	f, wk, _ := signinFront(t, `{"provider":{"pluginName":"magic-link"},"pending":null}`)
	if w := call(f, "POST", "/api/v1/admin/signin/email-provider", `{"provider":"resend","from":"a <b@c.co>"}`, sessionCookie("admin")); w.Code != 200 {
		t.Errorf("live provider keeps its key: %d %s", w.Code, w.Body.String())
	}
	if len(wk.calls) != 1 || strings.Contains(wk.calls[0], "RESEND_API_KEY") {
		t.Errorf("calls %v", wk.calls)
	}
}

func TestAdminSigninTest(t *testing.T) {
	f, wk, _ := signinFront(t, signinPending("[]"))
	admin := sessionCookie("admin")
	wk.reply["/admin/auth/begin"] = `{"authorizationUrl":"https://x"}`
	if w := call(f, "POST", "/api/v1/admin/signin/test", `{"email":" Admin@Company.com "}`, admin); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Fatalf("test: %d %s", w.Code, w.Body.String())
	}
	if len(wk.calls) != 1 || !strings.Contains(wk.calls[0], `"loginHint":"admin@company.com"`) ||
		!strings.Contains(wk.calls[0], `"redirectUri":"https://neko.example/api/auth/callback"`) || !strings.Contains(wk.calls[0], `"state":"delivery-test-`) {
		t.Errorf("calls %v", wk.calls)
	}
	for _, body := range []string{`{"email":"nope"}`, `{"email":"a b@c.co"}`, `{"email":"a@b"}`, `{}`, `nope`} {
		if w := call(f, "POST", "/api/v1/admin/signin/test", body, admin); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if len(wk.calls) != 1 {
		t.Errorf("a refused address reached the worker: %v", wk.calls)
	}
	wk.fail = "Resend rejected the sign-in email (422): sender not verified"
	w := call(f, "POST", "/api/v1/admin/signin/test", `{"email":"a@b.co"}`, admin)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "sender not verified") {
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

func TestAdminChannelSettings(t *testing.T) {
	f, wk, _ := signinFront(t, signinPending("[]"))
	admin := sessionCookie("admin")
	wk.reply["/admin/channels/settings"] = `{ "channels": [] }`
	if w := call(f, "GET", "/api/v1/admin/channels/settings", "", admin); w.Code != 200 || w.Body.String() != `{"channels":[]}` {
		t.Errorf("get: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/admin/channels/settings", `{"channel":"slack","values":{"SLACK_BOT_TOKEN":"planted"}}`, admin); w.Code != 200 {
		t.Errorf("post: %d %s", w.Code, w.Body.String())
	}
	if len(wk.calls) != 2 || wk.calls[0] != "GET /admin/channels/settings" || wk.calls[1] != `POST /admin/channels/settings {"channel":"slack","values":{"SLACK_BOT_TOKEN":"planted"}}` {
		t.Errorf("calls %v", wk.calls)
	}
	wk.fail = `{"error":"unknown channel"}`
	if w := call(f, "POST", "/api/v1/admin/channels/settings", `{"channel":"x"}`, admin); w.Code != 502 || w.Body.String() != `{"error":"unknown channel"}` {
		t.Errorf("worker error: %d %s", w.Code, w.Body.String())
	}
	wk.fail = "boom"
	if w := call(f, "GET", "/api/v1/admin/channels/settings", "", admin); w.Code != 502 || w.Body.String() != `{"error":"Worker returned HTTP 502"}` {
		t.Errorf("worker text: %d %s", w.Code, w.Body.String())
	}
	f.API.Gate.WorkerURL = "http://127.0.0.1:1"
	if w := call(f, "GET", "/api/v1/admin/channels/settings", "", admin); w.Code != 502 || w.Body.String() != `{"error":"the worker did not answer"}` {
		t.Errorf("worker down: %d %s", w.Code, w.Body.String())
	}
}
