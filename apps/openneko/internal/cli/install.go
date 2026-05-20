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
	cmd := &cobra.Command{
		Use:   "install <name>[@<marketplace>]",
		Short: "Install a plugin from a trusted marketplace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := args[0]
			if spec == "" {
				return WithExit(2, errors.New("install: package name required"))
			}
			h := host.Check()
			if !h.Supported && !unverified {
				reason := h.Reason
				if reason == "" {
					reason = "(unknown reason)"
				}
				return WithExit(3, fmt.Errorf("host not supported: %s\nIf you understand and want to install anyway, re-run with --unverified.", reason))
			}
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
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
