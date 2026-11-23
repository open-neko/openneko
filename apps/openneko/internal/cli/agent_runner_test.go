package cli

import (
	"strings"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// TestRunnerGetsNoControlZoneSecret: web, serve and the runner share one
// account on a server install, so the runner's environment is what keeps
// the secrets of web and serve out of an agent turn.
func TestRunnerGetsNoControlZoneSecret(t *testing.T) {
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "MODEL_API_KEY", "NEKO_EMBEDDING_API_KEY", "AZURE_OPENAI_KEY",
		"HF_TOKEN", "VERTEX_ACCESS_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"DATABASE_URL", "OPENNEKO_DATABASE_URL", "OPENNEKO_DATABASE_URL_FILE", "OPENNEKO_TEST_DATABASE_URL", "OPENNEKO_DATABASE_KEY",
		"PGPASSWORD", "PGPASSFILE", "NEKO_PG_PASSWORD", "NEKO_SECRET_KEY", "OPENNEKO_SESSION_SECRET", "OPENNEKO_BACKUP_KEY_FILE",
		"OPENNEKO_JOBS_WAKE_TOKEN", "OPENNEKO_GITHUB_TOKEN", "OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN", "GJ_DATABASE_PASSWORD",
		"OPENNEKO_SANDBOX_KEY", "OPENNEKO_FILES_BUCKET_KEY_ID", "OPENNEKO_FILES_BUCKET_SECRET", "OPENNEKO_KEY_DOOR_SOCKET",
		"OPENNEKO_SERVE_CREDENTIALS", "XDG_CONFIG_HOME", "HOME", "openai_api_key"} {
		t.Setenv(name, "planted")
	}
	t.Setenv("TMPDIR", "/tmp")
	command, env, err := agentRunner()
	if err != nil {
		t.Fatal(err)
	}
	if !serve.RunnerSupported {
		if command != nil {
			t.Fatalf("a runner command on a system that does not build it: %v", command)
		}
		return
	}
	if len(command) != 2 || command[1] != agentRunnerCmd {
		t.Fatalf("command = %v", command)
	}
	for _, entry := range append(env, command...) {
		if strings.Contains(entry, "planted") || !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "TMPDIR=") && !strings.HasPrefix(entry, "SYSTEMROOT=") && entry != command[0] && entry != agentRunnerCmd {
			t.Fatalf("the runner gets %q", entry)
		}
	}
}
