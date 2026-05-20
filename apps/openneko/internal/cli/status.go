package cli

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/compose"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show service health for the running stack",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sup := compose.New(assets.ComposeFS)
			files, err := sup.Materialize(compose.ModeDemo)
			if err != nil {
				return err
			}
			project, err := sup.ProjectName("")
			if err != nil {
				return err
			}
			code, err := sup.Run(context.Background(), project, files, []string{"ps"}, os.Stdout, os.Stderr)
			if err != nil {
				return err
			}
			if code != 0 {
				return WithExit(code, nil)
			}
			return nil
		},
	}
}
