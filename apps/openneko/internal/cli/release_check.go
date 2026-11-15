package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

func newReleaseCheckCmd() *cobra.Command {
	var scheduler bool
	cmd := &cobra.Command{
		Use:    "release-check",
		Short:  "Check a running web from inside its container (release pipelines)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !scheduler {
				return errors.New("name a check: --scheduler")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			if err := checkScheduler(ctx, webAdminSocket()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "scheduler=ok")
			return nil
		},
	}
	cmd.Flags().BoolVar(&scheduler, "scheduler", false, "The workflow scheduler finished a database tick since this web started")
	return cmd
}

func checkScheduler(ctx context.Context, socket string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://web/health/scheduler", nil)
	if err != nil {
		return err
	}
	res, err := webAdminClient(socket).Do(req)
	if err != nil {
		return webAdminUnreachable(socket, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	var health struct {
		Status        string  `json:"status"`
		LastSuccessAt *string `json:"lastSuccessAt"`
	}
	if err := json.Unmarshal(raw, &health); err != nil || res.StatusCode != http.StatusOK || health.Status != "ok" || health.LastSuccessAt == nil {
		return fmt.Errorf("the workflow scheduler is not healthy (HTTP %d): %s", res.StatusCode, raw)
	}
	return nil
}
