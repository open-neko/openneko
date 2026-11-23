package webfront

import (
	"context"
	"strings"
	"testing"
)

const failingStep = `[{"id":"s1","description":"Find alternates","script":{"skill":"alternates","command":"python3 run.py"}}]`

func TestAFailedScriptStepMarksTheWorkflowRunResult(t *testing.T) {
	a, db, wf := workflowTurnFront(t, failingStep)
	model := fakeModel(t, []string{
		`{"javascriptCode":"final('Find alternates', {})"}`,
		`{"javascriptCode":"final('Report', {found:3});"}`,
		"Found 3 alternates for SKU 1042.",
	})
	t.Setenv("OPENNEKO_SERVE_SOCKET", startServe(t))
	log := &eventLog{}
	o := workflowTurnOpts(wf, log)
	o.resolve = fixedBackend(model.URL)
	res, err := a.runWorkflowTurn(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	const mark = "Script step s1 failed: script steps run in the sandbox service"
	if res.Status != "completed" || !strings.HasPrefix(res.StepError, mark) {
		t.Fatalf("result %+v\nevents %s", res, strings.Join(log.all(), "\n"))
	}
	workStatus, workErr, wfStatus, _, wfErr := runRow(t, db)
	if workStatus != "completed" || wfStatus != "completed" || workErr != res.StepError || wfErr != res.StepError {
		t.Fatalf("rows: %s %q %s %q", workStatus, workErr, wfStatus, wfErr)
	}
	if last := log.all()[len(log.all())-1]; !strings.Contains(last, `"stepError":"`+mark) {
		t.Fatalf("done event %s", last)
	}
	if got := rowText(t, db, `select content from work_message where thread_id = $1 and run_id is null`, threadMine); !strings.Contains(got, mark) {
		t.Fatalf("bot chat post %q", got)
	}
}

func TestAFailedScriptStepMarksTheAPIRunResult(t *testing.T) {
	a, db, job := apiRunFront(t, "single")
	seed(t, db, `update workflow_definition set steps = $2::jsonb where id = $1`, job.WorkflowID, failingStep)
	a.turnResolve = fixedBackend("http://unused")
	var prompt string
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		prompt = in.Prompt
		return axRunResult{Status: "completed", FinalText: "Found 3 alternates for SKU 1042."}, nil
	}
	if err := a.WorkflowRunFire(context.Background(), job, 1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `<step id="s1" status="failed"`) || !strings.Contains(prompt, section("step_failure_rule")) {
		t.Errorf("the turn prompt lacks the failed step or the rule:\n%s", prompt)
	}
	got := apiRunRow(t, db)
	if !strings.HasPrefix(got, "completed | script_step_failed | completed | completed | Script step s1 failed: ") {
		t.Errorf("run %s", got)
	}
}
