package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	webStandInEnv = "OPENNEKO_TEST_WEB_STAND_IN"
	webArgSecret  = "planted-secret-in-web-arguments"
	webEnvSecret  = "postgres://web:planted-secret-in-web-environment@db/openneko"
)

// TestWebStandIn stands for web: a process of the same account with a secret
// in its arguments and in its environment.
func TestWebStandIn(t *testing.T) {
	if os.Getenv(webStandInEnv) == "" {
		t.Skip("helper process")
	}
	fmt.Println("ready")
	time.Sleep(10 * time.Minute)
}

func startWebStandIn(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWebStandIn$", "-test.count=1", webArgSecret)
	cmd.Env = append(os.Environ(), webStandInEnv+"=1", "DATABASE_URL="+webEnvSecret)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, _ := bufio.NewReader(out).ReadString('\n'); !strings.Contains(line, "ready") {
		t.Fatalf("the web stand-in did not start: %q", line)
	}
	return cmd.Process.Pid
}

// readWebProcess counts a read as done only when it gives the planted
// secrets, so a refusal that comes from an empty answer is not a pass.
func readWebProcess(pid int, refused func(string, error)) {
	both, err := unix.SysctlRaw("kern.procargs2", pid)
	refused("read web's arguments and environment", holds(both, err, webArgSecret, webEnvSecret))
	args, err := unix.SysctlRaw("kern.procargs", pid)
	refused("read web's arguments", holds(args, err, webArgSecret))
	_, err = unix.SysctlKinfoProc("kern.proc.pid", pid)
	refused("read web's process record", err)
}

func holds(b []byte, err error, want ...string) error {
	if err != nil {
		return err
	}
	for _, w := range want {
		if !bytes.Contains(b, []byte(w)) {
			return fmt.Errorf("the answer of %d bytes holds no %q", len(b), w)
		}
	}
	return nil
}

func runOutput(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil && len(bytes.TrimSpace(out)) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
	}
	return err
}
