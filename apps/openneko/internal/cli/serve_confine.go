package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

const (
	serveConfineEnv = "OPENNEKO_SERVE_CONFINE"
)

type serveFolders struct {
	dsn, files, index, agentRuns, subsDir, subsURLFile, gjConfig, duckdb string
	agentSocket, checkSocket, sandboxURL, gjSocket, subsSocket           string
}

// serveSystemReads holds /proc because DuckDB reads its own /proc/self; Landlock
// still refuses mem, environ and ptrace of a process outside serve's domain.
var serveSystemReads = []string{
	"/etc/ssl", "/etc/pki", "/etc/ca-certificates", "/usr/share/ca-certificates", "/usr/local/share/ca-certificates",
	"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/gai.conf", "/etc/host.conf",
	"/etc/services", "/etc/protocols", "/etc/localtime", "/usr/share/zoneinfo", "/etc/mime.types",
	"/dev/urandom", "/dev/random", "/sys/fs/cgroup", "/sys/kernel/mm/transparent_hugepage", "/sys/devices/system/cpu",
	"/proc",
}

// programSpec lets a confined part run this program and the DuckDB CLI.
func programSpec(duckdb string) confine.Spec {
	var spec confine.Spec
	if exe, err := os.Executable(); err == nil {
		x, r := confine.Program(exe)
		spec.Exec, spec.Read = append(spec.Exec, x...), append(spec.Read, r...)
	}
	if duckdb != "" {
		if path, err := exec.LookPath(duckdb); err == nil {
			if abs, err := filepath.Abs(path); err == nil {
				x, r := confine.Program(abs)
				spec.Exec, spec.Read = append(spec.Exec, x...), append(spec.Read, r...)
			}
		}
	}
	spec.Read = append(spec.Read, serveSystemReads...)
	for _, env := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR"} {
		spec.Read = append(spec.Read, filepath.SplitList(os.Getenv(env))...)
	}
	return spec
}

// serveSpec is the network part of serve: the files service, the customer
// and subscriptions GraphJin and the config check. It keeps no agent run,
// no key door and no credentials folder.
func serveSpec(f serveFolders) confine.Spec {
	spec := programSpec(f.duckdb)
	spec.Read = append(spec.Read, postgresCertPaths(f.dsn)...)
	spec.ReadWrite = []string{os.DevNull, os.TempDir(), f.files, f.index, f.subsDir}
	if f.gjConfig != "" {
		spec.ReadWrite = append(spec.ReadWrite, filepath.Dir(f.gjConfig))
	}
	for _, s := range []string{f.checkSocket, f.gjSocket, f.subsSocket} {
		if s != "" {
			spec.Listen = append(spec.Listen, s)
		}
	}
	return spec
}

// hostSpec is the part of serve that starts the agent runners: their run
// folders, its socket, the key door and the sandbox socket.
func hostSpec(f serveFolders) confine.Spec {
	spec := programSpec("")
	spec.ReadWrite = []string{os.DevNull, os.TempDir(), f.agentRuns}
	if f.agentSocket != "" {
		spec.Listen = append(spec.Listen, f.agentSocket)
	}
	if door := keydoor.SocketPath(os.Getenv, f.agentSocket); door != "" {
		spec.Sockets = append(spec.Sockets, door)
	}
	if path, ok := sandbox.SocketPath(f.sandboxURL); ok && path != "" {
		spec.Sockets = append(spec.Sockets, path)
	}
	return spec
}

// confineTurnHost confines the part of serve that starts agent runners where
// the OS lets a confined process start a runner that confines itself.
func confineTurnHost(out io.Writer, f serveFolders, development bool) (confine.State, error) {
	if !confine.Stacks {
		return confine.State{Reason: fmt.Sprintf("on %s the agent runners confine themselves, and a confined process cannot start them", confine.Kernel())}, nil
	}
	return confinePart(out, "the agent turn part of openneko serve", hostSpec(f), []string{os.TempDir(), f.agentRuns}, development)
}

// confineServe confines the network part of serve for good. When the system
// cannot, it starts unconfined and says so, and runnerConfinement refuses
// every agent turn; host calls, data reads and the rest still run.
// Production refuses to start only when a system that can confine refuses
// the rules.
func confineServe(out io.Writer, f serveFolders, development bool) (confine.State, error) {
	return confinePart(out, "the network part of openneko serve", serveSpec(f), []string{os.TempDir(), f.files, f.index, f.subsDir}, development)
}

// confinePart confines a part of serve for good. Production refuses to start
// when the system has the confinement and fails to give it; development
// starts unconfined and says so.
func confinePart(out io.Writer, name string, spec confine.Spec, dirs []string, development bool) (confine.State, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(serveConfineEnv)), "off") {
		if !development {
			return confine.State{}, fmt.Errorf("%s=off works only with OPENNEKO_ENV=development; remove it", serveConfineEnv)
		}
		st := confine.State{Reason: serveConfineEnv + "=off in development mode"}
		fmt.Fprintf(out, "warning: %s runs unconfined: %s\n", name, st.Reason)
		return st, nil
	}
	for _, dir := range dirs {
		if dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return confine.State{}, err
			}
		}
	}
	st := serveAvailable()
	var err error
	if st.Confined {
		st, err = confine.Apply(spec)
	} else {
		err = fmt.Errorf("%w: %s", confine.ErrUnsupported, st.Reason)
	}
	if err == nil {
		fmt.Fprintf(out, "%s is confined: %s\n", name, st)
		if len(st.Gaps) > 0 {
			fmt.Fprintf(out, "confinement gaps on this kernel: %s\n", strings.Join(st.Gaps, "; "))
		}
		return st, nil
	}
	if !errors.Is(err, confine.ErrUnsupported) && st.Mechanism != "" && !development {
		return st, fmt.Errorf("%s cannot confine itself: %w", name, err)
	}
	st.Confined, st.Reason = false, err.Error()
	fmt.Fprintf(out, "warning: %s runs unconfined: %s\n", name, st.Reason)
	return st, nil
}

var (
	serveAvailable  = confine.Available
	runnerAvailable = confine.RunnerAvailable
)

// runnerConfinement tells how each agent runner will be confined. On Linux
// in production mode a runner that cannot be confined runs no turn: turnsOff
// then holds the line that each refused turn shows.
func runnerConfinement(out io.Writer, development bool) (st confine.State, confineRunner bool, turnsOff string) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(serveConfineEnv)), "off") {
		st = confine.State{Reason: serveConfineEnv + "=off in development mode"}
		fmt.Fprintf(out, "warning: agent runners run unconfined: %s\n", st.Reason)
		return st, false, ""
	}
	st = runnerAvailable()
	switch {
	case st.Confined:
		fmt.Fprintf(out, "each agent runner confines itself: %s\n", st)
		if len(st.Gaps) > 0 {
			fmt.Fprintf(out, "agent runner gaps on this system: %s\n", strings.Join(st.Gaps, "; "))
		}
		return st, true, ""
	case st.Mechanism == "" && development:
		fmt.Fprintf(out, "warning: %s (%s); agent turns run unconfined in development mode\n", confine.NotAvailable, st.Reason)
		return st, false, ""
	case development:
		fmt.Fprintf(out, "warning: agent runners run unconfined in development mode: %s\n", st.Reason)
		return st, false, ""
	}
	turnsOff = turnsOffLine(st)
	fmt.Fprintln(out, turnsOff)
	fmt.Fprintln(out, unconfinedHint)
	return st, false, turnsOff
}

// turnsOffLine is the one line that the start, the status page, doctor and
// each refused turn show when agent runners cannot be confined.
func turnsOffLine(st confine.State) string {
	if st.Mechanism == "" {
		return fmt.Sprintf("%s (%s), so agent turns are off", confine.NotAvailable, st.Reason)
	}
	return fmt.Sprintf("agent confinement: off. %s, so agent turns are off", st.Reason)
}

const unconfinedHint = `an admin can run agent turns without OS confinement on this system: Admin, Settings, Agent, "Run agent turns without OS confinement on this system" (INSTALL.md, "Agent confinement")`

// postgresCertPaths lists where the Postgres client looks for TLS files: the
// URL, the PG variables and ~/.postgresql.
func postgresCertPaths(dsn string) []string {
	var out []string
	if u, err := url.Parse(dsn); err == nil {
		for _, k := range []string{"sslrootcert", "sslcert", "sslkey"} {
			if v := u.Query().Get(k); v != "" && v != "system" {
				out = append(out, v)
			}
		}
	}
	for _, k := range []string{"PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, v)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".postgresql"))
	}
	return out
}
