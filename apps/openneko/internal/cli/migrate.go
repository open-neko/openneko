package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

func newMigrateCmd() *cobra.Command {
	var skipSchema bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply neko-db schema migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			skip := skipSchema || envTruthy("OPENNEKO_SKIP_MIGRATE")
			if !skip {
				target, err := defaultConn()
				if err != nil {
					return err
				}
				conn, err := db.Connect(ctx, target, "openneko migrate")
				if err != nil {
					return err
				}
				pool, err := db.Open(ctx, target, "openneko migrate", nil)
				if err != nil {
					_ = conn.Close(ctx)
					return err
				}
				logf := func(format string, args ...any) {
					fmt.Fprintf(out, format+"\n", args...)
				}
				queue := &jobs.Queue{Pool: pool, Logf: logf}
				mig := &db.Migrator{FS: assets.MigrationsFS, Dir: "migrations", After: queue.Migrate}
				ran, migrateErr := mig.Apply(ctx, conn, logf)
				pool.Close()
				_ = conn.Close(ctx)
				if migrateErr != nil {
					return migrateErr
				}
				fmt.Fprintf(out, "%d migration(s) applied\n", ran)
			} else {
				fmt.Fprintln(out, "schema migrations skipped")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&skipSchema, "skip-schema", false, "Skip schema migrations")
	return cmd
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
