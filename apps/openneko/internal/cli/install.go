package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/host"
	"github.com/open-neko/neko/apps/openneko/internal/plugin/install"
	"github.com/open-neko/neko/apps/openneko/internal/plugin/marketplace"
	"github.com/open-neko/neko/apps/openneko/internal/plugin/store"
	"github.com/open-neko/neko/apps/openneko/internal/prompt"
)

func newInstallCmd() *cobra.Command {
	var version string
	var unverified bool
	var skipHostCheck bool
	cmd := &cobra.Command{
		Use:   "install <name>[@<marketplace>]",
		Short: "Install a plugin from a trusted marketplace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if code, proxied := MaybeProxyToWorker(cmd); proxied {
				return WithExit(code, nil)
			}
			spec := args[0]
			if spec == "" {
				return WithExit(2, errors.New("install: package name required"))
			}
			errOut := cmd.ErrOrStderr()
			h := host.Check()
			if !h.Supported && !skipHostCheck {
				// Used to be a hard exit. Softened to a warning because
				// install is increasingly decoupled from run: operators
				// stage plugin installs from one host (e.g. Mac) and the
				// VMs actually execute on another (Linux+KVM container,
				// or pnpm-dev on the same Mac). --skip-host-check passed
				// = silence the warning entirely.
				reason := h.Reason
				if reason == "" {
					reason = "(unknown reason)"
				}
				fmt.Fprintf(errOut, "WARNING: host check: %s\n  Install will proceed. Plugin execution requires a host that can spawn microsandbox VMs (Linux with /dev/kvm, or macOS arm64 via pnpm-dev). Pass --skip-host-check to suppress this warning.\n", reason)
			}
			out := cmd.OutOrStdout()
			if unverified {
				fmt.Fprintln(errOut, "WARNING: --unverified bypasses every trusted marketplace. The plugin is not reviewed and its integrity hash is taken on trust from npm. Use only for plugin authoring or emergency hotfixes.")
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			var trusted []install.TrustedMarketplace
			if !unverified {
				s, err := store.Read("")
				if err != nil {
					return err
				}
				for _, m := range s.Marketplaces {
					trusted = append(trusted, install.TrustedMarketplace{Name: m.Name, URL: m.URL})
				}
			}

			res, err := install.Run(context.Background(), install.Options{
				RepoRoot:            cwd,
				Spec:                spec,
				Version:             version,
				Unverified:          unverified,
				TrustedMarketplaces: trusted,
				EnvPrompt:           defaultEnvPrompt(),
			})
			if err != nil {
				return err
			}

			network := "none"
			if len(res.Network) > 0 {
				network = strings.Join(res.Network, ", ")
			}
			provenance := "unverified npm"
			if res.Source == "marketplace" {
				provenance = "marketplace=" + res.Marketplace
			}
			fmt.Fprintf(out, "installed %s@%s (%s) — network: %s\n", res.Name, res.Version, provenance, network)
			if len(res.EnvSaved) > 0 {
				fmt.Fprintf(out, "  saved env: %s\n", strings.Join(res.EnvSaved, ", "))
			}
			if len(res.EnvAlreadySet) > 0 {
				fmt.Fprintf(out, "  env already set: %s\n", strings.Join(res.EnvAlreadySet, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "Pin to a specific version (default: latest non-yanked)")
	cmd.Flags().BoolVar(&unverified, "unverified", false, "Bypass trusted marketplaces and install directly from npm")
	cmd.Flags().BoolVar(&skipHostCheck, "skip-host-check", false, "Silence the host-compatibility warning when installing on a host that can't execute plugin VMs (Mac+docker, Linux without KVM, etc.)")
	return cmd
}

func defaultEnvPrompt() install.EnvPromptFunc {
	return func(plugin string, req marketplace.EnvRequirement) (string, error) {
		if !prompt.IsInteractive() {
			return "", fmt.Errorf(`install: required env %q for %s is not set and stdin is not a TTY.\nRun: openneko secrets set %s %s <value>`, req.Key, plugin, plugin, req.Key)
		}
		fmt.Fprintf(os.Stdout, "\n%s requires %s\n  %s\n", plugin, req.Key, req.Description)
		hidden := req.Secret == nil || *req.Secret
		if hidden {
			return prompt.Hidden(req.Key + " (hidden): ")
		}
		return prompt.Visible("value: ")
	}
}
