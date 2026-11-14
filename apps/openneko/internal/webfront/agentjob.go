package webfront

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// An agent job is one non-interactive Ax turn for a background job, such as
// the business profile or the bootstrap metrics. It gets an empty workspace
// and, at most, read-only GraphJin tools.

type upstreamProviderError struct{ msg string }

func (e upstreamProviderError) Error() string { return e.msg }

type dataSourceAuthorizationError struct{ msg string }

func (e dataSourceAuthorizationError) Error() string { return e.msg }

var (
	jsWhitespaceAll  = regexp.MustCompile(`[` + jsSpaceClass + `]+`)
	dataSourceAuth   = jsRe(`(?i)\b(?:blocked for role|graphjin\b(?s:.*)\bunauthorized)\b`)
	upstreamPatterns = []struct {
		re             *regexp.Regexp
		kind, provider string
	}{
		{jsRe(`(?i)Authentication token has expired`), "auth", ""},
		{jsRe(`(?i)must authenticate again`), "auth", ""},
		{jsRe(`(?i)\bHTTP\s+401\b`), "auth", ""},
		{regexp.MustCompile(`(?i)\binvalid[_` + jsSpaceClass + `-]*api[_` + jsSpaceClass + `-]*key\b`), "auth", ""},
		{jsRe(`(?i)Gemini\s+HTTP\s+(5\d{2})\b`), "5xx", "google-gemini"},
		{jsRe(`(?i)Anthropic\s+HTTP\s+(5\d{2})\b`), "5xx", "anthropic"},
		{jsRe(`(?i)OpenAI\s+HTTP\s+(5\d{2})\b`), "5xx", "openai"},
		{jsRe(`(?i)API call failed after \d+ retries:\s*HTTP\s+(5\d{2})\b`), "5xx", ""},
		{jsRe(`(?i)\bHTTP\s+(5\d{2})\b`), "5xx", ""},
	}
)

func agentOutputHead(stdout string) (string, string, bool) {
	head := jsTrim(jsSlice(stdout, 1500))
	if strings.HasPrefix(head, "{") || strings.HasPrefix(head, "```") {
		return "", "", false
	}
	return head, jsTrim(jsWhitespaceAll.ReplaceAllString(jsSlice(head, 300), " ")), true
}

// agentExecutionError is detectAgentExecutionError() in
// packages/llm/src/agent-error.ts: a final text that reports a data source
// or model provider failure instead of an answer.
func agentExecutionError(stdout string) error {
	head, detail, ok := agentOutputHead(stdout)
	if !ok {
		return nil
	}
	if dataSourceAuth.MatchString(head) {
		return dataSourceAuthorizationError{"Data source authorization failed — verify the authentication mode in /admin/settings/data: " + detail}
	}
	for _, p := range upstreamPatterns {
		if !p.re.MatchString(head) {
			continue
		}
		label := ""
		if p.provider != "" {
			label = " (" + p.provider + ")"
		}
		heading := "Upstream provider unavailable" + label
		if p.kind == "auth" {
			heading = "Provider authentication failed" + label + " — re-check the API key in /admin/settings/agent"
		}
		return upstreamProviderError{heading + ": " + detail}
	}
	return nil
}

var jsonFence = jsRe("^\\s*```(?:json)?\\s*((?s:.*?))\\s*```\\s*$")

// parseAgentJSON is parseJsonFromOutput() in agent-backends/output.ts.
func parseAgentJSON(raw string) (any, error) {
	candidate := jsTrim(raw)
	if m := jsonFence.FindStringSubmatch(candidate); m != nil {
		candidate = m[1]
	}
	candidate = jsTrim(candidate)
	if v, err := jsParse([]byte(candidate)); err == nil {
		return v, nil
	}
	first, last := strings.Index(candidate, "{"), strings.LastIndex(candidate, "}")
	if first == -1 || last == -1 || last < first {
		return nil, errors.New("agent output not parseable as JSON (no object braces found): " + jsSlice(candidate, 200))
	}
	return jsParse([]byte(candidate[first : last+1]))
}

func agentCorrectionPrompt(base, rejected, reason string) string {
	return strings.Join([]string{base, "", "<previous-attempt-rejected>", "Your previous reply was rejected by the output validator:",
		reason, "", "Tail of the rejected reply:", utf16Tail(rejected, 2000), "</previous-attempt-rejected>", "",
		"Redo the task and reply in EXACTLY the required output format —", "no surrounding prose, no apologies."}, "\n")
}

var jobLabelUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// isolatedJobWorkspace is ensureIsolatedJobWorkspace(): an empty tree, so the
// job never sees member memories, uploads or earlier runs.
func isolatedJobWorkspace(label string) (agentWorkspace, func(), error) {
	safe := jsSlice(jobLabelUnsafe.ReplaceAllString(label, "_"), 48)
	if safe == "" {
		safe = "job"
	}
	root, err := os.MkdirTemp("", "openneko-agent-"+safe+"-")
	if err != nil {
		return agentWorkspace{}, nil, err
	}
	run := filepath.Join(root, "runs", "current")
	ws := agentWorkspace{OrgRoot: root, SkillsRoot: filepath.Join(root, "skills"), MemoryRoot: filepath.Join(root, "memory"),
		KnowledgeRoot: filepath.Join(root, "knowledge"), UploadsRoot: filepath.Join(root, "uploads"), RunsRoot: filepath.Join(root, "runs"),
		ThreadUploadsRoot: filepath.Join(root, "uploads", "job"), RunRoot: run, ArtifactRoot: filepath.Join(run, "artifacts"), BinRoot: filepath.Join(run, "bin")}
	cleanup := func() { os.RemoveAll(root) }
	for _, dir := range []string{ws.SkillsRoot, ws.MemoryRoot, ws.KnowledgeRoot, ws.ThreadUploadsRoot, ws.ArtifactRoot, ws.BinRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			cleanup()
			return agentWorkspace{}, nil, err
		}
	}
	return ws, cleanup, nil
}

type agentJob struct {
	OrgID, RunID, Label, Prompt string
	GraphjinRead                bool
	TimeoutMs                   int
	MaxAttempts                 int
	Validate                    func(finalText string) error
}

// runAgentJob is sandboxAgentBackendForJob() with runValidatedAgentTurn():
// a reply that fails validation goes back to the agent with the reason, up
// to MaxAttempts. A failed turn or a provider failure ends the job at once.
func (a *API) runAgentJob(ctx context.Context, j agentJob) (string, error) {
	resolve := a.turnResolve
	if resolve == nil {
		resolve = a.agentBackendWithKeys
	}
	backend, keys, err := resolve(ctx, j.OrgID)
	if err != nil {
		return "", err
	}
	ws, cleanup, err := isolatedJobWorkspace(j.Label + "-" + j.RunID)
	if err != nil {
		return "", err
	}
	defer cleanup()
	o := axTurnOptions{OrgID: j.OrgID, ThreadID: j.RunID, Tag: j.Label + " " + j.RunID, Workspace: &ws,
		TimeoutMs: j.TimeoutMs, Keys: keys, Socket: a.serveSocket()}
	if j.GraphjinRead {
		b := turnBinding{OrgID: j.OrgID, ThreadID: j.RunID, RunID: j.RunID, Kind: "agent-job"}
		o.Servers = []string{"neko_graphjin"}
		var be toolBackend = a.runTools(b)
		if a.jobTools != nil {
			be = a.jobTools(b)
		}
		if o.Tools, err = turnTools(b, be, o.Servers); err != nil {
			return "", err
		}
	}
	release, err := a.slots.acquire(ctx, func() error { return nil }, func() {})
	if err != nil {
		return "", err
	}
	defer release()
	attempts := max(1, j.MaxAttempts)
	var lastErr, lastOutput string
	for attempt := 1; attempt <= attempts; attempt++ {
		o.Prompt = j.Prompt
		if attempt > 1 {
			o.Prompt = agentCorrectionPrompt(j.Prompt, lastOutput, lastErr)
		}
		res, err := runAx(ctx, backend, o)
		if err != nil {
			return "", err
		}
		if res.Status != "completed" {
			if res.Error != "" {
				return "", errors.New(res.Error)
			}
			return "", fmt.Errorf("ax returned status=%s", res.Status)
		}
		if err := agentExecutionError(res.FinalText); err != nil {
			return "", err
		}
		if j.Validate == nil {
			return res.FinalText, nil
		}
		verr := j.Validate(res.FinalText)
		if verr == nil {
			return res.FinalText, nil
		}
		lastErr, lastOutput = verr.Error(), res.FinalText
		log.Printf("[%s] attempt %d/%d failed validation: %s", j.Label, attempt, attempts, lastErr)
	}
	return "", errors.New("[" + j.Label + "] output failed validation after " + strconv.Itoa(attempts) + " attempt(s): " + lastErr)
}
