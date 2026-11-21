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
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// confinementDoctorLines prints the Linux security modules, when on Linux,
// and how agent turns are confined. When serve answers on its socket, it
// prints what serve reports; else what this system can give. It reports a
// failure when agent runners or the network part of serve cannot be
// confined.
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
	fmt.Fprintln(out, turnsOffLine(st))
	return true
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
	if status.TurnsOff != "" {
		fmt.Fprintln(out, status.TurnsOff)
		return true
	}
	if !status.RequireDoor {
		fmt.Fprintln(out, "agent confinement: development mode; serve takes a model key in a frame")
	}
	failed := false
	if !runner.Confined {
		fmt.Fprintf(out, "agent confinement: FAIL, agent runners cannot be confined (%s)\n", runner.Reason)
		failed = true
	}
	if !network.Confined && network.Mechanism != "" {
		fmt.Fprintf(out, "agent confinement: FAIL, the network part of serve runs unconfined (%s)\n", network.Reason)
		failed = true
	}
	if !failed {
		fmt.Fprintf(out, "agent confinement: %s\n", runner)
	}
	return failed
}
