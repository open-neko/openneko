package harness

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMainRefusesATurn(t *testing.T) {
	if os.Getenv("AX_HARNESS_TEST_MAIN") == "1" {
		os.Args = os.Args[:1]
		Main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainRefusesATurn$", "-test.count=1")
	cmd.Env = append(os.Environ(), "AX_HARNESS_TEST_MAIN=1", "HARNESS_MODEL_PROVIDER=openai", "HARNESS_MODEL=gpt-5", "HARNESS_MODEL_API_KEY=k")
	cmd.Stdin = strings.NewReader(`{"version":1,"run_id":"r1","input_id":"r1","prompt":"hello"}` + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("exit %v, want 2\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), ErrNoTurnHere.Error()) {
		t.Errorf("stderr %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "run.started") {
		t.Errorf("the binary started a turn: %s", stdout.String())
	}
}
