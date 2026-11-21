package cli

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

// confinementDoctorLines prints the Linux security modules, when on Linux, and
// whether agent runners can be confined. It reports a failure when production
// mode cannot confine agent turns, also when an admin turned them on without
// confinement.
func confinementDoctorLines(ctx context.Context, out io.Writer) bool {
	st := runnerAvailable()
	if runtime.GOOS == "linux" && st.Mechanism != "" {
		if lsm, err := confine.LSMList(); err != nil {
			fmt.Fprintf(out, "kernel security modules: cannot read them (%v); run: cat /sys/kernel/security/lsm\n", err)
		} else {
			fmt.Fprintf(out, "kernel security modules: %s\n", lsm)
		}
	}
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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	on, line, err := unconfinedTurnsState(ctx, webAdminSocket())
	switch {
	case err != nil:
		fmt.Fprintf(out, "%s\n  the admin setting for turns without confinement cannot be read: %v\n", turnsOffLine(st), err)
	case on:
		fmt.Fprintf(out, "agent confinement: FAIL, %s (%s)\n", line, st.Reason)
	default:
		fmt.Fprintf(out, "%s\n  %s\n", turnsOffLine(st), unconfinedHint)
	}
	return true
}
