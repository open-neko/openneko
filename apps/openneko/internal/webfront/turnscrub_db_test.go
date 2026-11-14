package webfront

import (
	"context"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

func TestStoredRunEventsHoldNoSecret(t *testing.T) {
	const key = "sk-planted-0123456789abcdef"
	front, db := signedInChatFront(t)
	wf := seedWorkflowTurn(t, db, `[{"id":"s1","description":"Read stock"}]`)
	a := front.API
	a.SecretsDir = t.TempDir()
	if err := secrets.Write(secrets.Store{"pack.shop": {"API_KEY": key}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	a.turnResolve = fixedBackend("http://unused")
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		_ = in.Emit(ctx, jsObjOf("type", "tool_start", "id", "t1", "name", "terminal", "input", jsObjOf("command", "env | grep KEY")))
		_ = in.Emit(ctx, jsObjOf("type", "tool_end", "id", "t1", "result", jsObjOf("stdout", "API_KEY="+key, "rows", []any{jsObjOf("value", key)})))
		_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "The key is "+key))
		return axRunResult{Status: "completed", FinalText: "Done."}, nil
	}
	check := func(kind, runID string) {
		t.Helper()
		if status := waitRun(t, db, runID); status != "completed" {
			t.Fatalf("%s: status %s", kind, status)
		}
		stored := rowText(t, db, `select string_agg(payload::text, E'\n' order by id) from work_run_event where run_id = $1`, runID)
		if strings.Contains(stored, key) || strings.Count(stored, "[REDACTED]") != 3 {
			t.Errorf("%s run events:\n%s", kind, stored)
		}
	}

	w := call(front, "POST", "/api/v1/work/threads/"+threadMine+"/runs", `{"message":"Show the env"}`, sessionCookie("member"))
	runID, _ := decode(t, w)["runId"].(string)
	if w.Code != 200 {
		t.Fatalf("chat %d %s", w.Code, w.Body)
	}
	check("chat", runID)

	w = call(front, "POST", "/api/v1/workflows/"+wf.ID+"/manual-runs", `{}`, sessionCookie("admin"))
	runID, _ = decode(t, w)["workRunId"].(string)
	if w.Code != 200 {
		t.Fatalf("manual %d %s", w.Code, w.Body)
	}
	check("workflow", runID)
}
