package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// confinementDoctorLines prints the Linux security modules, when on Linux,
// and how agent turns are confined. When serve answers on its socket, it
// prints what serve reports; else what this system can give. It reports a
// failure when production mode cannot confine agent turns, also when an
// admin turned them on without confinement.
func confinementDoctorLines(ctx context.Context, out io.Writer) bool {
	st := runnerAvailable()
	if runtime.GOOS == "linux" && st.Mechanism != "" {
		if lsm, err := confine.LSMList(); err != nil {
			fmt.Fprintf(out, "kernel security modules: cannot read them (%v); run: cat /sys/kernel/security/lsm\n", err)
		} else {
			fmt.Fprintf(out, "kernel security modules: %s\n", lsm)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	socket := doctorServeSocket()
	status, err := serveStatus(ctx, socket)
	if err == nil {
		return serveConfinementLines(ctx, out, socket, status)
	}
	fmt.Fprintf(out, "openneko serve: no answer (%v); this system can give:\n", err)
	if st.Confined {
		fmt.Fprintf(out, "agent confinement: %s, on %s\n", st, confine.Kernel())
		if len(st.Gaps) > 0 {
			fmt.Fprintf(out, "  this system cannot refuse: %s\n", strings.Join(st.Gaps, "; "))
		}
		return false
	}
	if webfront.Development() {
		fmt.Fprintf(out, "%s (%s); agent turns run unconfined in development mode\n", confine.NotAvailable, st.Reason)
		return false
	}
	return turnsOffDoctorLines(ctx, out, turnsOffLine(st), st.Reason)
}

// doctorServeSocket is OPENNEKO_SERVE_SOCKET, or the serve socket of this
// native install.
func doctorServeSocket() string {
	if v := strings.TrimSpace(os.Getenv("OPENNEKO_SERVE_SOCKET")); v != "" {
		return v
	}
	return native.ServeEnv(nativeLayout())["OPENNEKO_SERVE_SOCKET"]
}

// serveConfinementLines prints how the running serve confines its runners
// and its two parts.
func serveConfinementLines(ctx context.Context, out io.Writer, socket string, status serve.Status) bool {
	host, network, runner, err := statusStates(socket, status)
	if err != nil {
		fmt.Fprintf(out, "agent confinement: FAIL, %v\n", err)
		return true
	}
	where := status.Kernel
	if where == "" {
		where = confine.Kernel()
	}
	fmt.Fprintf(out, "openneko serve answers on %s, on %s\n", socket, where)
	part := func(name string, st confine.State) {
		fmt.Fprintf(out, "  %s: %s\n", name, st)
		if st.Confined && len(st.Gaps) > 0 {
			fmt.Fprintf(out, "    cannot refuse: %s\n", strings.Join(st.Gaps, "; "))
		}
	}
	part("agent turns", runner)
	part("network part of serve", network)
	part("turn part of serve", host)
	if !status.RequireDoor {
		fmt.Fprintln(out, "agent confinement: development mode; serve does not hold agent turns to production rules")
		return false
	}
	if status.TurnsOff != "" {
		return turnsOffDoctorLines(ctx, out, status.TurnsOff, runner.Reason)
	}
	failed := false
	if !runner.Confined {
		fmt.Fprintf(out, "agent confinement: FAIL, agent turns run unconfined in production mode (%s)\n", runner.Reason)
		failed = true
	}
	if !network.Confined && network.Mechanism != "" {
		fmt.Fprintf(out, "agent confinement: FAIL, the network part of serve runs unconfined in production mode (%s)\n", network.Reason)
		failed = true
	}
	if !failed {
		fmt.Fprintf(out, "agent confinement: %s\n", runner)
	}
	return failed
}

// turnsOffDoctorLines prints why agent turns are off and the host command
// that allows them without confinement.
func turnsOffDoctorLines(ctx context.Context, out io.Writer, line, reason string) bool {
	on, admin, err := unconfinedTurnsState(ctx, webAdminSocket())
	switch {
	case err != nil:
		fmt.Fprintf(out, "%s\n  the override for turns without confinement cannot be read: %v\n", line, err)
	case on:
		fmt.Fprintf(out, "agent confinement: FAIL, %s (%s)\n", admin, reason)
	case admin != "":
		fmt.Fprintf(out, "%s\n  the override for turns without confinement does not apply: %s\n  %s\n", line, admin, unconfinedHint)
	default:
		fmt.Fprintf(out, "%s\n  %s\n", line, unconfinedHint)
	}
	return true
}
