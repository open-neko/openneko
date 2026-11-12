package webfront

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestActionExecuteJob(t *testing.T) {
	var status int
	var reply string
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(body))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	a := &API{Gate: &Gate{WorkerURL: srv.URL}}
	status, reply = 200, `{"ok":true}`
	if err := a.ActionExecute(t.Context(), "org", "ar-1"); err != nil {
		t.Errorf("ok: %v", err)
	}
	if !slices.Equal(calls, []string{`POST /admin/actions/execute {"actionRequestId":"ar-1","orgId":"org"}`}) {
		t.Errorf("calls: %v", calls)
	}
	status, reply = 503, `{"error":"action adapters are not ready"}`
	if err := a.ActionExecute(t.Context(), "org", "ar-1"); err == nil || err.Error() != "action ar-1 failed (503): action adapters are not ready" {
		t.Errorf("not ready: %v", err)
	}
	status, reply = 500, ``
	if err := a.ActionExecute(t.Context(), "org", "ar-1"); err == nil || err.Error() != "action ar-1 failed (500): Internal Server Error" {
		t.Errorf("no body: %v", err)
	}
	a.Gate.WorkerURL = "http://127.0.0.1:1"
	if err := a.ActionExecute(t.Context(), "org", "ar-1"); err == nil || !strings.Contains(err.Error(), "the worker did not answer") {
		t.Errorf("worker down: %v", err)
	}
}
