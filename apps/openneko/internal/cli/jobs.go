package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

var jobRunKinds = []string{jobs.KindCronSweep}

func newJobsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "Run one background job body by hand",
	}
	cmd.AddCommand(newJobsRunCmd())
	return cmd
}

func newJobsRunCmd() *cobra.Command {
	var orgID, at string
	cmd := &cobra.Command{
		Use:   "run <kind>",
		Short: "Run one job body once for an organization and print the result as JSON",
		Long: `Runs one job body once, in this process, with the main database login,
and prints what it did as JSON. It does not wait for the queue and does
not stop the scheduled job.

Kinds:
  workflow_cron_sweep  the behavior thresholds, then the condition watchers.
                       A watcher that fires queues its workflow on the
                       River workflow_run_fire job, as the minute job does.

--at sets the clock of the job (RFC 3339, default now). Use it to rerun a
sweep for a past minute.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := args[0]
			if !slices.Contains(jobRunKinds, kind) {
				return fmt.Errorf("openneko jobs run can run %s; %q is not one of them", strings.Join(jobRunKinds, ", "), kind)
			}
			if strings.TrimSpace(orgID) == "" {
				return fmt.Errorf("--org is required")
			}
			now := time.Now()
			if at != "" {
				t, err := time.Parse(time.RFC3339Nano, at)
				if err != nil {
					return fmt.Errorf("--at: %w", err)
				}
				now = t
			}
			ctx := cmd.Context()
			conn, err := defaultConn()
			if err != nil {
				return err
			}
			pool, err := db.Open(ctx, conn, "openneko jobs run", defaultConn)
			if err != nil {
				return err
			}
			defer pool.Close()
			inserter, err := jobs.NewInserter(pool, "")
			if err != nil {
				return err
			}
			api := &webfront.API{Gate: &webfront.Gate{WorkerURL: envOr("WORKER_ADMIN_URL", "http://127.0.0.1:4100"), DB: pool}, DB: pool, Jobs: inserter}
			res, runErr := api.CronSweepAt(ctx, orgID, now)
			if runErr != nil && res.At == "" {
				return runErr
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(res); err != nil {
				return err
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&at, "at", "", "Clock of the job, RFC 3339 (default now)")
	return cmd
}
