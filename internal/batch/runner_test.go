package batch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fixture = `import argparse, csv, hashlib, json, os, pathlib, sys
p = argparse.ArgumentParser()
for name in ('target-day','work-dir','output','summary','max-runtime'):
    p.add_argument('--' + name, required=True)
a = vars(p.parse_args())
root = pathlib.Path(os.environ['OPENNEKO_QUERY_CACHE_DIR'])
for query in ('query { first }', 'query { second }'):
    digest = hashlib.sha256(query.encode()).hexdigest()
    response = root / 'responses' / (digest + '.json')
    if not response.exists():
        request = {'schema_version':1,'id':digest,'tool':'mcp_neko_graphjin_execute_graphql','arguments':{'query':query},'response_path':str(response)}
        (root / 'requests' / (digest + '.json')).write_text(json.dumps(request))
        sys.exit(4)
    assert json.loads(response.read_text())['data']
assert 'OPENNEKO_BROKER_TOKEN' not in os.environ
with open(a['output'], 'w', newline='') as f:
    w = csv.writer(f); w.writerow(['email','score']); w.writerow(['one@example.com','7'])
pathlib.Path(a['summary']).write_text(json.dumps({'status':'completed','target_day':a['target_day'],'merge':{'final_rows':1}}))
`

func setup(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	script := filepath.Join(root, "script.py")
	if err := os.WriteFile(script, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	work, artifacts := filepath.Join(root, "work"), filepath.Join(root, "artifacts")
	for _, dir := range []string{work, artifacts} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	hash := sha256.Sum256([]byte(fixture))
	return Config{Script: script, ScriptSHA256: hex.EncodeToString(hash[:]), WorkDir: work, ArtifactDir: artifacts, TargetDay: "2026-09-15", Columns: []string{"email", "score"}, MaxQueries: 4}
}

func localStep(ctx context.Context, cfg Config, output io.Writer) error {
	cache := cfg.ScriptCacheDir
	if cache == "" {
		cache = filepath.Join(cfg.WorkDir, "graphjin-cache")
	}
	cmd := exec.CommandContext(ctx, "python3", cfg.Script,
		"--target-day", cfg.TargetDay, "--work-dir", cfg.WorkDir,
		"--output", filepath.Join(cfg.WorkDir, "union_final.csv"),
		"--summary", filepath.Join(cfg.WorkDir, "summary.json"), "--max-runtime", "1200")
	cmd.Dir = filepath.Dir(cfg.Script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "PYTHONDONTWRITEBYTECODE=1", "OPENNEKO_QUERY_CACHE_DIR=" + cache}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdout, cmd.Stderr = output, output
	return cmd.Run()
}

func TestRunMaterializesQueriesAndPublishesValidatedCSV(t *testing.T) {
	cfg := setup(t)
	var calls []string
	result, err := Run(context.Background(), cfg, func(_ context.Context, query string) ([]byte, error) {
		calls = append(calls, query)
		return []byte(`{"data":{"ok":true}}`), nil
	}, localStep, nil)
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(cfg.WorkDir, "pipeline_stdout.log"))
		t.Fatalf("%v: %s", err, log)
	}
	if result.Queries != 2 || result.Rows != 1 || len(calls) != 2 || calls[0] != "query { first }" || calls[1] != "query { second }" {
		t.Fatalf("unexpected batch result: %+v calls=%v", result, calls)
	}
	data, err := os.ReadFile(result.Artifact)
	if err != nil || !strings.Contains(string(data), "one@example.com,7") {
		t.Fatalf("missing CSV artifact: %v", err)
	}
	for _, query := range calls {
		id := sha256.Sum256([]byte(query))
		if _, err := os.Stat(filepath.Join(cfg.WorkDir, "graphjin-cache", "receipts", hex.EncodeToString(id[:])+".json")); err != nil {
			t.Fatalf("missing query receipt: %v", err)
		}
	}
	replayed, err := Run(context.Background(), cfg, func(context.Context, string) ([]byte, error) {
		t.Fatal("cached query was dispatched again")
		return nil, nil
	}, localStep, nil)
	if err != nil || replayed.Queries != 2 || replayed.SHA256 != result.SHA256 {
		t.Fatalf("cached run was not stable: %+v %v", replayed, err)
	}
	first := sha256.Sum256([]byte(calls[0]))
	response := filepath.Join(cfg.WorkDir, "graphjin-cache", "responses", hex.EncodeToString(first[:])+".json")
	if err := os.WriteFile(response, []byte(`{"data":{"forged":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), cfg, func(context.Context, string) ([]byte, error) {
		t.Fatal("tampered response triggered an ungoverned retry")
		return nil, nil
	}, localStep, nil); err == nil {
		t.Fatal("tampered response was accepted")
	}
}

func TestDeniedQueryNeverPublishesArtifact(t *testing.T) {
	cfg := setup(t)
	_, err := Run(context.Background(), cfg, func(context.Context, string) ([]byte, error) {
		return nil, context.Canceled
	}, localStep, nil)
	if err == nil {
		t.Fatal("denied query unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(cfg.ArtifactDir, "union_final.csv")); !os.IsNotExist(err) {
		t.Fatalf("denied query published artifact: %v", err)
	}
}

func TestCleanupFailurePreventsArtifactPublication(t *testing.T) {
	cfg := setup(t)
	closed := 0
	_, err := Run(context.Background(), cfg, func(context.Context, string) ([]byte, error) {
		return []byte(`{"data":{"ok":true}}`), nil
	}, localStep, func() error {
		closed++
		return errors.New("sandbox deletion failed")
	})
	if err == nil || !strings.Contains(err.Error(), "sandbox deletion failed") || closed != 1 {
		t.Fatalf("cleanup failure was not fatal: %v, calls=%d", err, closed)
	}
	if _, err := os.Stat(filepath.Join(cfg.ArtifactDir, "union_final.csv")); !os.IsNotExist(err) {
		t.Fatalf("artifact published before cleanup: %v", err)
	}
}

func TestCancellationLetsPipelineCleanUpChildren(t *testing.T) {
	cfg := setup(t)
	script := `import os, pathlib, subprocess, time
p = subprocess.Popen(['sleep', '30'], start_new_session=True)
pathlib.Path(os.environ['OPENNEKO_QUERY_CACHE_DIR']).parent.joinpath('ready').write_text('1')
try:
    while True: time.sleep(1)
finally:
    p.terminate(); p.wait(timeout=5)
    pathlib.Path(os.environ['OPENNEKO_QUERY_CACHE_DIR']).parent.joinpath('cleaned').write_text('1')
`
	if err := os.WriteFile(cfg.Script, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(script))
	cfg.ScriptSHA256 = hex.EncodeToString(hash[:])
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, cfg, func(context.Context, string) ([]byte, error) {
			return nil, nil
		}, localStep, nil)
		done <- err
	}()
	ready := filepath.Join(cfg.WorkDir, "ready")
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("pipeline stopped before cancellation: %v", err)
		case <-deadline:
			t.Fatal("pipeline did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("unexpected cancellation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WorkDir, "cleaned")); err != nil {
		t.Fatalf("pipeline children were not cleaned up: %v", err)
	}
}

func TestConcurrentBatchRunIsRejected(t *testing.T) {
	cfg := setup(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), cfg, func(context.Context, string) ([]byte, error) {
			select {
			case <-entered:
			default:
				close(entered)
			}
			<-release
			return []byte(`{"data":{"ok":true}}`), nil
		}, localStep, nil)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first run did not reach query")
	}
	if _, err := Run(context.Background(), cfg, func(context.Context, string) ([]byte, error) {
		t.Fatal("concurrent run dispatched a query")
		return nil, nil
	}, localStep, nil); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent run was not rejected: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
