//go:build !windows

package gjhost

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheGraphJinChildGetsItsSecretsOnStdinOnly(t *testing.T) {
	env := []string{"GJ_DATABASE_HOST=db", "GJ_DATABASE_PASSWORD=planted-gj", "OTEL_EXPORTER_OTLP_HEADERS=authorization=planted-otel",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://otel:4318", "PATH=/bin", "OPENAI_API_KEY=planted-model"}
	got := strings.Join(ChildEnv(env), " ")
	if strings.Contains(got, "planted") || !strings.Contains(got, "GJ_DATABASE_HOST=db") || !strings.Contains(got, "OTEL_EXPORTER_OTLP_ENDPOINT=") {
		t.Errorf("child env: %s", got)
	}
	secrets := ChildSecrets(env)
	if len(secrets) != 2 || secrets["GJ_DATABASE_PASSWORD"] != "planted-gj" || secrets["OTEL_EXPORTER_OTLP_HEADERS"] != "authorization=planted-otel" {
		t.Errorf("child secrets: %v", secrets)
	}

	out := filepath.Join(t.TempDir(), "line")
	s := &Supervisor{Log: io.Discard, Secrets: secrets, Command: func(string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `head -n 1 > "$0"; cat > /dev/null`, out)
	}}
	c, err := s.startChild(filepath.Join(t.TempDir(), "gj.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.stdin.Close(); <-c.done }()
	var line map[string]string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if raw, _ := os.ReadFile(out); len(raw) > 0 && json.Unmarshal(raw, &line) == nil {
			break
		}
	}
	if line["GJ_DATABASE_PASSWORD"] != "planted-gj" || line["OTEL_EXPORTER_OTLP_HEADERS"] != "authorization=planted-otel" {
		t.Errorf("the child's first stdin line: %v", line)
	}
}
