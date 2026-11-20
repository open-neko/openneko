package native

import (
	"path/filepath"
	"strings"
	"testing"
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

func TestServeGetsNoControlZoneSecret(t *testing.T) {
	secrets := []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "MODEL_API_KEY", "NEKO_EMBEDDING_API_KEY", "AZURE_OPENAI_KEY",
		"HF_TOKEN", "VERTEX_ACCESS_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"DATABASE_URL", "OPENNEKO_DATABASE_URL", "OPENNEKO_DATABASE_URL_FILE", "OPENNEKO_TEST_DATABASE_URL", "OPENNEKO_DATABASE_KEY",
		"PGPASSWORD", "PGPASSFILE", "NEKO_PG_PASSWORD", "NEKO_SECRET_KEY", "OPENNEKO_SESSION_SECRET", "OPENNEKO_BACKUP_KEY_FILE",
		"OPENNEKO_JOBS_WAKE_TOKEN", "OPENNEKO_GITHUB_TOKEN", "OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN", "openai_api_key"}
	env := []string{"PATH=/bin", "HOME=/home/u", "HTTPS_PROXY=http://proxy:3128", "OPENNEKO_SANDBOX_URL=unix:///s/sandbox.sock",
		"OPENNEKO_SANDBOX_KEY=box", "OPENNEKO_FILES_BUCKET=s3://b", "OPENNEKO_FILES_BUCKET_KEY_ID=id", "OPENNEKO_FILES_BUCKET_SECRET=bs",
		"GJ_DATABASE_PASSWORD=gj"}
	for _, name := range secrets {
		env = append(env, name+"=planted")
	}
	l := Layout{Root: "/srv/on"}
	procs := Procs(l, Options{Exe: "x", Env: env})
	serve, web := envMap(procs[0].Env), envMap(procs[1].Env)
	for _, name := range secrets {
		if _, ok := serve[name]; ok {
			t.Errorf("serve holds %s", name)
		}
		if web[name] != "planted" {
			t.Errorf("web lost %s", name)
		}
	}
	for _, kv := range procs[0].Env {
		if strings.Contains(kv, "planted") {
			t.Errorf("serve holds %q", kv)
		}
	}
	for _, kv := range env[:9] {
		k, v, _ := strings.Cut(kv, "=")
		if serve[k] != v {
			t.Errorf("serve %s=%q, want %q", k, serve[k], v)
		}
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
