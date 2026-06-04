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
	"errors"
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
// forwarding stdout/stderr and propagating the exit code.
//
// Stdin is attached only when it can carry useful input: an interactive TTY,
// or a pipe/regular file being redirected in (e.g. `echo val | openneko …`).
// A terminal-less stdin — the common CI/script case where the value is passed
// as an argument — is NOT attached, because `docker exec -i` draining such a
// stream can surface a spurious non-zero exit even when the inner command
// succeeded. `-t` is added only for an interactive TTY.
func ProxyToWorker(container string, args []string) int {
	isTTY := term.IsTerminal(int(os.Stdin.Fd()))
	attachStdin := isTTY
	if !isTTY {
		if fi, err := os.Stdin.Stat(); err == nil {
			attachStdin = shouldAttachStdin(false, fi.Mode())
		}
	}
	dockerArgs := dockerExecArgs(container, args, attachStdin, isTTY)
	fmt.Fprintf(os.Stderr, "[openneko] proxying into %s …\n", container)
	cmd := exec.Command("docker", dockerArgs...)
	if attachStdin {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "[openneko] proxy failed: %v\n", err)
		return 1
	}
	return 0
}

// shouldAttachStdin decides whether to forward stdin to `docker exec`: yes for
// an interactive TTY or redirected pipe/regular-file input; no for a
// terminal-less device (e.g. /dev/null, an inherited non-tty).
func shouldAttachStdin(isTTY bool, mode os.FileMode) bool {
	if isTTY {
		return true
	}
	return mode&os.ModeNamedPipe != 0 || mode.IsRegular()
}

func dockerExecArgs(container string, args []string, attachStdin, isTTY bool) []string {
	out := []string{"exec"}
	if attachStdin {
		out = append(out, "-i")
	}
	if isTTY {
		out = append(out, "-t")
	}
	out = append(out, "-e", EnvMarker+"=1", container, "openneko")
	return append(out, args...)
}
