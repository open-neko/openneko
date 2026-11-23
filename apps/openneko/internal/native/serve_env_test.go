package native

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/secretenv"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := m[k]; dup {
			m[k] = "DUPLICATE"
			continue
		}
		m[k] = v
	}
	return m
}

func TestServeAndWebGetOnlyTheSecretsTheyRead(t *testing.T) {
	notRead := []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "MODEL_API_KEY", "AZURE_OPENAI_KEY", "PLANTED_TOKEN",
		"HF_TOKEN", "VERTEX_ACCESS_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"DATABASE_URL", "OPENNEKO_DATABASE_URL", "OPENNEKO_TEST_DATABASE_URL", "OPENNEKO_DATABASE_KEY",
		"PGPASSWORD", "PGPASSFILE", "NEKO_SECRET_KEY", "OPENNEKO_BACKUP_KEY_FILE", "openai_api_key"}
	webOnly := []string{"NEKO_EMBEDDING_API_KEY", "OPENNEKO_DATABASE_URL_FILE", "NEKO_PG_PASSWORD", "OPENNEKO_SESSION_SECRET",
		"OPENNEKO_JOBS_WAKE_TOKEN", "OPENNEKO_GITHUB_TOKEN", "OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN"}
	env := []string{"PATH=/bin", "HOME=/home/u", "HTTPS_PROXY=http://proxy:3128", "OPENNEKO_SANDBOX_URL=unix:///s/sandbox.sock",
		"OPENNEKO_FILES_BUCKET=s3://b", "GJ_DATABASE_PASSWORD=gj", "GJ_DATABASE_HOST=gj-host", "OTEL_EXPORTER_OTLP_HEADERS=authorization=otel-header",
		"OPENNEKO_SANDBOX_KEY=box", "OPENNEKO_FILES_BUCKET_KEY_ID=id", "OPENNEKO_FILES_BUCKET_SECRET=bs"}
	for _, name := range append(notRead, webOnly...) {
		env = append(env, name+"=planted")
	}
	l := Layout{Root: "/srv/on"}
	procs := Procs(l, Options{Exe: "x", Env: env})
	serve, web := envMap(procs[0].Env), envMap(procs[1].Env)
	for _, name := range notRead {
		if _, ok := web[name]; ok {
			t.Errorf("web holds %s, which it does not read", name)
		}
	}
	var webHanded, handed map[string]string
	if err := json.Unmarshal(procs[1].Stdin, &webHanded); err != nil {
		t.Fatal(err)
	}
	for _, name := range webOnly {
		if name == "OPENNEKO_DATABASE_URL_FILE" {
			if web[name] != "planted" {
				t.Errorf("web lost %s", name)
			}
			continue
		}
		if _, ok := web[name]; ok {
			t.Errorf("web holds %s in its environment", name)
		}
		if webHanded[name] != "planted" {
			t.Errorf("web's stdin lost %s", name)
		}
	}
	if _, ok := web["OPENNEKO_SANDBOX_KEY"]; ok || webHanded["OPENNEKO_SANDBOX_KEY"] != "box" {
		t.Error("web holds the sandbox key in its environment, or lost it on stdin")
	}
	for _, kv := range append(procs[0].Env, procs[1].Env...) {
		if strings.Contains(kv, "planted") && kv != "OPENNEKO_DATABASE_URL_FILE=planted" || strings.Contains(kv, "otel-header") || strings.HasPrefix(kv, "GJ_DATABASE_PASSWORD") {
			t.Errorf("serve or web holds %q", kv)
		}
	}
	for _, name := range []string{"OPENNEKO_SANDBOX_KEY", "OPENNEKO_FILES_BUCKET_KEY_ID", "OPENNEKO_FILES_BUCKET_SECRET"} {
		if _, ok := serve[name]; ok {
			t.Errorf("serve holds %s in its environment", name)
		}
		if _, ok := webHanded[name]; ok && name != "OPENNEKO_SANDBOX_KEY" {
			t.Errorf("web gets %s", name)
		}
	}
	for _, kv := range env[:5] {
		k, v, _ := strings.Cut(kv, "=")
		if serve[k] != v || web[k] != v {
			t.Errorf("serve %s=%q, web %q, want %q", k, serve[k], web[k], v)
		}
	}
	if serve["GJ_DATABASE_HOST"] != "gj-host" {
		t.Errorf("serve lost GJ_DATABASE_HOST: %q", serve["GJ_DATABASE_HOST"])
	}
	if err := json.Unmarshal(procs[0].Stdin, &handed); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"OPENNEKO_SANDBOX_KEY": "box", "OPENNEKO_FILES_BUCKET_KEY_ID": "id", "OPENNEKO_FILES_BUCKET_SECRET": "bs",
		"AWS_ACCESS_KEY_ID": "planted", "AWS_SECRET_ACCESS_KEY": "planted", "AWS_SESSION_TOKEN": "planted",
		"GJ_DATABASE_PASSWORD": "gj", "OTEL_EXPORTER_OTLP_HEADERS": "authorization=otel-header"}
	if len(handed) != len(want) {
		t.Errorf("serve's stdin %v, want %v", handed, want)
	}
	for k, v := range want {
		if handed[k] != v {
			t.Errorf("serve's stdin %s=%q, want %q", k, handed[k], v)
		}
	}
	if serve[secretenv.StdinEnv] != "1" || web[secretenv.StdinEnv] != "1" {
		t.Errorf("serve reads stdin %q, web %q", serve[secretenv.StdinEnv], web[secretenv.StdinEnv])
	}
	if serve["OPENNEKO_KEY_DOOR_SOCKET"] != filepath.Join("/srv/on", "door", "keydoor.sock") || serve["OPENNEKO_SERVE_CREDENTIALS"] != filepath.Join("/srv/on", "credentials") {
		t.Errorf("serve lost its own paths: %v", serve)
	}
	for _, step := range Steps(l, Options{Exe: "x", Env: env}, true) {
		if envMap(step.Env)["OPENNEKO_DATABASE_URL"] != "planted" {
			t.Errorf("step %s lost the database URL", step.Name)
		}
	}
}

func TestServeSecretsReadTheirFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sandbox-key")
	if err := os.WriteFile(file, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"OPENNEKO_SANDBOX_KEY_FILE=" + file, "OPENNEKO_FILES_BUCKET_SECRET_FILE=" + filepath.Join(dir, "absent")}
	var handed map[string]string
	if err := json.Unmarshal(ServeSecrets(env), &handed); err != nil || len(handed) != 1 || handed["OPENNEKO_SANDBOX_KEY"] != "from-file" {
		t.Errorf("handed %v %v", handed, err)
	}
	serve := envMap(Procs(Layout{Root: "/r"}, Options{Exe: "x", Env: env})[0].Env)
	if _, ok := serve["OPENNEKO_SANDBOX_KEY_FILE"]; ok {
		t.Error("serve holds the sandbox key file variable")
	}
}

// TestWebReadsIsComplete reads every literal variable name that the code
// reads; a secret-named one must be in webReads or in notForWeb, so a new
// variable does not vanish from web unnoticed.
func TestWebReadsIsComplete(t *testing.T) {
	notForWeb := map[string]string{
		"ADVENTUREWORKS_AUTH_MODE":           "seed",
		"ADVENTUREWORKS_RECONCILE_AUTH_MODE": "seed",
		"PGPASSWORD":                         "seed",
		"NEKO_SECRET_KEY":                    "container init",
		"OPENNEKO_BACKUP_KEY_FILE":           "backup and restore commands",
		"OPENNEKO_FILES_BUCKET_KEY_ID":       "serve, on stdin",
		"OPENNEKO_FILES_BUCKET_SECRET":       "serve, on stdin",
		"AWS_ACCESS_KEY_ID":                  "serve, on stdin",
		"AWS_SECRET_ACCESS_KEY":              "serve, on stdin",
		"AWS_SESSION_TOKEN":                  "serve, on stdin",
		"HARNESS_MODEL_API_KEY":              "agent runner",
		"HARNESS_FALLBACK_API_KEY":           "agent runner",
		"HARNESS_EMBEDDING_API_KEY":          "agent runner",
	}
	envName := regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,}$`)
	readers := map[string]bool{"behaviorLimit": true, "readLimitInt": true, "assetsRoot": true, "runtimeDir": true, "Get": true}
	fset := token.NewFileSet()
	found := map[string]string{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "testdata") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			var name string
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || name == "Setenv" || name == "Unsetenv" || !(readers[name] || strings.Contains(strings.ToLower(name), "env")) {
				return true
			}
			v, _ := strconv.Unquote(lit.Value)
			if envName.MatchString(v) && secretenv.SecretName(v) && !strings.HasPrefix(v, "OPENNEKO_TEST_") {
				found[v] = fset.Position(lit.Pos()).String()
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 10 {
		t.Fatalf("the scan found %d secret-named variables: %v", len(found), found)
	}
	for v, at := range found {
		if !webReads[v] && notForWeb[v] == "" {
			t.Errorf("%s (read at %s) is secret-named: add it to webReads if web reads it, else to notForWeb", v, at)
		}
	}
	for v := range webReads {
		if _, ok := found[v]; !ok && !strings.HasSuffix(v, "_FILE") && v != "OPENNEKO_AGENT_MODEL_KEY_ENV" {
			t.Errorf("webReads holds %s, which no code reads", v)
		}
	}
}
