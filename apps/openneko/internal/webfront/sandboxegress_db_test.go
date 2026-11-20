package webfront

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func expireEgressCheck(a *API) {
	a.egressGate.mu.Lock()
	a.egressGate.at = time.Time{}
	a.egressGate.mu.Unlock()
}

func TestTerminalNeedsAClosedSandbox(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	var answer atomic.Value
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" || r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(answer.Load().(string)))
	}))
	t.Cleanup(box.Close)
	t.Setenv("OPENNEKO_SANDBOX_URL", box.URL)
	t.Setenv("OPENNEKO_SANDBOX_KEY", "k")
	t.Setenv(OperatorEgressEnv, "")

	turnShell := func() bool {
		t.Helper()
		var runID string
		if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, actor_user_id, actor_role) values ('org', $1, 'ax', 'admin', 'admin') returning id::text`,
			threadMine).Scan(&runID); err != nil {
			t.Fatal(err)
		}
		var shell bool
		_, err := a.runChatTurn(ctx, chatTurnOptions{OrgID: "org", ThreadID: threadMine, RunID: runID, Message: "Question",
			Channel: "web", Emit: (&eventLog{}).emit, Summarize: noSummary(t), resolve: fixedBackend("http://unused"),
			keepBot: func(context.Context, botKeepInput) error { return nil },
			runCore: func(_ context.Context, in agentCoreInput) (axRunResult, error) {
				shell = in.Shell
				return axRunResult{Status: "completed", FinalText: "Done"}, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		return shell
	}
	admin := sessionCookie("admin")
	blocked := func() string {
		t.Helper()
		got := decode(t, call(f, "GET", "/api/v1/settings/agent", "", admin))
		s, _ := got["terminalBlocked"].(string)
		return s
	}

	for _, c := range []struct {
		name, body, key, operator string
		shell                     bool
		blocked                   string
	}{
		{"closed", `{"status":"ok","egress":"closed"}`, "k", "", true, ""},
		{"open", `{"status":"ok","egress":"open"}`, "k", "", false, "the sandbox network has a route out"},
		{"pre-6.0 sandbox", `{"status":"ok"}`, "k", "", false, "does not report that its network is closed"},
		{"refused key", `{"status":"ok","egress":"closed"}`, "wrong", "", false, "does not report that its network is closed"},
		{"operator enforces egress", `{"status":"ok","egress":"open"}`, "k", "1", true, ""},
		{"operator setting off again", `{"status":"ok","egress":"open"}`, "k", "", false, "the sandbox network has a route out"},
	} {
		answer.Store(c.body)
		t.Setenv("OPENNEKO_SANDBOX_KEY", c.key)
		t.Setenv(OperatorEgressEnv, c.operator)
		expireEgressCheck(a)
		if got := turnShell(); got != c.shell {
			t.Errorf("%s: the turn's terminal = %v, want %v", c.name, got, c.shell)
		}
		if got := blocked(); c.blocked == "" && got != "" || !strings.Contains(got, c.blocked) {
			t.Errorf("%s: terminalBlocked = %q, want %q", c.name, got, c.blocked)
		}
	}

	rows, err := db.Query(ctx, `select payload->>'previous', payload->>'next' from audit_chain
		where org_id = 'org' and event = 'sandbox:egress_state' order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	var changes []string
	for rows.Next() {
		var previous, next string
		if err := rows.Scan(&previous, &next); err != nil {
			t.Fatal(err)
		}
		changes = append(changes, previous+">"+next)
	}
	if want := "closed>open open>unknown unknown>operator operator>open"; strings.Join(changes, " ") != want {
		t.Errorf("audit events %v, want %s", changes, want)
	}

	answer.Store(`{"status":"ok","egress":"closed"}`)
	expireEgressCheck(a)
	t.Setenv("OPENNEKO_SANDBOX_URL", "")
	if turnShell() {
		t.Error("a turn with no sandbox got the terminal")
	}
}

func TestWorkflowScriptStepsNeedAClosedSandbox(t *testing.T) {
	steps := `[{"id":"pull","description":"Pull stock","script":{"skill":"stockcheck","command":["python3","${SKILL_DIR}/run.py"]}}]`
	for _, egress := range []string{"open", "unknown"} {
		t.Run(egress, func(t *testing.T) {
			a, _, wf := workflowTurnFront(t, steps)
			fake := &fakeStepSandbox{egress: egress, answers: []sandboxAnswerFixture{{ExitCode: 0, Output: "rows: 3\n"}}}
			box := httptest.NewServer(fake)
			defer box.Close()
			t.Setenv("OPENNEKO_SANDBOX_URL", box.URL)
			var shell bool
			o := workflowTurnOpts(wf, &eventLog{})
			o.runCore = func(_ context.Context, in agentCoreInput) (axRunResult, error) {
				shell = in.Shell
				return axRunResult{Status: "completed", FinalText: "Done"}, nil
			}
			_, _ = a.runWorkflowTurn(context.Background(), o)
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.seen) != 0 || shell {
				t.Fatalf("an %s sandbox ran %d script steps; the turn's terminal = %v", egress, len(fake.seen), shell)
			}
		})
	}
}
