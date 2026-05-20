package cli

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
)

func newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending SQL migrations to neko-db",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			conn, err := pgx.Connect(ctx, defaultConn().DSN())
			if err != nil {
				return err
			}
			defer conn.Close(ctx)
			mig := &db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}
			out := cmd.OutOrStdout()
			ran, err := mig.Apply(ctx, conn, func(format string, args ...any) {
				fmt.Fprintf(out, format+"\n", args...)
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%d migration(s) applied\n", ran)
			_ = context.Canceled
			return nil
		},
	}
}
