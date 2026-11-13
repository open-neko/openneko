package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
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
			if err := convertConfigRepos(ctx, out, false); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "config repository not moved: %v\n", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&skipSchema, "skip-schema", false, "Skip schema migrations")
	cmd.AddCommand(newMigrateConfigRepoCmd())
	return cmd
}

func newMigrateConfigRepoCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "config-repo",
		Short: "Move each org folder off its old config repository into plain folders",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return convertConfigRepos(cmd.Context(), cmd.OutOrStdout(), dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List what the step would write and change nothing")
	return cmd
}

// convertConfigRepos works with no database too: the member handles then
// come from the folder names, and no audit row is written.
func convertConfigRepos(ctx context.Context, out io.Writer, dryRun bool) error {
	api := &webfront.API{}
	if target, err := defaultConn(); err == nil {
		if pool, err := db.Open(ctx, target, "openneko migrate config-repo", nil); err == nil {
			defer pool.Close()
			api.DB = pool
		}
	}
	results, err := api.ConvertConfigRepos(ctx, dryRun)
	reportConfigRepos(out, results, dryRun)
	return err
}

func reportConfigRepos(out io.Writer, results []webfront.PlainResult, dryRun bool) {
	for _, r := range results {
		verb := "moved"
		if dryRun {
			verb = "would move"
		}
		fmt.Fprintf(out, "config repository %s: %s\n", verb, r.Repo)
		for _, a := range r.Actions {
			line := "  " + a.Kind + " " + a.Path
			if a.Note != "" {
				line += " (" + a.Note + ")"
			}
			fmt.Fprintln(out, line)
		}
		if !dryRun {
			fmt.Fprintf(out, "the old repository is at %s; delete it when you no longer need it\n", r.Moved)
		}
	}
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
