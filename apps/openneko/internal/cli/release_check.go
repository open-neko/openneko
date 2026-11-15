package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

func newReleaseCheckCmd() *cobra.Command {
	var scheduler, serveSocket bool
	var datasourceOrg, mcpURL, query string
	cmd := &cobra.Command{
		Use:    "release-check",
		Short:  "Check a running web from inside its container (release pipelines)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !scheduler && !serveSocket && datasourceOrg == "" {
				return errors.New("name a check: --scheduler, --datasource ORG or --serve-socket")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			out := cmd.OutOrStdout()
			if scheduler {
				if err := checkScheduler(ctx, webAdminSocket()); err != nil {
					return err
				}
				fmt.Fprintln(out, "scheduler=ok")
			}
			if datasourceOrg != "" {
				if err := webfront.DatasourceQueryCheck(ctx, config.Dir(""), mcpURL, datasourceOrg, query); err != nil {
					return fmt.Errorf("authenticated datasource query: %w", err)
				}
				fmt.Fprintln(out, "authenticated_datasource_query=ok")
			}
			if serveSocket {
				if err := checkServeSocket(ctx, os.Getenv("OPENNEKO_SERVE_SOCKET")); err != nil {
					return err
				}
				fmt.Fprintln(out, "serve_socket=ok")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&scheduler, "scheduler", false, "The workflow scheduler finished a database tick since this web started")
	f.StringVar(&datasourceOrg, "datasource", "", "Run --query through GraphJin as this org's service role and require a row")
	f.StringVar(&mcpURL, "mcp-url", "http://graphjin:8080/api/v1/mcp", "GraphJin MCP endpoint for --datasource")
	f.StringVar(&query, "query", "query { soh(limit: 1, order_by: { id: desc }) { id } }", "GraphQL query for --datasource")
	f.BoolVar(&serveSocket, "serve-socket", false, "Connect to the agent socket of openneko serve (OPENNEKO_SERVE_SOCKET)")
	return cmd
}

func checkServeSocket(ctx context.Context, socket string) error {
	if socket == "" {
		return errors.New("OPENNEKO_SERVE_SOCKET is not set")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("serve socket %s: %w", socket, err)
	}
	return conn.Close()
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
