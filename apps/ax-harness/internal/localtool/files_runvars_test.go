package localtool

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type shExecutor struct{ dir string }

func (s shExecutor) Exec(ctx context.Context, command, _ string, env map[string]string, _, _ int) (ExecResult, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = s.dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code, err = exit.ExitCode(), nil
	}
	return ExecResult{ExitCode: code, Output: out.String()}, err
}

func (shExecutor) Close() error { return nil }

func TestFileToolsAndTheTerminalShareTheRunFolders(t *testing.T) {
	workspace := t.TempDir()
	runDir := filepath.Join(workspace, "runs", "r1")
	artifacts := filepath.Join(runDir, "artifacts")
	for _, dir := range []string{artifacts, filepath.Join(workspace, "$RUN_DIRX"), filepath.Join(workspace, "$RUN_DIR")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	environ := []string{"RUN_DIR=" + runDir, "ARTIFACT_DIR=" + artifacts}
	f, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.SetEnviron(environ)
	tools := f.Capabilities()
	call := func(i int, input map[string]string) (json.RawMessage, error) {
		raw, _ := json.Marshal(input)
		return tools[i].Call(context.Background(), raw)
	}

	if _, err := call(2, map[string]string{"path": "$RUN_DIR/a.py", "content": "print('from a.py')\n"}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(runDir, "a.py")); err != nil || !strings.Contains(string(data), "from a.py") {
		t.Fatalf("file_write did not put $RUN_DIR/a.py in the run folder: %v", err)
	}
	if raw, err := call(0, map[string]string{"path": "${RUN_DIR}/a.py"}); err != nil || !strings.Contains(string(raw), "from a.py") {
		t.Fatalf("file_read of ${RUN_DIR}/a.py: %s %v", raw, err)
	}
	if _, err := call(1, map[string]string{"path": "$RUN_DIR/a.py", "old_string": "from a.py", "new_string": "edited"}); err != nil {
		t.Fatalf("file_edit of $RUN_DIR/a.py: %v", err)
	}
	if _, err := call(2, map[string]string{"path": "${ARTIFACT_DIR}/result.json", "content": "{}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(artifacts, "result.json")); err != nil {
		t.Fatalf("file_write did not put ${ARTIFACT_DIR}/result.json in the artifact folder: %v", err)
	}

	if _, err := call(2, map[string]string{"path": "$RUN_DIRX/b.txt", "content": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "$RUN_DIRX", "b.txt")); err != nil {
		t.Fatalf("a longer variable name was expanded: %v", err)
	}
	bare, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	raw, _ := json.Marshal(map[string]string{"path": "$RUN_DIR/c.txt", "content": "x"})
	if _, err := bare.Capabilities()[2].Call(context.Background(), raw); err == nil {
		t.Fatal("a run with no RUN_DIR wrote to a literal $RUN_DIR folder")
	}
	outside := t.TempDir()
	f.SetEnviron([]string{"RUN_DIR=" + outside})
	if _, err := call(2, map[string]string{"path": "$RUN_DIR/escape.txt", "content": "x"}); err == nil {
		t.Fatal("a RUN_DIR outside the workspace was written")
	}
	f.SetEnviron(environ)

	if runtime.GOOS == "windows" {
		t.Log("no POSIX shell on Windows; the terminal half runs on Linux and macOS")
		return
	}
	shell, err := NewShell(workspace, shExecutor{dir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	shell.SetEnviron(environ)
	result, err := runShell(t, shell, context.Background(), `{"command":"cd \"$RUN_DIR\" && cat a.py"}`)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "edited") {
		t.Fatalf("the terminal did not find the file in $RUN_DIR: %+v %v", result, err)
	}
}
