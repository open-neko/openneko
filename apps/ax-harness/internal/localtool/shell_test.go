package localtool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type execCall struct {
	command, skillDir string
	env               map[string]string
	timeout, max      int
}

type fakeExecutor struct {
	calls  []execCall
	closed bool
	err    error
}

func (f *fakeExecutor) Exec(_ context.Context, command, skillDir string, env map[string]string, timeoutSeconds, maxOutput int) (ExecResult, error) {
	f.calls = append(f.calls, execCall{command, skillDir, env, timeoutSeconds, maxOutput})
	if f.err != nil {
		return ExecResult{}, f.err
	}
	output := "[" + skillDir + "]"
	if strings.Contains(command, "big") {
		output = strings.Repeat("x", maxOutput)
	}
	return ExecResult{ExitCode: 3, Output: output, Truncated: strings.Contains(command, "big")}, nil
}

func (f *fakeExecutor) Close() error {
	f.closed = true
	return nil
}

func openShell(t *testing.T) (*Shell, *fakeExecutor) {
	t.Helper()
	executor := &fakeExecutor{}
	s, err := OpenShell(t.TempDir(), executor)
	if err != nil {
		t.Fatal(err)
	}
	return s, executor
}

func runShell(t *testing.T, s *Shell, ctx context.Context, input string) (ExecResult, error) {
	t.Helper()
	raw, err := s.Capability().Call(ctx, json.RawMessage(input))
	var result ExecResult
	if err == nil {
		if decodeErr := json.Unmarshal(raw, &result); decodeErr != nil {
			t.Fatal(decodeErr)
		}
	}
	return result, err
}

func TestShellSendsCommandsToTheSandbox(t *testing.T) {
	s, executor := openShell(t)
	s.SetEnviron([]string{"RUN_DIR=/runs/a", "ARTIFACT_DIR=/runs/a/out", "HARNESS_MODEL_API_KEY=secret", "OPENNEKO_BROKER_TOKEN=token", "PATH=/bin"})
	result, err := runShell(t, s, context.Background(), `{"command":"echo hi"}`)
	if err != nil || result.ExitCode != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	call := executor.calls[0]
	if call.command != "echo hi" || call.timeout != defaultShellTimeout || call.max != defaultShellOutput {
		t.Fatalf("call = %+v", call)
	}
	if len(call.env) != 2 || call.env["RUN_DIR"] != "/runs/a" || call.env["ARTIFACT_DIR"] != "/runs/a/out" {
		t.Fatalf("the sandbox got more than the run's paths: %v", call.env)
	}
	if err := s.Close(); err != nil || !executor.closed {
		t.Fatal("closing the shell did not end the sandbox run")
	}
}

func TestShellReportsAnUnavailableSandbox(t *testing.T) {
	s, executor := openShell(t)
	executor.err = errors.New("terminal unavailable: connection refused")
	if _, err := runShell(t, s, context.Background(), `{"command":"true"}`); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v", err)
	}
}

func TestShellRejectsBadInput(t *testing.T) {
	s, executor := openShell(t)
	for _, input := range []string{`{"command":""}`, `{"command":"ls","timeout_seconds":601}`} {
		if _, err := runShell(t, s, context.Background(), input); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if len(executor.calls) != 0 {
		t.Fatal("a bad command reached the sandbox")
	}
	if _, err := OpenShell("relative/dir", executor); err == nil {
		t.Fatal("accepted a relative workspace")
	}
	if _, err := OpenShell(t.TempDir(), nil); err == nil {
		t.Fatal("opened a terminal with no sandbox")
	}
}

func TestShellHostLimits(t *testing.T) {
	s, _ := openShell(t)
	if err := s.SetLimits(0, 50_000); err == nil {
		t.Fatal("zero timeout was accepted")
	}
	if err := s.SetLimits(30, 2_000); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Capability().Description, "2000 characters") || !strings.Contains(s.Capability().Description, "30 seconds") {
		t.Fatalf("description does not show host limits: %s", s.Capability().Description)
	}
	raw, err := s.Capability().Call(context.Background(), json.RawMessage(`{"command":"big"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Output    string `json:"output"`
		Truncated bool   `json:"truncated"`
	}
	if json.Unmarshal(raw, &out) != nil || !out.Truncated || len([]rune(out.Output)) > 2_000+40 {
		t.Fatalf("host output cap not applied: truncated=%v len=%d", out.Truncated, len(out.Output))
	}
}

func TestShellSetsSkillDirFromTheScriptPath(t *testing.T) {
	t.Setenv("SKILL_DIR", "")
	skills := t.TempDir()
	for _, path := range []string{"stock-check/scripts/check.py", "lead-report/scripts/report.py", "lead-report/scripts/common.py", "stock-check/scripts/common.py"} {
		if err := os.MkdirAll(filepath.Join(skills, filepath.Dir(path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skills, path), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := openShell(t)
	s.SetSkillsRoot(skills)
	for command, want := range map[string]string{
		`echo \"[$SKILL_DIR]\" \"$SKILL_DIR/scripts/check.py\"`:                          "[" + filepath.Join(skills, "stock-check") + "]",
		`echo \"[${SKILL_DIR}]\" ${SKILL_DIR}/scripts/report.py`:                         "[" + filepath.Join(skills, "lead-report") + "]",
		`echo \"[$SKILL_DIR]\" $SKILL_DIR/scripts/common.py`:                             "[]",
		`echo \"[$SKILL_DIR]\" $SKILL_DIR/scripts/check.py $SKILL_DIR/scripts/report.py`: "[]",
		`echo \"[$SKILL_DIR]\" $SKILL_DIR/../../etc/passwd`:                              "[]",
	} {
		result, err := runShell(t, s, context.Background(), `{"command":"`+command+`"}`)
		if err != nil || !strings.HasPrefix(result.Output, want) {
			t.Fatalf("%s: result=%+v err=%v want %s", command, result, err, want)
		}
	}
}
