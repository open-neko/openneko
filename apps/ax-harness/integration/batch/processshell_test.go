package batch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/adapters/openneko/processshell"
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
    assert isinstance(denied.reason, (socket.gaierror, PermissionError)), denied.reason
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
	processBin := os.Getenv("HARNESS_PROCESS_TEST_BIN")
	if cli == "" || gateway == "" || processBin == "" {
		t.Skip("set OPENSHELL_TEST_CLI, HARNESS_BATCH_TEST_GATEWAY and HARNESS_PROCESS_TEST_BIN for isolated OpenShell test")
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
	t.Setenv("OPENNEKO_BROKER_TOKEN", "host-only-fixture-secret")
	t.Setenv("MODEL_API_KEY", "host-only-fixture-secret")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, processBin)
	cmd.Env = append(os.Environ(),
		"HARNESS_OPENSHELL_BIN="+cli, "OPENSHELL_GATEWAY="+gateway,
		"HARNESS_PROCESS_IMAGE="+image, "HARNESS_PROCESS_RUN_ID=fixture-process-run",
		"HARNESS_PROCESS_OPERATION_ID=1", "HARNESS_PROCESS_INPUT_ROOT="+input,
		"HARNESS_PROCESS_OUTPUT_ROOT="+output, "HARNESS_PROCESS_TIMEOUT_SECONDS=30",
	)
	cmd.Stdin = strings.NewReader(`{"Argv":["python3","run.py"],"Outputs":["result.csv"]}`)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated process failed: %v; output=%s", err, raw)
	}
	var receipt struct {
		OK     bool                `json:"ok"`
		Result processshell.Result `json:"result"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || !receipt.OK {
		t.Fatalf("invalid process command receipt: %s (%v)", raw, err)
	}
	result := receipt.Result
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
		{"exit-nonzero", "import pathlib, sys\npathlib.Path('result.csv').write_text('partial')\nsys.exit(7)\n", time.Minute},
		{"oversized", "import pathlib\npathlib.Path('result.csv').write_bytes(b'x' * (17 << 20))\n", time.Minute},
		{"cancel", "import pathlib, subprocess, time\npathlib.Path('result.csv').write_text('partial')\nprint('partial ready', flush=True)\nsubprocess.Popen(['python3', '-c', 'import time; time.sleep(60)'])\ntime.sleep(60)\n", 2 * time.Second},
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
			result, err := runner.Run(ctx, processshell.Request{Argv: []string{"python3", "run.py"}, Outputs: []string{"result.csv"}})
			if err == nil {
				t.Fatal("failed or cancelled process published")
			}
			if tc.name == "cancel" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline exceeded, got %v", err)
			}
			if tc.name == "cancel" && !strings.Contains(result.Output, "partial ready") {
				t.Fatalf("cancellation did not reach a running script with a partial output: %+v", result)
			}
			if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
				t.Fatalf("failed or cancelled process exposed output: %v", statErr)
			}
		})
	}
}
