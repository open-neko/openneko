package webfront

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
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

func TestActionExecuteSendsOnlyBuiltInKindsToTheWorker(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, string(body))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	a := &API{Gate: &Gate{WorkerURL: srv.URL}, DB: db}
	seed(t, db, `insert into action_request (id, org_id, scope, kind, status, payload) values
		('aaaaaaaa-0000-4000-8000-000000000001', 'org', 'external', 'send_webhook', 'approved', '{}'),
		('aaaaaaaa-0000-4000-8000-000000000002', 'org', 'external', 'shop.refund', 'approved', '{}')`)
	if err := a.ActionExecute(t.Context(), "org", "aaaaaaaa-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "aaaaaaaa-0000-4000-8000-000000000001") {
		t.Fatalf("built-in kind: %v", calls)
	}
	calls = nil
	_ = a.ActionExecute(t.Context(), "org", "aaaaaaaa-0000-4000-8000-000000000002")
	if len(calls) != 0 {
		t.Fatalf("a pack kind went to the worker: %v", calls)
	}
	if got := rowText(t, db, `select r.status || '|' || e.status || '|' || coalesce(e.error, '') from action_request r join action_execution e on e.action_request_id = r.id where r.id = 'aaaaaaaa-0000-4000-8000-000000000002'`); got != `failed|failed|no adapter registered for kind "shop.refund"` {
		t.Fatalf("pack kind without a definition: %s", got)
	}
}
