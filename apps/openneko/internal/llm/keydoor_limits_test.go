package llm

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
)

func issuePass(t *testing.T, door *KeyDoor, routes map[string]DoorRoute, calls int) *DoorPass {
	t.Helper()
	pass, err := door.Issue("org-1", "test", routes, time.Minute, calls)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pass.Revoke)
	return pass
}

func noCall(t *testing.T, got chan seen, what string) {
	t.Helper()
	select {
	case s := <-got:
		t.Errorf("%s reached the upstream: %s %s?%s", what, s.method, s.path, s.query)
	default:
	}
}

func TestKeyDoorForwardsOnlyModelCallPaths(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	for _, c := range []struct {
		api, upstreamPath string
		allowed, refused  []string
	}{
		{DoorChat, "/v1", []string{"/chat/completions"},
			[]string{"/files", "/batches", "/fine_tuning/jobs", "/images/generations", "/responses", "/embeddings", "/chat/completions/x", "", "/"}},
		{DoorAnthropic, "", []string{"/v1/messages"},
			[]string{"/v1/messages/batches", "/v1/files", "/v1/complete", "/chat/completions"}},
		{DoorGemini, "/v1beta", []string{"/models/gemini-2.5-flash:generateContent", "/models/gemini-2.5-flash:streamGenerateContent?alt=sse"},
			[]string{"/models/gemini-2.5-flash:countTokens", "/models/gemini-2.5-flash:batchEmbedContents", "/files", "/models/a/b:generateContent", "/models/:generateContent", "/cachedContents"}},
		{DoorEmbed, "/v1", []string{"/embeddings"},
			[]string{"/chat/completions", "/files", "/embeddings/x"}},
		{DoorDecisions, "/v1beta", []string{"/decisions"},
			[]string{"/models/gemini-decision-1:generateContent", "/chat/completions", "/decisions/x", "/files", "/cachedContents", ""}},
		{DoorTypesafe, "", []string{"/v1/systemone"},
			[]string{"/v1/systemone/x", "/v1/files", "/chat/completions", "/decisions"}},
	} {
		srv, got := upstream(t, 200, `{}`)
		pass := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL + c.upstreamPath, Key: "k", API: c.api}}, 100)
		for _, path := range c.refused {
			if status, _ := post(t, client, keydoor.URL("primary")+path, "Authorization", "Bearer "+pass.Token); status != http.StatusForbidden {
				t.Errorf("%s %q: status %d, want 403", c.api, path, status)
			}
			noCall(t, got, c.api+" "+path)
		}
		for _, path := range c.allowed {
			if status, _ := post(t, client, keydoor.URL("primary")+path, "Authorization", "Bearer "+pass.Token); status != 200 {
				t.Errorf("%s %q: status %d, want 200", c.api, path, status)
				continue
			}
			want, _, _ := strings.Cut(c.upstreamPath+path, "?")
			if s := <-got; s.path != want {
				t.Errorf("%s %q reached %s, want %s", c.api, path, s.path, want)
			}
		}
		if pass.Calls() != len(c.allowed) {
			t.Errorf("%s: the door counted %d calls, want %d", c.api, pass.Calls(), len(c.allowed))
		}
	}
}

func TestKeyDoorRefusesAnExtraQueryParameter(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	srv, got := upstream(t, 200, `{}`)
	azure, err := DoorRouteFor(AxModelRoute{Provider: "azure-openai", Model: "dep", Options: map[string]string{"resource_name": "res1", "deployment_name": "dep"}}, "k")
	if err != nil {
		t.Fatal(err)
	}
	azure.Upstream = srv.URL + "/openai/deployments/dep"
	pass := issuePass(t, door, map[string]DoorRoute{
		"primary":  {Upstream: srv.URL + "/v1", Key: "k"},
		"fallback": {Upstream: srv.URL + "/v1beta", Key: "k", API: DoorGemini},
		"embed":    azure,
	}, 100)
	for _, path := range []string{
		keydoor.URL("primary") + "/chat/completions?api-version=1",
		keydoor.URL("primary") + "/chat/completions?stream=true",
		keydoor.URL("fallback") + "/models/m:generateContent?key=AIza",
		keydoor.URL("fallback") + "/models/m:streamGenerateContent?alt=json",
		keydoor.URL("fallback") + "/models/m:streamGenerateContent?alt=sse&alt=sse",
		keydoor.URL("embed") + "/chat/completions?api-version=1&user=x",
	} {
		if status, _ := post(t, client, path, "Authorization", "Bearer "+pass.Token); status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", path, status)
		}
		noCall(t, got, path)
	}
	for path, want := range map[string]string{
		keydoor.URL("primary") + "/chat/completions":                          "",
		keydoor.URL("fallback") + "/models/m:streamGenerateContent?alt=sse":   "alt=sse",
		keydoor.URL("embed") + "/chat/completions?api-version=2024-02-15-pre": "api-version=" + AxAzureAPIVersion,
	} {
		if status, _ := post(t, client, path, "Authorization", "Bearer "+pass.Token); status != 200 {
			t.Errorf("%s: status %d", path, status)
			continue
		}
		if s := <-got; s.query != want {
			t.Errorf("%s: the upstream got query %q, want %q", path, s.query, want)
		}
	}
}

func TestKeyDoorRefusesTheCallAfterItsCap(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	srv, got := upstream(t, 200, `{}`)
	pass := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}, "fallback": {Upstream: srv.URL, Key: "k"}}, 3)
	for i, route := range []string{"primary", "fallback", "primary"} {
		if status, _ := post(t, client, keydoor.URL(route)+"/chat/completions", "Authorization", "Bearer "+pass.Token); status != 200 {
			t.Fatalf("call %d of 3: status %d", i+1, status)
		}
		<-got
	}
	for _, route := range []string{"primary", "fallback"} {
		if status, body := post(t, client, keydoor.URL(route)+"/chat/completions", "Authorization", "Bearer "+pass.Token); status != http.StatusTooManyRequests {
			t.Errorf("call over the cap on %s: status %d %q, want 429", route, status, body)
		}
		noCall(t, got, "a call over the cap")
	}
	if pass.Calls() != 3 {
		t.Errorf("the door counted %d calls, want 3", pass.Calls())
	}
	if _, err := door.Issue("org-1", "test", map[string]DoorRoute{"primary": {Upstream: srv.URL}}, time.Minute, 0); err == nil {
		t.Error("a pass with no call cap was issued")
	}
}

func TestKeyDoorReportsACountThatServeMisreported(t *testing.T) {
	var got []DoorMismatch
	door := &KeyDoor{Mismatch: func(_ context.Context, m DoorMismatch) { got = append(got, m) }}
	client := doorClient(t, door)
	srv, calls := upstream(t, 200, `{}`)
	pass, err := door.Issue("org-7", "turn run-9", map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}}, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer pass.Revoke()
	for range 2 {
		if status, _ := post(t, client, keydoor.URL("primary")+"/chat/completions", "Authorization", "Bearer "+pass.Token); status != 200 {
			t.Fatalf("status %d", status)
		}
		<-calls
	}
	if !pass.Settle(context.Background(), 2) || len(got) != 0 {
		t.Fatalf("a matching count was reported: %+v", got)
	}
	if pass.Settle(context.Background(), 1) {
		t.Error("Settle accepted 1 reported call for 2 at the door")
	}
	want := DoorMismatch{OrgID: "org-7", Purpose: "turn run-9", Counted: 2, Reported: 1}
	if len(got) != 1 || got[0] != want {
		t.Errorf("mismatch reports %+v, want [%+v]", got, want)
	}
}

func TestKeyDoorRevokeCutsAStreamingCall(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	upstreamDone := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			upstreamDone <- r.Context().Err()
		case <-time.After(20 * time.Second):
			upstreamDone <- nil
		}
	}))
	t.Cleanup(srv.Close)
	pass := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}}, 1)
	req, _ := http.NewRequest(http.MethodPost, keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+pass.Token)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "data: first\n" {
		t.Fatalf("first chunk %q, %v", line, err)
	}
	pass.Revoke()
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream went on after the revoke")
	}
	select {
	case err := <-upstreamDone:
		if err == nil {
			t.Error("the upstream call was not cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Error("the upstream call was not cancelled within 5 s")
	}
}

func TestDoorRouteForADecisionProvider(t *testing.T) {
	for _, c := range []struct{ provider, upstream, header, api string }{
		{"google-gemini-decisions", "https://generativelanguage.googleapis.com/v1beta", "x-goog-api-key", DoorDecisions},
		{"openai-decisions", "https://api.openai.com/v1", "authorization", DoorDecisions},
		{"typesafe", "https://api.typesafe.ai", "authorization", DoorTypesafe},
	} {
		route, err := DoorRouteFor(AxModelRoute{Provider: c.provider, Model: DefaultModel(c.provider)}, "k")
		if err != nil || route.Upstream != c.upstream || route.Header != c.header || route.API != c.api || route.Key != "k" {
			t.Errorf("%s: route %+v err %v", c.provider, route, err)
		}
	}
}
