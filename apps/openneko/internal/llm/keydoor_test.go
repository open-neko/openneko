package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
)

type seen struct {
	method, path, query string
	header              http.Header
}

func upstream(t *testing.T, status int, body string) (*httptest.Server, chan seen) {
	t.Helper()
	got := make(chan seen, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func doorClient(t *testing.T, door *KeyDoor) *http.Client {
	t.Helper()
	path := filepath.Join(testsock.Dir(t), keydoor.SocketName)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := ListenDoor(ctx, path, door); err != nil {
		t.Fatal(err)
	}
	keydoor.SetSocket(path)
	t.Cleanup(func() { keydoor.SetSocket("") })
	client, err := keydoor.Client()
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func post(t *testing.T, client *http.Client, url, header, value string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	req.Header.Set(header, value)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func issue(t *testing.T, door *KeyDoor, org string, routes map[string]DoorRoute) (string, func()) {
	t.Helper()
	pass, err := door.Issue(org, "test", routes, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	return pass.Token, pass.Revoke
}

func TestKeyDoorPutsEachProvidersKeyInItsHeader(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	for _, c := range []struct {
		provider, header, want, path string
	}{
		{"openai-compatible", "Authorization", "Bearer real-key-1", "/chat/completions"},
		{"anthropic", "X-Api-Key", "real-key-1", "/v1/messages"},
		{"google-gemini", "X-Goog-Api-Key", "real-key-1", "/models/m:streamGenerateContent?alt=sse"},
		{"azure-openai", "Api-Key", "real-key-1", "/chat/completions?api-version=old"},
	} {
		srv, got := upstream(t, 200, `{"ok":true}`)
		route, err := DoorRouteFor(AxModelRoute{Provider: c.provider, Model: "m", URL: srv.URL + "/v1"}, "real-key-1")
		if c.provider == "azure-openai" {
			route, err = DoorRouteFor(AxModelRoute{Provider: c.provider, Model: "dep", Options: map[string]string{"resource_name": "res1", "deployment_name": "dep"}}, "real-key-1")
			route.Upstream = srv.URL + "/openai/deployments/dep"
		}
		if err != nil {
			t.Fatal(err)
		}
		token, revoke := issue(t, door, "org-1", map[string]DoorRoute{"primary": route})
		status, _ := post(t, client, keydoor.URL("primary")+c.path, c.header, map[bool]string{true: "Bearer ", false: ""}[c.header == "Authorization"]+token)
		if status != 200 {
			t.Fatalf("%s: status %d", c.provider, status)
		}
		s := <-got
		if s.header.Get(c.header) != c.want {
			t.Errorf("%s: %s = %q, want %q", c.provider, c.header, s.header.Get(c.header), c.want)
		}
		for _, h := range doorKeyHeaders {
			if strings.Contains(s.header.Get(h), keydoor.TokenPrefix) {
				t.Errorf("%s: the token reached the upstream in %s", c.provider, h)
			}
		}
		if c.provider == "azure-openai" && (s.path != "/openai/deployments/dep/chat/completions" || s.query != "api-version="+AxAzureAPIVersion) {
			t.Errorf("azure upstream %s?%s", s.path, s.query)
		}
		revoke()
		if status, _ := post(t, client, keydoor.URL("primary")+c.path, c.header, token); status != http.StatusUnauthorized {
			t.Errorf("%s: a revoked token got status %d", c.provider, status)
		}
	}
}

func TestKeyDoorRefusesOtherRoutesAndRedactsErrors(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	srv, _ := upstream(t, 401, `{"error":"bad key real-key-2"}`)
	token, revoke := issue(t, door, "org-1", map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "real-key-2"}})
	defer revoke()
	if status, _ := post(t, client, keydoor.URL("fallback")+"/chat/completions", "Authorization", "Bearer "+token); status != http.StatusForbidden {
		t.Errorf("another route: status %d", status)
	}
	open, reached := upstream(t, 200, `{}`)
	dots, revokeDots := issue(t, door, "org-1", map[string]DoorRoute{"primary": {Upstream: open.URL + "/v1", Key: "k"}})
	defer revokeDots()
	for _, path := range []string{"/../x", "/../chat/completions", "/chat/../chat/completions", "/./chat/completions", "/%2e%2e/chat/completions", "/chat%2Fcompletions"} {
		if status, _ := post(t, client, keydoor.URL("primary")+path, "Authorization", "Bearer "+dots); status == 200 {
			t.Errorf("the path %s passed", path)
		}
		noCall(t, reached, path)
	}
	if status, _ := post(t, client, keydoor.URL("primary")+"/chat/completions", "Authorization", "Bearer "+token+"x"); status != http.StatusUnauthorized {
		t.Errorf("a changed token: status %d", status)
	}
	status, body := post(t, client, keydoor.URL("primary")+"/chat/completions", "Authorization", "Bearer "+token)
	if status != 401 || strings.Contains(body, "real-key-2") || !strings.Contains(body, "[redacted]") {
		t.Errorf("error answer %d %q", status, body)
	}
}

func TestKeyDoorTokenEndsAtItsDeadline(t *testing.T) {
	now := time.Now()
	door := &KeyDoor{Now: func() time.Time { return now }}
	token, _ := issue(t, door, "org-1", map[string]DoorRoute{"primary": {Upstream: "https://example.test"}})
	if door.grant(token) == nil {
		t.Fatal("a fresh token was refused")
	}
	now = now.Add(time.Minute)
	if door.grant(token) != nil {
		t.Fatal("a token passed after its deadline")
	}
}

func TestDoorRouteForCallsTheURLThatAxBuilds(t *testing.T) {
	for _, c := range []struct {
		route AxModelRoute
		want  string
	}{
		{AxModelRoute{Provider: "azure-openai", Model: "dep1", Options: map[string]string{"resource_name": "res1", "deployment_name": "dep1", "api_version": "2024-10-21"}},
			"https://res1.openai.azure.com/openai/deployments/dep1?api-version=2024-10-21"},
		{AxModelRoute{Provider: "openai", Model: "m"}, "https://api.openai.com/v1"},
		{AxModelRoute{Provider: "anthropic", Model: "m"}, "https://api.anthropic.com"},
		{AxModelRoute{Provider: "google-gemini", Model: "m"}, "https://generativelanguage.googleapis.com/v1beta"},
		{AxModelRoute{Provider: "mistral", Model: "m", URL: "https://api.mistral.ai/v1"}, "https://api.mistral.ai/v1"},
	} {
		r, err := DoorRouteFor(c.route, "k")
		if err != nil {
			t.Fatal(err)
		}
		got := r.Upstream
		if len(r.Query) > 0 {
			got += "?" + r.Query.Encode()
		}
		if got != c.want {
			t.Errorf("%s: %s, want %s", c.route.Provider, got, c.want)
		}
	}
}

func TestKeyDoorTokenReachesOnlyItsOwnOrgsRoute(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	srvA, gotA := upstream(t, 200, `{}`)
	srvB, gotB := upstream(t, 200, `{}`)
	tokenA, revokeA := issue(t, door, "org-a", map[string]DoorRoute{"primary": {Upstream: srvA.URL, Key: "key-of-a"}})
	defer revokeA()
	tokenB, revokeB := issue(t, door, "org-b", map[string]DoorRoute{"primary": {Upstream: srvB.URL, Key: "key-of-b"}, "embed": {Upstream: srvB.URL, Key: "key-of-b"}})
	defer revokeB()
	if status, _ := post(t, client, keydoor.URL("primary")+"/chat/completions", "Authorization", "Bearer "+tokenA); status != 200 {
		t.Fatalf("org-a's own route: status %d", status)
	}
	if s := <-gotA; s.header.Get("Authorization") != "Bearer key-of-a" {
		t.Fatalf("org-a's upstream got %q", s.header.Get("Authorization"))
	}
	if status, _ := post(t, client, keydoor.URL("embed")+"/chat/completions", "Authorization", "Bearer "+tokenA); status != http.StatusForbidden {
		t.Errorf("org-a's token on a route only org-b holds: status %d", status)
	}
	idA, _, _ := strings.Cut(strings.TrimPrefix(tokenA, keydoor.TokenPrefix), ".")
	_, secretB, _ := strings.Cut(strings.TrimPrefix(tokenB, keydoor.TokenPrefix), ".")
	if status, _ := post(t, client, keydoor.URL("primary")+"/chat/completions", "Authorization", "Bearer "+keydoor.TokenPrefix+idA+"."+secretB); status != http.StatusUnauthorized {
		t.Errorf("org-a's id with org-b's secret: status %d", status)
	}
	select {
	case s := <-gotB:
		t.Fatalf("org-b's upstream got a call with %q from org-a's token", s.header.Get("Authorization"))
	default:
	}
}

func TestKeyDoorForwardsToTheRoutesUpstreamOnly(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	srv, got := upstream(t, 200, `{}`)
	other, otherGot := upstream(t, 200, `{}`)
	otherHost := strings.TrimPrefix(other.URL, "http://")
	token, revoke := issue(t, door, "org-1", map[string]DoorRoute{"primary": {Upstream: srv.URL + "/v1", Key: "real-key-3"}})
	defer revoke()
	for _, path := range []string{
		"/chat/completions",
		"//" + otherHost + "/chat/completions",
		"/http://" + otherHost + "/chat/completions",
		"/@" + otherHost + "/chat/completions",
	} {
		req, _ := http.NewRequest(http.MethodPost, keydoor.URL("primary")+path, strings.NewReader(`{}`))
		req.Host = otherHost
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Forwarded-Host", otherHost)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode == 200 {
			select {
			case s := <-got:
				if !strings.HasPrefix(s.path, "/v1/") {
					t.Errorf("%s reached the upstream at %s", path, s.path)
				}
			case <-time.After(5 * time.Second):
				t.Errorf("%s: status 200, but the route's upstream got no call", path)
			}
		}
	}
	if _, err := door.Issue("org-1", "test", map[string]DoorRoute{"primary": {Upstream: "http://user:pw@" + otherHost}}, time.Minute, 1); err == nil {
		t.Error("an upstream with user info was accepted")
	}
	select {
	case s := <-otherGot:
		t.Fatalf("a host that is not the route's upstream got %s %s", s.method, s.path)
	default:
	}
}

func TestKeyDoorPassesNoRedirectToAnotherHost(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	other, otherGot := upstream(t, 200, `{}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	token, revoke := issue(t, door, "org-1", map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "real-key-4"}})
	defer revoke()
	req, _ := http.NewRequest(http.MethodPost, keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	if res, err := client.Do(req); err == nil {
		_ = res.Body.Close()
	}
	select {
	case s := <-otherGot:
		t.Fatalf("a redirect reached another host with %q", s.header.Get("Authorization"))
	default:
	}
}
