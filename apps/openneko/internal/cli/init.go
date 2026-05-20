package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/plugin/manifest"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create an empty openneko.plugins.json in the current directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			file := manifest.PathFor(cwd)
			if _, err := os.Stat(file); err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s already exists\n", file)
				return nil
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err := manifest.Write(cwd, manifest.Empty()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", file)
			return nil
		},
	}
}
