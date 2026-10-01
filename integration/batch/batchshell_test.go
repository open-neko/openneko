package batch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/harness/adapters/openneko/batchshell"
	"github.com/open-neko/harness/internal/batch"
)

const batchScript = `import argparse, csv, hashlib, json, os, pathlib, socket, sys, urllib.error, urllib.request
p = argparse.ArgumentParser()
for name in ('target-day','work-dir','output','summary','max-runtime'):
    p.add_argument('--' + name, required=True)
a = vars(p.parse_args())
assert 'OPENNEKO_BROKER_TOKEN' not in os.environ
try:
    urllib.request.urlopen('http://model-fixture:8080/v1/chat/completions', timeout=5)
    raise AssertionError('ungranted model network access')
except urllib.error.HTTPError as denied:
    assert denied.code == 403, denied.code
except urllib.error.URLError as denied:
    assert isinstance(denied.reason, (socket.gaierror, PermissionError)), denied.reason
cache = pathlib.Path(os.environ['OPENNEKO_QUERY_CACHE_DIR'])
query = 'query { fixture }'
id = hashlib.sha256(query.encode()).hexdigest()
response = cache / 'responses' / (id + '.json')
if not response.exists():
    (cache / 'requests' / (id + '.json')).write_text(json.dumps({'schema_version':1,'id':id,'tool':'mcp_neko_graphjin_execute_graphql','arguments':{'query':query},'response_path':str(response)}))
    sys.exit(4)
assert json.loads(response.read_text())['data']['ok']
with open(a['output'], 'w', newline='') as out:
    writer = csv.writer(out); writer.writerow(['email','score']); writer.writerow(['fixture@example.com','7'])
pathlib.Path(a['summary']).write_text(json.dumps({'status':'completed','target_day':a['target_day'],'merge':{'final_rows':1}}))
`

func TestOpenShellBatchCompartment(t *testing.T) {
	cli := os.Getenv("OPENSHELL_TEST_CLI")
	if cli == "" || os.Getenv("HARNESS_BATCH_TEST_GATEWAY") == "" {
		t.Skip("set OPENSHELL_TEST_CLI and HARNESS_BATCH_TEST_GATEWAY for isolated OpenShell test")
	}
	root := t.TempDir()
	bundle, work, artifacts := filepath.Join(root, "workflow-bundle"), filepath.Join(root, "work"), filepath.Join(root, "artifacts")
	for _, dir := range []string{bundle, work, artifacts} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(bundle, "run.py")
	if err := os.WriteFile(script, []byte(batchScript), 0600); err != nil {
		t.Fatal(err)
	}
	bundleHash, err := batchshell.BundleSHA256(bundle)
	if err != nil {
		t.Fatal(err)
	}
	scriptHash := sha256.Sum256([]byte(batchScript))
	cfg := batch.Config{Script: script, ScriptSHA256: hex.EncodeToString(scriptHash[:]), WorkDir: work, ArtifactDir: artifacts, ArtifactName: "contacts.csv", ScriptCacheDir: batchshell.RemoteCacheDir, TargetDay: "2026-09-15", Columns: []string{"email", "score"}, MaxQueries: 2}
	image := os.Getenv("HARNESS_BATCH_TEST_IMAGE")
	if image == "" {
		image = "harness-openneko:m3"
	}
	runner, err := batchshell.New(cfg, batchshell.Options{CLI: cli, Gateway: os.Getenv("HARNESS_BATCH_TEST_GATEWAY"), Image: image, BundleRoot: bundle, BundleSHA256: bundleHash, RunID: "fixture-batch-run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_BROKER_TOKEN", "host-only-fixture-secret")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, runErr := batch.Run(ctx, cfg, func(_ context.Context, query string) ([]byte, error) {
		if query != "query { fixture }" {
			t.Fatalf("unexpected query %q", query)
		}
		return []byte(`{"data":{"ok":true}}`), nil
	}, runner.Step, runner.Close)
	if runErr != nil {
		log, _ := os.ReadFile(filepath.Join(work, "pipeline_stdout.log"))
		t.Fatalf("batch failed: %v; script log: %s", runErr, log)
	}
	data, err := os.ReadFile(result.Artifact)
	if err != nil || result.Rows != 1 || result.Queries != 1 || !strings.Contains(string(data), "fixture@example.com,7") {
		t.Fatalf("invalid batch result %+v, err=%v, data=%s", result, err, data)
	}
}
