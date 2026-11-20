package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
)

// confinementDoctorLines prints the active Linux security modules and
// whether agent runners can be confined. It reports a failure when this
// system has a confinement and cannot give it.
func confinementDoctorLines(out io.Writer) bool {
	st := runnerAvailable()
	if st.Mechanism == "" {
		fmt.Fprintf(out, "agent confinement: %s\n", st.Reason)
		return false
	}
	if lsm, err := confine.LSMList(); err != nil {
		fmt.Fprintf(out, "kernel security modules: cannot read them (%v); run: cat /sys/kernel/security/lsm\n", err)
	} else {
		fmt.Fprintf(out, "kernel security modules: %s\n", lsm)
	}
	if !st.Confined {
		fmt.Fprintf(out, "%s\n", turnsOffLine(st))
		return true
	}
	fmt.Fprintf(out, "agent confinement: %s, kernel %s\n", st, confine.Kernel())
	if len(st.Gaps) > 0 {
		fmt.Fprintf(out, "  this kernel cannot refuse: %s\n", strings.Join(st.Gaps, "; "))
	}
	return false
}
