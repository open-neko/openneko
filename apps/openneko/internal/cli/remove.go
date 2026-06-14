package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/plugin/manifest"
)

func newRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a plugin from openneko.plugins.json",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if code, proxied := MaybeProxyToWorker(cmd); proxied {
				return WithExit(code, nil)
			}
			name := args[0]
			if name == "" {
				return WithExit(2, errors.New("remove: package name required"))
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
			if m == nil {
				fmt.Fprintf(out, "%s was not in the manifest\n", name)
				return nil
			}
			before := len(m.Plugins)
			updated := manifest.RemoveByName(*m, name)
			if len(updated.Plugins) == before {
				fmt.Fprintf(out, "%s was not in the manifest\n", name)
				return nil
			}
			if err := manifest.Write(cwd, updated); err != nil {
				return err
			}
			fmt.Fprintf(out, "removed %s\n", name)
			return nil
		},
	}
}
