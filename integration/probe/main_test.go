package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A control for the live OpenShell cancellation gate: the same idle fixture must
// observe a direct client's cancellation before blaming the proxy path.
func TestFixtureObservesDirectCancellation(t *testing.T) {
	done := make(chan error, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture(w, r)
		done <- r.Context().Err()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"messages":[{"content":"cancel-probe"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer synthetic-M2-credential")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err := bufio.NewReader(res.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("direct fixture cancellation timed out")
	}
}

func TestSyntheticOAuthAndQueryCredentials(t *testing.T) {
	t.Run("refresh rejects invalid client", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/token", strings.NewReader("grant_type=client_credentials&client_id=fixture&client_secret=wrong"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		oauthFixture(w, r)
		if w.Code != 401 {
			t.Fatalf("got %d", w.Code)
		}
	})
	t.Run("refresh mints bounded token", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/token", strings.NewReader("grant_type=client_credentials&client_id=fixture&client_secret=synthetic-refresh-secret"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		oauthFixture(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"expires_in":3600`) {
			t.Fatal("invalid token response")
		}
	})
	for _, key := range []string{"synthetic-M2-credential", "wrong"} {
		w := httptest.NewRecorder()
		fixture(w, httptest.NewRequest("GET", "/v1/query?key="+key, nil))
		want := 401
		if key == "synthetic-M2-credential" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("query status %d, want %d", w.Code, want)
		}
	}
}
