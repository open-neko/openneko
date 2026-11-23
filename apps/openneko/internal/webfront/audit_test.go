package webfront

import (
	"encoding/json"
	"io"
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

func captureAuditStderr(t *testing.T) *lockedBuffer {
	t.Helper()
	buf, old := &lockedBuffer{}, auditStderr
	auditStderr = buf
	t.Cleanup(func() { auditStderr = old })
	return buf
}

func TestAuditWebhookRedirectIsADeliveryFailure(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	stderr := captureAuditStderr(t)
	a := &API{AuditWebhook: srv.URL + "/hooks/s3cret?token=s3cret"}
	a.reportAuditFailure(auditEvent{OrgID: "o", Event: "e"}, errTest("append failed"))
	h := waitAuditFailures(t, a, 2)
	host := strings.TrimPrefix(srv.URL, "http://")
	if h.FailureCount != 2 || h.LastError == nil || *h.LastError != "audit failure webhook ("+host+"): HTTP 307" {
		t.Errorf("health: %+v", h)
	}
	if followed.Load() != 0 {
		t.Error("the webhook followed a redirect")
	}
	if strings.Contains(stderr.String(), "s3cret") {
		t.Errorf("stderr holds the URL: %s", stderr.String())
	}
}

func TestAuditWebhookTimeoutIsADeliveryFailure(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	old := auditWebhookTimeout
	auditWebhookTimeout = 100 * time.Millisecond
	t.Cleanup(func() { auditWebhookTimeout = old })
	stderr := captureAuditStderr(t)
	a := &API{AuditWebhook: srv.URL + "/hooks/s3cret?token=s3cret"}
	a.reportAuditFailure(auditEvent{OrgID: "o", Event: "e"}, errTest("append failed"))
	h := waitAuditFailures(t, a, 2)
	host := strings.TrimPrefix(srv.URL, "http://")
	if h.FailureCount != 2 || h.LastError == nil || *h.LastError != "audit failure webhook ("+host+"): no reply in 100ms" {
		t.Errorf("health: %+v", h)
	}
	if strings.Contains(stderr.String(), "s3cret") {
		t.Errorf("stderr holds the URL: %s", stderr.String())
	}
}

func TestAuditWebhookTransportErrorHoldsNoURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	stderr := captureAuditStderr(t)
	a := &API{AuditWebhook: url + "/hooks/s3cret?token=s3cret"}
	a.reportAuditFailure(auditEvent{OrgID: "o", Event: "e"}, errTest("append failed"))
	h := waitAuditFailures(t, a, 2)
	if h.FailureCount != 2 || h.LastError == nil || strings.Contains(*h.LastError, "s3cret") || strings.Contains(stderr.String(), "s3cret") {
		t.Errorf("health %+v, stderr %s", h, stderr.String())
	}
}

func TestAuditWebhookStateAtStartAndOnHealth(t *testing.T) {
	for raw, want := range map[string]string{
		"":                                    `{"state":"off","host":null,"error":null}`,
		"https://alerts.example/hooks/s3cret": `{"state":"ok","host":"alerts.example","error":null}`,
		"http://alerts.example/hooks/s3cret":  `{"state":"refused","host":"alerts.example","error":"the URL must be https, or http on localhost"}`,
		"https://alerts.example/%zz/s3cret":   `{"state":"refused","host":"an unreadable URL","error":"the URL does not parse"}`,
	} {
		t.Setenv("OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL", raw)
		a := &API{}
		var start strings.Builder
		a.CheckAuditWebhook(&start)
		got, _ := json.Marshal(a.AuditWebhookState())
		if string(got) != want {
			t.Errorf("%q: state %s", raw, got)
		}
		refused := strings.Contains(want, "refused")
		if lines := strings.Count(start.String(), "\n"); (lines == 1) != refused || lines > 1 || strings.Contains(start.String(), "s3cret") {
			t.Errorf("%q: start warning %q", raw, start.String())
		}
		w := httptest.NewRecorder()
		a.serveHealth(w, "/health/security", true)
		var body struct {
			Status       string            `json:"status"`
			AuditWebhook AuditWebhookState `json:"auditWebhook"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if hook, _ := json.Marshal(body.AuditWebhook); string(hook) != want || (body.Status == "degraded") != refused {
			t.Errorf("%q: health %s", raw, w.Body.String())
		}
		w = httptest.NewRecorder()
		a.serveHealth(w, "/health/security", false)
		if strings.Contains(w.Body.String(), "auditWebhook") {
			t.Errorf("%q: the public health shows the webhook: %s", raw, w.Body.String())
		}
	}
}

func TestAuditWebhookStateOnlyForAnAdminThroughTheRouter(t *testing.T) {
	t.Setenv("OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL", "http://alerts.example/hooks/s3cret")
	f, _ := pushFront(t)
	f.API.CheckAuditWebhook(io.Discard)
	for who, header := range map[string]map[string]string{
		"admin":      sessionCookie("admin"),
		"member":     sessionCookie("member"),
		"signed out": nil,
	} {
		res := call(f, "GET", "/health/security", "", header)
		shows := strings.Contains(res.Body.String(), "auditWebhook")
		if res.Code != 200 || shows != (who == "admin") || strings.Contains(res.Body.String(), "s3cret") {
			t.Errorf("%s: %d %s", who, res.Code, res.Body.String())
		}
	}
}
