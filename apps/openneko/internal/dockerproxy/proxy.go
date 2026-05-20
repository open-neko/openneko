// Package dockerproxy detects a running openneko-*-worker-1 container and
// re-executes plugin-op CLI subcommands inside it. The brew-installed openneko
// on the host can't reach the worker's named-volume manifest or its baked
// node_modules, so install/list/remove/secrets/marketplace operations have
// to happen container-side. This package makes that transparent — operators
// just run `openneko install …` and the proxy handles it.
//
// Source-build dev users (running the worker via `pnpm dev` on the host) get
// no proxy because no openneko-*-worker-1 container exists; the same command
// falls through to the local in-process implementation. `--local` on the
// root command forces local even when a container is running.
package dockerproxy

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// EnvMarker is set on the proxied invocation so the inner openneko inside
// the worker container doesn't try to detect-and-proxy itself recursively.
const EnvMarker = "OPENNEKO_PROXIED"

// FindRunningWorker returns the name of a running openneko-{prod,dev,demo}-
// worker-1 container, or "" if none / docker unreachable / we are already
// inside a proxied invocation.
func FindRunningWorker() string {
	if os.Getenv(EnvMarker) == "1" {
		return ""
	}
	out, err := exec.Command(
		"docker", "ps",
		"--filter", "status=running",
		"--format", "{{.Names}}",
	).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "openneko-") && strings.HasSuffix(line, "-worker-1") {
			return line
		}
	}
	return ""
}

// ProxyToWorker re-executes `openneko <args>` inside the named container,
// forwarding stdin/stdout/stderr and propagating the exit code. The `-t`
// docker flag is added only when stdin is a TTY (so non-interactive flows
// — scripts, CI — don't fail).
func ProxyToWorker(container string, args []string) int {
	dockerArgs := []string{"exec", "-i"}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		dockerArgs = append(dockerArgs, "-t")
	}
	dockerArgs = append(dockerArgs, "-e", EnvMarker+"=1", container, "openneko")
	dockerArgs = append(dockerArgs, args...)
	fmt.Fprintf(os.Stderr, "[openneko] proxying into %s …\n", container)
	cmd := exec.Command("docker", dockerArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ok := errAs(err, &exitErr); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "[openneko] proxy failed: %v\n", err)
		return 1
	}
	return 0
}

// errAs is a tiny errors.As wrapper that avoids the import cycle/double
// import for the one place this package needs it.
func errAs(err error, target **exec.ExitError) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}
