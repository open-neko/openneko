package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/plugin/manifest"
)

func newListCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show plugins listed in the project's openneko.plugins.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if code, proxied := MaybeProxyToWorker(cmd); proxied {
				return WithExit(code, nil)
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			m, err := manifest.Read(cwd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if output == "json" {
				if m == nil {
					fmt.Fprintln(out, "[]")
					return nil
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(m.Plugins)
			}
			if m == nil {
				fmt.Fprintln(out, "no openneko.plugins.json — run `openneko init` to create one")
				return nil
			}
			if len(m.Plugins) == 0 {
				fmt.Fprintln(out, "no plugins installed")
				return nil
			}
			for _, e := range m.Plugins {
				hosts := "no network"
				if len(e.Permissions.Network) > 0 {
					hosts = strings.Join(e.Permissions.Network, ", ")
				}
				flags := ""
				if e.Capabilities.Auth != nil {
					flags = "  [SSO provider]"
				}
				from := ""
				if e.Marketplace != "" {
					from = "  from=" + e.Marketplace
				}
				fmt.Fprintf(out, "%s@%s  [%s]%s%s\n", e.Name, e.Version, hosts, flags, from)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text|json")
	return cmd
}
