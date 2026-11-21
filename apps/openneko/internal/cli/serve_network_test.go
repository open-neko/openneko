package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/filestore"
)

func TestNetworkPartGetsNoSecretVariable(t *testing.T) {
	env := []string{"PATH=/usr/bin", "OPENNEKO_SANDBOX_KEY=sandbox", "OPENAI_API_KEY=model", "OPENNEKO_DATABASE_URL=postgres://main",
		"NEKO_SECRET_KEY=secret", serveCredentialsEnv + "=/data/credentials", "OPENNEKO_FILES_BUCKET_SECRET=bucket",
		"AWS_SECRET_ACCESS_KEY=aws", "GJ_DATABASE_PASSWORD=gj", "OTEL_EXPORTER_OTLP_HEADERS=a=b", "AWS_REGION=eu-west-1", "OPENNEKO_FILES_DIR=/data/files"}
	got := strings.Join(networkEnv(env), "\n")
	for _, gone := range []string{"OPENNEKO_SANDBOX_KEY", "OPENAI_API_KEY", "OPENNEKO_DATABASE_URL", "NEKO_SECRET_KEY", serveCredentialsEnv, "OPENNEKO_FILES_BUCKET_SECRET",
		"AWS_SECRET_ACCESS_KEY", "GJ_DATABASE_PASSWORD", "OTEL_EXPORTER_OTLP_HEADERS"} {
		if strings.Contains(got, gone+"=") {
			t.Errorf("the network part gets %s:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"PATH=/usr/bin", "AWS_REGION=eu-west-1", "OPENNEKO_FILES_DIR=/data/files"} {
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

func TestNetworkPartGetsTheBucketLoginOnStdin(t *testing.T) {
	t.Setenv("OPENNEKO_FILES_BUCKET", "s3://b")
	t.Setenv("OPENNEKO_FILES_BUCKET_KEY_ID", "id")
	t.Setenv("OPENNEKO_FILES_BUCKET_SECRET", "bs")
	raw, err := json.Marshal(networkStart{FileStore: filestore.FromEnv("/data/files")})
	if err != nil {
		t.Fatal(err)
	}
	var in networkStart
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if in.FileStore.Bucket != "s3://b" || in.FileStore.AccessKeyID != "id" || in.FileStore.SecretKey != "bs" {
		t.Errorf("the network part gets %+v", in.FileStore)
	}
}
