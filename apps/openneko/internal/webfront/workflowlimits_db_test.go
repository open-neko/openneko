package webfront

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func slowModel(t *testing.T, content string, delay time.Duration) *httptest.Server {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(model.Close)
	return model
}

func TestManualWorkflowRunEndsAtItsLimits(t *testing.T) {
	loop := `{"javascriptCode":"const a=file_search({glob:'*'}); console.log(a.paths.length);"}`
	for _, c := range []struct {
		name, env, value string
		delay            time.Duration
		check            func(t *testing.T, status, errText string, calls int)
	}{
		{"tool calls", "OPENNEKO_WORKFLOW_MAX_TOOL_CALLS", "3", 0, func(t *testing.T, status, errText string, calls int) {
			if calls != 3 || status != "failed" && status != "completed" {
				t.Fatalf("%d calls, %s %q", calls, status, errText)
			}
		}},
		{"time", "OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS", "3000", 500 * time.Millisecond, func(t *testing.T, status, errText string, _ int) {
			if status != "failed" || !strings.HasPrefix(errText, "ax turn exceeded its 3s budget") {
				t.Fatalf("%s %q", status, errText)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			front, db, wf := workflowTurnFrontHTTP(t, `[{"id":"s1","description":"Loop"}]`)
			seed(t, db, `update workflow_definition set created_by_thread_id = null where id = $1`, wf.ID)
			t.Setenv(c.env, c.value)
			t.Setenv("OPENNEKO_WORKFLOW_MAX_CONTINUATIONS", "0")
			t.Setenv("OPENNEKO_SERVE_SOCKET", startServe(t))
			front.API.turnResolve = fixedBackend(slowModel(t, loop, c.delay).URL)
			w := call(front, "POST", "/api/v1/workflows/"+wf.ID+"/manual-runs", `{}`, sessionCookie("admin"))
			runID, _ := decode(t, w)["workRunId"].(string)
			status := waitRunFor(t, db, runID, 60*time.Second)
			errText := rowText(t, db, `select coalesce(error, '') from work_run where id = $1`, runID)
			if wfStatus := rowText(t, db, `select status from workflow_run where work_run_id = $1`, runID); wfStatus != status {
				t.Fatalf("workflow run %s, work run %s", wfStatus, status)
			}
			calls := 0
			if n := rowText(t, db, `select count(*)::text from work_run_event where run_id = $1 and kind = 'tool_start'`, runID); n != "" {
				calls = int(pgNumber(n))
			}
			c.check(t, status, errText, calls)
		})
	}
}

func cancelAs(f *Front, runID, who string) (int, string) {
	w := call(f, "POST", "/api/v1/work/runs/"+runID+"/cancel", `{}`, sessionCookie(who))
	return w.Code, w.Body.String()
}

func waitRunFor(t *testing.T, db *pgxpool.Pool, runID string, limit time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		status := rowText(t, db, `select status from work_run where id = $1`, runID)
		done := rowText(t, db, `select count(*)::text from work_run_event where run_id = $1 and kind = 'done'`, runID)
		if status != "queued" && status != "running" && done != "0" {
			return status
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run %s did not end within %s", runID, limit)
	return ""
}

func TestServiceRunFileOwner(t *testing.T) {
	front, db, wf := workflowTurnFrontHTTP(t, `[]`)
	a := front.API
	ctx := context.Background()
	if _, err := a.filesCall(ctx, "org", ptr(wfTurnWorkRun), "/v1/files/search", map[string]any{"query": "policy"}); err == nil ||
		err.Error() != "This run has no owner, so it has no private files to read." {
		t.Fatalf("service run: %v", err)
	}
	seed(t, db, `update workflow_definition set owner_user_id = 'member' where id = $1`, wf.ID)
	if owner, err := a.runOwner(ctx, "org", wfTurnWorkRun); err != nil || owner != "member" {
		t.Fatalf("owned workflow: %q %v", owner, err)
	}
}
