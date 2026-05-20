package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/host"
	"github.com/open-neko/neko/apps/openneko/internal/plugin/manifest"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check whether this host can run microsandbox and report state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			cwd, _ := os.Getwd()
			h := host.Check()
			supportedTag := "supported"
			if !h.Supported {
				supportedTag = "UNSUPPORTED"
			}
			fmt.Fprintf(out, "host: %s (%s)\n", h.Triple, supportedTag)
			if !h.Supported && h.Reason != "" {
				fmt.Fprintf(out, "  reason: %s\n", h.Reason)
			}
			file := manifest.PathFor(cwd)
			present := false
			pluginCount := 0
			if _, err := os.Stat(file); err == nil {
				present = true
				if m, err := manifest.Read(cwd); err == nil && m != nil {
					pluginCount = len(m.Plugins)
				}
			}
			label := "missing"
			if present {
				label = "found"
			}
			fmt.Fprintf(out, "manifest: %s at %s\n", label, file)
			fmt.Fprintf(out, "plugins:  %d\n", pluginCount)

			dockerVer := detectDocker()
			fmt.Fprintf(out, "docker:   %s\n", dockerVer)

			if other := otherOpennekoOnPath(); other != "" {
				fmt.Fprintf(out, "warning:  duplicate `openneko` on PATH at %s — remove the older one\n", other)
			}

			if !h.Supported {
				return WithExit(1, nil)
			}
			return nil
		},
	}
}

func detectDocker() string {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return "not found"
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return bin + " (version probe failed)"
	}
	return strings.TrimSpace(string(out))
}

// otherOpennekoOnPath returns a second openneko on PATH that isn't this one,
// or "" if there's no duplicate. Useful while operators are transitioning off
// the npm `@open-neko/cli`.
func otherOpennekoOnPath() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	bin, err := exec.LookPath("openneko")
	if err != nil {
		return ""
	}
	if bin == self {
		return ""
	}
	return bin
}
