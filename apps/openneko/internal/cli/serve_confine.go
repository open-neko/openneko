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
)

const (
	serveConfineEnv = "OPENNEKO_SERVE_CONFINE"
)

type serveFolders struct {
	dsn, files, index, agentRuns, subsDir, subsURLFile, gjConfig, duckdb string
	agentSocket, checkSocket, sandboxURL                                 string
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

// serveSpec keeps what serve opens in a turn, a host call, a GraphJin query,
// a file tool and a sandbox call, and nothing else.
func serveSpec(f serveFolders) confine.Spec {
	var spec confine.Spec
	if exe, err := os.Executable(); err == nil {
		x, r := confine.Program(exe)
		spec.Exec, spec.Read = append(spec.Exec, x...), append(spec.Read, r...)
	}
	if f.duckdb != "" {
		if path, err := exec.LookPath(f.duckdb); err == nil {
			if abs, err := filepath.Abs(path); err == nil {
				x, r := confine.Program(abs)
				spec.Exec, spec.Read = append(spec.Exec, x...), append(spec.Read, r...)
			}
		}
	}
	spec.Read = append(spec.Read, serveSystemReads...)
	for _, env := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR"} {
		for _, p := range filepath.SplitList(os.Getenv(env)) {
			spec.Read = append(spec.Read, p)
		}
	}
	spec.Read = append(spec.Read, postgresCertPaths(f.dsn)...)
	if f.subsURLFile != "" {
		spec.Read = append(spec.Read, f.subsURLFile)
	}
	spec.ReadWrite = []string{os.DevNull, os.TempDir(), f.files, f.index, f.agentRuns, f.subsDir}
	if f.gjConfig != "" {
		spec.ReadWrite = append(spec.ReadWrite, filepath.Dir(f.gjConfig))
	}
	for _, s := range []string{f.agentSocket, f.checkSocket} {
		if s != "" {
			spec.ReadWrite = append(spec.ReadWrite, filepath.Dir(s))
		}
	}
	if door := keydoor.SocketPath(os.Getenv, f.agentSocket); door != "" {
		spec.Sockets = append(spec.Sockets, door)
	}
	if path, ok := strings.CutPrefix(f.sandboxURL, "unix://"); ok {
		spec.Sockets = append(spec.Sockets, path)
	}
	return spec
}

// confineServe confines serve for good. When the kernel cannot, serve starts
// unconfined and says so, and runnerConfinement refuses every agent turn;
// host calls, data reads and the rest still run. Production refuses to start
// only when a kernel that can confine refuses serve's rules.
func confineServe(out io.Writer, f serveFolders, development bool) (confine.State, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(serveConfineEnv)), "off") {
		if !development {
			return confine.State{}, fmt.Errorf("%s=off works only with OPENNEKO_ENV=development; remove it", serveConfineEnv)
		}
		st := confine.State{Reason: serveConfineEnv + "=off in development mode"}
		fmt.Fprintf(out, "warning: openneko serve runs unconfined: %s\n", st.Reason)
		return st, nil
	}
	for _, dir := range []string{os.TempDir(), f.files, f.index, f.agentRuns, f.subsDir} {
		if dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return confine.State{}, err
			}
		}
	}
	st := serveAvailable()
	var err error
	if st.Confined {
		st, err = confine.Apply(serveSpec(f))
	} else {
		err = fmt.Errorf("%w: %s", confine.ErrUnsupported, st.Reason)
	}
	if err == nil {
		fmt.Fprintf(out, "openneko serve is confined: %s\n", st)
		if len(st.Gaps) > 0 {
			fmt.Fprintf(out, "confinement gaps on this kernel: %s\n", strings.Join(st.Gaps, "; "))
		}
		return st, nil
	}
	if !errors.Is(err, confine.ErrUnsupported) && st.Mechanism != "" && !development {
		return st, fmt.Errorf("openneko serve cannot confine itself: %w", err)
	}
	st.Confined, st.Reason = false, err.Error()
	fmt.Fprintf(out, "warning: openneko serve runs unconfined: %s\n", st.Reason)
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
	case st.Mechanism == "":
		fmt.Fprintf(out, "warning: %s (%s); agent turns run unconfined\n", confine.NotAvailable, st.Reason)
		return st, false, ""
	case development:
		fmt.Fprintf(out, "warning: agent runners run unconfined in development mode: %s\n", st.Reason)
		return st, false, ""
	}
	turnsOff = turnsOffLine(st)
	fmt.Fprintln(out, turnsOff)
	return st, false, turnsOff
}

// turnsOffLine is the one line that the start, the status page, doctor and
// each refused turn show when agent runners cannot be confined.
func turnsOffLine(st confine.State) string {
	return fmt.Sprintf("agent confinement: off. %s, so agent turns are off", st.Reason)
}

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
