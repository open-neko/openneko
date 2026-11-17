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
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func newReleaseCheckCmd() *cobra.Command {
	var scheduler, serveSocket, packsCheck, sandboxCheck, skillsCheck, bundledPacks bool
	var datasourceOrg, mcpURL, query string
	cmd := &cobra.Command{
		Use:    "release-check",
		Short:  "Check a running web from inside its container (release pipelines)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !scheduler && !serveSocket && !packsCheck && !sandboxCheck && !skillsCheck && !bundledPacks && datasourceOrg == "" {
				return errors.New("name a check: --scheduler, --datasource ORG, --serve-socket, --sandbox, --packs, --bundled-packs or --skills")
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
			if packsCheck {
				n, err := checkBuiltinPacks(ctx, webAdminSocket())
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "builtin_packs=%d\n", n)
			}
			if bundledPacks {
				n, err := webfront.BundledPacksCheck()
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "bundled_packs=%d\n", n)
			}
			if skillsCheck {
				names, err := webfront.BundledSkillsCheck()
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "bundled_skills=%d\n", len(names))
			}
			if serveSocket {
				if err := checkServeSocket(ctx, os.Getenv("OPENNEKO_SERVE_SOCKET")); err != nil {
					return err
				}
				fmt.Fprintln(out, "serve_socket=ok")
			}
			if sandboxCheck {
				state, err := checkSandboxSocket(ctx, os.Getenv("OPENNEKO_SANDBOX_URL"), os.Getenv("OPENNEKO_SERVE_SOCKET"), os.Getenv("OPENNEKO_CONFIG_CHECK_SOCKET"))
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "sandbox=%s\n", state)
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
	f.BoolVar(&sandboxCheck, "sandbox", false, "The sandbox is off, or its socket answers in a folder that holds none of serve's sockets")
	f.BoolVar(&packsCheck, "packs", false, "The pack catalog on the web admin socket lists one or more built-in packs")
	f.BoolVar(&bundledPacks, "bundled-packs", false, "This executable finds one or more built-in packs, with no running web")
	f.BoolVar(&skillsCheck, "skills", false, "This executable finds every sandbox skill and one or more built-in skills")
	return cmd
}

func checkBuiltinPacks(ctx context.Context, socket string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://web/admin/packs", nil)
	if err != nil {
		return 0, err
	}
	res, err := webAdminClient(socket).Do(req)
	if err != nil {
		return 0, webAdminUnreachable(socket, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	var list struct {
		Packs []struct {
			Source string `json:"source"`
		} `json:"packs"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the pack catalog did not answer (HTTP %d): %s", res.StatusCode, raw)
	}
	n := 0
	for _, p := range list.Packs {
		if p.Source == "embedded" {
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("the pack catalog lists no built-in pack: %s", raw)
	}
	return n, nil
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

func checkSandboxSocket(ctx context.Context, url string, stackSockets ...string) (string, error) {
	box := sandbox.Config{URL: url}
	if !box.Enabled() {
		return "off", nil
	}
	if box.SharesFolder(stackSockets...) {
		return "", fmt.Errorf("the sandbox socket %s is in the folder of serve's sockets; give the sandbox a socket folder of its own (INSTALL.md, \"Sandbox\")", url)
	}
	path, ok := strings.CutPrefix(url, "unix://")
	if !ok {
		return "remote", nil
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return "", fmt.Errorf("sandbox socket %s: %w", path, err)
	}
	return "ok", conn.Close()
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

// bundledAssetsCheck stops a production web that finds no bundled skills or
// packs; development walks to a source checkout instead.
func bundledAssetsCheck() error {
	if webfront.Development() {
		return nil
	}
	const fix = "reinstall the release, or set OPENNEKO_BUILTIN_SKILLS_ROOT, OPENNEKO_SANDBOX_SKILLS_ROOT and OPENNEKO_PACKS_DIR"
	if _, err := webfront.BundledSkillsCheck(); err != nil {
		return fmt.Errorf("openneko web cannot start: %w; %s", err, fix)
	}
	if _, err := webfront.BundledPacksCheck(); err != nil {
		return fmt.Errorf("openneko web cannot start: %w; %s", err, fix)
	}
	return nil
}
