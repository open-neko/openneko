package webfront

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitAuditFailures(t *testing.T, a *API, want int) AuditHealth {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h := a.AuditHealth()
		if h.FailureCount >= want || time.Now().After(deadline) {
			return h
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuditWebhookErrorReplyIsADeliveryFailure(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	a := &API{AuditWebhook: srv.URL}
	a.reportAuditFailure(auditEvent{OrgID: "o", Event: "e"}, errTest("append failed"))
	h := waitAuditFailures(t, a, 2)
	if posts.Load() != 1 {
		t.Fatalf("posts: %d", posts.Load())
	}
	if h.Healthy || h.FailureCount != 2 || h.LastError == nil || !strings.Contains(*h.LastError, "HTTP 503") {
		t.Errorf("health: %+v", h)
	}
}

func TestAuditWebhookSuccessIsNoFailure(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	a := &API{AuditWebhook: srv.URL}
	a.reportAuditFailure(auditEvent{OrgID: "o", Event: "e"}, errTest("append failed"))
	deadline := time.Now().Add(5 * time.Second)
	for posts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if h := a.AuditHealth(); h.FailureCount != 1 || !strings.Contains(*h.LastError, "append failed") {
		t.Errorf("health: %+v", h)
	}
}

func TestAuditWebhookRefusesPlainHTTPOffLocalhost(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://alerts.example/audit": true,
		"http://localhost:9000/hook":   true,
		"http://127.0.0.1:9000/hook":   true,
		"http://alerts.example/audit":  false,
		"ftp://alerts.example/audit":   false,
		"alerts.example/audit":         false,
		"https:///audit":               false,
	} {
		if err := auditWebhookAllowed(raw); (err == nil) != ok {
			t.Errorf("%q: %v", raw, err)
		}
	}
	a := &API{AuditWebhook: "http://alerts.example/audit"}
	a.reportAuditFailure(auditEvent{OrgID: "o", Event: "e"}, errTest("append failed"))
	if h := a.AuditHealth(); h.FailureCount != 2 || !strings.Contains(*h.LastError, "https") {
		t.Errorf("health: %+v", h)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
