package batch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/harness/adapters/openneko/processshell"
)

const isolatedProcessScript = `import os, pathlib, socket, urllib.error, urllib.request
secret = 'host-only-fixture-secret'
assert secret not in repr(dict(os.environ))
for pid in pathlib.Path('/proc').iterdir():
    if not pid.name.isdigit():
        continue
    try:
        assert secret.encode() not in (pid / 'environ').read_bytes()
    except (PermissionError, FileNotFoundError, ProcessLookupError):
        pass
try:
    urllib.request.urlopen('http://model-fixture:8080/v1/chat/completions', timeout=5)
    raise AssertionError('ungranted model network access')
except urllib.error.HTTPError as denied:
    assert denied.code == 403, denied.code
except urllib.error.URLError as denied:
    assert isinstance(denied.reason, socket.gaierror), denied.reason
try:
    socket.create_connection(('1.1.1.1', 80), timeout=3)
    raise AssertionError('ungranted direct network access')
except OSError:
    pass
pathlib.Path('result.csv').write_text('lead_id\nLEAD-42\n')
print('process completed without host credentials')
`

func TestOpenShellProcessCompartment(t *testing.T) {
	cli := os.Getenv("OPENSHELL_TEST_CLI")
	gateway := os.Getenv("HARNESS_BATCH_TEST_GATEWAY")
	if cli == "" || gateway == "" {
		t.Skip("set OPENSHELL_TEST_CLI and HARNESS_BATCH_TEST_GATEWAY for isolated OpenShell test")
	}
	root := t.TempDir()
	input, output := filepath.Join(root, "input"), filepath.Join(root, "published")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "run.py"), []byte(isolatedProcessScript), 0600); err != nil {
		t.Fatal(err)
	}
	image := os.Getenv("HARNESS_BATCH_TEST_IMAGE")
	if image == "" {
		image = "harness-openneko:m3"
	}
	runner, err := processshell.New(processshell.Options{CLI: cli, Gateway: gateway, Image: image,
		InputRoot: input, OutputRoot: output, RunID: "fixture-process-run", OperationID: 1, Memory: "512Mi", TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_BROKER_TOKEN", "host-only-fixture-secret")
	t.Setenv("MODEL_API_KEY", "host-only-fixture-secret")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := runner.Run(ctx, processshell.Request{Argv: []string{"python3", "run.py"}, Outputs: []string{"result.csv"}})
	if err != nil {
		t.Fatalf("isolated process failed: %v; output=%s", err, result.Output)
	}
	data, err := os.ReadFile(filepath.Join(output, "result.csv"))
	if err != nil || string(data) != "lead_id\nLEAD-42\n" || !strings.Contains(result.Output, "process completed") || result.OutputTruncated {
		t.Fatalf("invalid process receipt: result=%+v data=%q err=%v", result, data, err)
	}
	digest := sha256.Sum256([]byte(isolatedProcessScript))
	if result.InputDigest == "" || result.InputDigest == hex.EncodeToString(digest[:]) {
		t.Fatal("expected a nonempty whole-directory input digest")
	}
}

func TestOpenShellProcessFailureDoesNotPublish(t *testing.T) {
	cli := os.Getenv("OPENSHELL_TEST_CLI")
	gateway := os.Getenv("HARNESS_BATCH_TEST_GATEWAY")
	if cli == "" || gateway == "" {
		t.Skip("set OPENSHELL_TEST_CLI and HARNESS_BATCH_TEST_GATEWAY for isolated OpenShell test")
	}
	image := os.Getenv("HARNESS_BATCH_TEST_IMAGE")
	if image == "" {
		image = "harness-openneko:m3"
	}
	for _, tc := range []struct {
		name, script string
		timeout      time.Duration
	}{
		{"symlink", "import pathlib\npathlib.Path('result.csv').symlink_to('/etc/passwd')\n", time.Minute},
		{"cancel", "import subprocess, time\nsubprocess.Popen(['python3', '-c', 'import time; time.sleep(60)'])\ntime.sleep(60)\n", 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			input, output := filepath.Join(root, "input"), filepath.Join(root, "published")
			if err := os.Mkdir(input, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(input, "run.py"), []byte(tc.script), 0600); err != nil {
				t.Fatal(err)
			}
			runner, err := processshell.New(processshell.Options{CLI: cli, Gateway: gateway, Image: image,
				InputRoot: input, OutputRoot: output, RunID: "fixture-process-" + tc.name, OperationID: 1,
				Memory: "512Mi", TimeoutSeconds: 90})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			_, err = runner.Run(ctx, processshell.Request{Argv: []string{"python3", "run.py"}, Outputs: []string{"result.csv"}})
			if err == nil {
				t.Fatal("failed or cancelled process published")
			}
			if tc.name == "cancel" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline exceeded, got %v", err)
			}
			if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
				t.Fatalf("failed or cancelled process exposed output: %v", statErr)
			}
		})
	}
}
