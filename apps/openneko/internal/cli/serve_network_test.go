package cli

import (
	"strings"
	"testing"
)

func TestNetworkPartGetsNoSecretVariable(t *testing.T) {
	env := []string{"PATH=/usr/bin", "OPENNEKO_SANDBOX_KEY=sandbox", "OPENAI_API_KEY=model", "OPENNEKO_DATABASE_URL=postgres://main",
		"NEKO_SECRET_KEY=secret", serveCredentialsEnv + "=/data/credentials", "OPENNEKO_FILES_BUCKET_SECRET=bucket",
		"AWS_SECRET_ACCESS_KEY=aws", "OPENNEKO_FILES_DIR=/data/files"}
	got := strings.Join(networkEnv(env), "\n")
	for _, gone := range []string{"OPENNEKO_SANDBOX_KEY", "OPENAI_API_KEY", "OPENNEKO_DATABASE_URL", "NEKO_SECRET_KEY", serveCredentialsEnv} {
		if strings.Contains(got, gone+"=") {
			t.Errorf("the network part gets %s:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"PATH=/usr/bin", "OPENNEKO_FILES_BUCKET_SECRET=bucket", "AWS_SECRET_ACCESS_KEY=aws", "OPENNEKO_FILES_DIR=/data/files"} {
		if !strings.Contains(got, kept) {
			t.Errorf("the network part lost %s:\n%s", kept, got)
		}
	}
}

func TestServeNeedsCredentialsInProduction(t *testing.T) {
	t.Setenv("OPENNEKO_ENV", "production")
	t.Setenv(serveCredentialsEnv, "")
	cmd := newServeCmd()
	cmd.SetArgs([]string{"--agent-socket="})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "needs --credentials") {
		t.Fatalf("serve in production with no credentials: %v", err)
	}
}
