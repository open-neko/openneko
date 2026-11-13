package webfront

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

// scriptStepResult is ScriptStepResult in
// packages/llm/src/workflows/script-steps.ts. ExitCode is nil for null.
type scriptStepResult struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Status      string `json:"status"`
	ExitCode    *int   `json:"exitCode"`
	DurationMs  int64  `json:"durationMs"`
	OutputPath  string `json:"outputPath,omitempty"`
	OutputTail  string `json:"outputTail,omitempty"`
	Error       string `json:"error,omitempty"`
}

const (
	stepDefaultTimeoutSeconds = 1_200
	stepTailBytes             = 4_000
	stepMaxOutput             = 1_000_000
)

type scriptStepsInput struct {
	Steps     []any
	Workspace agentWorkspace
	Input     any
	Allowed   []string
	Sandbox   *sandbox.Config
	Emit      func(ctx context.Context, event any) error
	now       func() time.Time
}

// runScriptSteps is runScriptSteps(): the steps with a script run in order
// before the agent turn, and the first failure skips the rest.
func runScriptSteps(ctx context.Context, in scriptStepsInput) ([]scriptStepResult, error) {
	var results []scriptStepResult
	var client *sandbox.Client
	var clientErr error
	if in.Sandbox != nil {
		if client, clientErr = sandbox.NewClient(*in.Sandbox, in.Workspace.OrgRoot); clientErr == nil {
			defer client.Close()
		}
	}
	blocked := ""
	for _, step := range in.Steps {
		if !jsTruthy(jsGet(step, "script")) {
			continue
		}
		id, description := jsTemplate(jsProp(step, "id")), jsTemplate(jsProp(step, "description"))
		if blocked != "" {
			results = append(results, scriptStepResult{ID: id, Description: description, Status: "skipped",
				Error: "skipped because step " + blocked + " did not succeed"})
			continue
		}
		if err := in.Emit(ctx, statusEvent("Running script step: "+description)); err != nil {
			return nil, err
		}
		r, err := runScriptStep(ctx, in, client, clientErr, step, id, description)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
		if err := in.Emit(ctx, statusEvent("Script step "+id+" "+r.Status+" in "+jsNumberText(jsRound(float64(r.DurationMs)/1000))+"s")); err != nil {
			return nil, err
		}
		if r.Status != "ok" {
			blocked = id
		}
	}
	return results, nil
}

func runScriptStep(ctx context.Context, in scriptStepsInput, client *sandbox.Client, clientErr error, step any, id, description string) (scriptStepResult, error) {
	now := time.Now
	if in.now != nil {
		now = in.now
	}
	started := now()
	base := scriptStepResult{ID: id, Description: description}
	fail := func(msg string) (scriptStepResult, error) {
		r := base
		r.Status, r.DurationMs, r.Error = "failed", now().Sub(started).Milliseconds(), msg
		return r, nil
	}
	if clientErr != nil {
		return fail(clientErr.Error())
	}
	if client == nil {
		return fail("script steps run in the sandbox service, and this run has none: OPENNEKO_SANDBOX_URL is not set, or an admin turned the Terminal off at Admin, Settings, Agent")
	}
	script := jsGet(step, "script")
	skill := jsTemplate(jsProp(script, "skill"))
	if in.Allowed != nil && !slices.Contains(in.Allowed, skill) {
		return fail("skill " + skill + " is not available to this run")
	}
	skillsRoot, _ := filepath.Abs(in.Workspace.SkillsRoot)
	skillDir := filepath.Join(skillsRoot, skill)
	if filepath.IsAbs(skill) {
		skillDir = filepath.Clean(skill)
	}
	if rel, err := filepath.Rel(skillsRoot, skillDir); err != nil || strings.HasPrefix(rel, "..") || !pathExists(skillDir) {
		return fail("skill " + skill + " is not installed")
	}
	vars := map[string]string{"SKILL_DIR": skillDir, "RUN_DIR": in.Workspace.RunRoot, "ARTIFACT_DIR": in.Workspace.ArtifactRoot}
	var argv []string
	for _, arg := range jsArray(jsGet(script, "command")) {
		expanded, err := expandStepArg(jsTemplate(arg), vars, in.Input)
		if err != nil {
			return fail(err.Error())
		}
		argv = append(argv, shellQuote(expanded))
	}

	logDir := filepath.Join(in.Workspace.RunRoot, "steps")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return scriptStepResult{}, err
	}
	name := safeStepName(id)
	outputPath := filepath.Join(logDir, name+".output.log")
	inputPath := filepath.Join(logDir, name+".input.json")
	input := in.Input
	if input == nil {
		input = &jsObj{}
	}
	if err := os.WriteFile(inputPath, []byte(jsStringify(input, false)), 0o644); err != nil {
		return scriptStepResult{}, err
	}
	timeout := stepDefaultTimeoutSeconds
	if t, ok := jsGet(script, "timeoutSeconds").(float64); ok {
		timeout = int(t)
	}
	command := `OPENNEKO_WORKFLOW_INPUT="$(cat ` + shellQuote(inputPath) + `)" exec ` + strings.Join(argv, " ")

	var code *int
	var timedOut bool
	var output, execErr string
	res, err := client.ExecIn(ctx, in.Workspace.RunRoot, command, skillDir,
		map[string]string{"RUN_DIR": in.Workspace.RunRoot, "ARTIFACT_DIR": in.Workspace.ArtifactRoot}, timeout, stepMaxOutput)
	if err != nil {
		execErr = err.Error()
	} else {
		c := res.ExitCode
		code, timedOut, output = &c, res.TimedOut, res.Output
	}
	if err := os.WriteFile(outputPath, []byte(output), 0o644); err != nil {
		return scriptStepResult{}, err
	}
	tail := []byte(output)
	r := base
	r.Status = "failed"
	switch {
	case timedOut:
		r.Status = "timeout"
	case code != nil && *code == 0:
		r.Status = "ok"
	}
	if !timedOut {
		r.ExitCode = code
	}
	r.DurationMs = now().Sub(started).Milliseconds()
	r.OutputPath = outputPath
	r.OutputTail = jsTrim(nodeUTF8(tail[max(0, len(tail)-stepTailBytes):]))
	switch {
	case execErr != "":
		r.Error = execErr
	case timedOut:
		r.Error = "timed out after " + strconv.Itoa(timeout) + "s"
	}
	return r, nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// nodeUTF8 is Buffer.toString("utf8"): each invalid byte becomes U+FFFD.
func nodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		out.WriteRune(r)
		b = b[size:]
	}
	return out.String()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

var stepArgVar = regexp.MustCompile(`\$\{(input\.[A-Za-z0-9_]+|[A-Z_]+)(?::-([^}]*))?\}`)

// expandStepArg is expandArg().
func expandStepArg(arg string, vars map[string]string, input any) (string, error) {
	var out strings.Builder
	last := 0
	for _, m := range stepArgVar.FindAllStringSubmatchIndex(arg, -1) {
		out.WriteString(arg[last:m[0]])
		last = m[1]
		name := arg[m[2]:m[3]]
		hasFallback := m[4] >= 0
		if field, ok := strings.CutPrefix(name, "input."); ok {
			value := jsProp(input, field)
			if _, undef := value.(jsUndefined); undef || value == nil || value == "" {
				if hasFallback {
					out.WriteString(arg[m[4]:m[5]])
					continue
				}
				return "", errors.New("the trigger payload has no " + field + " for ${" + name + "}")
			}
			if s, ok := value.(string); ok {
				out.WriteString(s)
			} else {
				out.WriteString(jsStringify(value, false))
			}
			continue
		}
		value, ok := vars[name]
		if !ok {
			return "", errors.New("unknown variable ${" + name + "}")
		}
		out.WriteString(value)
	}
	out.WriteString(arg[last:])
	return out.String(), nil
}

// formatStepResults is formatScriptStepResults(): the <step_results> block
// the agent turn receives.
func formatStepResults(results []scriptStepResult) string {
	if len(results) == 0 {
		return ""
	}
	blocks := make([]string, len(results))
	for i, r := range results {
		code := ""
		if r.ExitCode != nil {
			code = strconv.Itoa(*r.ExitCode)
		}
		lines := []string{`<step id="` + r.ID + `" status="` + r.Status + `" exit_code="` + code + `" duration_s="` +
			jsNumberText(jsRound(float64(r.DurationMs)/1000)) + `">`, "description: " + r.Description}
		if r.Error != "" {
			lines = append(lines, "error: "+r.Error)
		}
		if r.OutputPath != "" {
			lines = append(lines, "output_file: "+r.OutputPath)
		}
		if r.OutputTail != "" {
			lines = append(lines, "output_tail:\n"+r.OutputTail)
		}
		blocks[i] = strings.Join(append(lines, "</step>"), "\n")
	}
	return section("workflow_step_results", "body", strings.Join(blocks, "\n"))
}

// safeStepName replaces each UTF-16 unit, as the JavaScript regular
// expression does.
func safeStepName(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("__")
		default:
			b.WriteByte('_')
		}
	}
	return orDefault(jsSlice(b.String(), 60), "step")
}
