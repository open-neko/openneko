package cli

import (
	"strings"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func TestAgentRunnerGetsOnlyPathAndTmpdir(t *testing.T) {
	t.Setenv("OPENNEKO_SANDBOX_KEY", "serve-sandbox-key")
	t.Setenv("OPENNEKO_KEY_DOOR_SOCKET", "/run/door.sock")
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
	for _, entry := range env {
		if !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "TMPDIR=") {
			t.Fatalf("the runner gets %q", entry)
		}
	}
}
