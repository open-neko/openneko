package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/host"
	"github.com/open-neko/neko/apps/openneko/internal/plugin/manifest"
	"github.com/open-neko/neko/apps/openneko/internal/preflight"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check whether this host can run the OpenShell sandbox runtime and report state",
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

			docker := preflight.Docker()
			fmt.Fprintf(out, "docker:   %s\n", docker.Detail)
			if docker.Level == preflight.Fail && docker.Remediation != "" {
				fmt.Fprintf(out, "  fix: %s\n", docker.Remediation)
			}

			if dup := preflight.DuplicateBinary(); dup.Level == preflight.Warn {
				fmt.Fprintf(out, "warning:  %s — %s\n", dup.Detail, dup.Remediation)
			}

			if !h.Supported {
				return WithExit(1, nil)
			}
			return nil
		},
	}
}
