// Package dockerproxy finds the running web container of an OpenNeko
// installation and re-executes a CLI command inside it, where the named
// volumes (secrets, packs) and the web admin socket are. `--local` on the
// root command runs the command on the host instead.
package dockerproxy

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"golang.org/x/term"
)

// EnvMarker is set on the proxied invocation so the inner openneko inside
// the container does not proxy again.
const EnvMarker = "OPENNEKO_PROXIED"

var ErrAmbiguousServices = errors.New("multiple OpenNeko installations are running")

// ResolveRunningService returns the running container of a Compose service.
// With a project it targets that installation; without one it refuses an
// ambiguous match.
func ResolveRunningService(project, service string) (string, error) {
	if os.Getenv(EnvMarker) == "1" {
		return "", nil
	}
	out, err := exec.Command(
		"docker", "ps",
		"--filter", "status=running",
		"--format", "{{.Names}}",
	).Output()
	if err != nil {
		return "", err
	}
	name, ambiguous := selectServiceForProject(strings.Split(strings.TrimSpace(string(out)), "\n"), strings.TrimSpace(project), service)
	if len(ambiguous) > 0 {
		return "", fmt.Errorf("%w (%s); select one with --instance <name> or pass --local", ErrAmbiguousServices, strings.Join(ambiguous, ", "))
	}
	return name, nil
}

// selectServiceForProject picks the single matching container from `docker
// ps` output lines. With more than one match it returns "" plus the sorted
// candidates so the caller can explain the refusal.
func selectServiceForProject(lines []string, project, service string) (string, []string) {
	var matches []string
	suffix := "-" + service + "-1"
	target := ""
	if project != "" {
		target = project + suffix
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if target != "" && line == target {
			return line, nil
		}
		if target == "" && strings.HasPrefix(line, "openneko-") && strings.HasSuffix(line, suffix) {
			matches = append(matches, line)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", matches
	}
}

// ProxyTo re-executes `openneko <args>` inside the named container,
// forwarding stdout/stderr and propagating the exit code.
//
// Stdin is attached only when it can carry useful input: an interactive TTY,
// or a pipe/regular file being redirected in (e.g. `echo val | openneko …`).
// A terminal-less stdin — the common CI/script case where the value is passed
// as an argument — is NOT attached, because `docker exec -i` draining such a
// stream can surface a spurious non-zero exit even when the inner command
// succeeded. `-t` is added only for an interactive TTY.
func ProxyTo(container string, args []string) int {
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
