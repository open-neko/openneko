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
	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/secretenv"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func newReleaseCheckCmd() *cobra.Command {
	var scheduler, serveSocket, packsCheck, sandboxCheck, skillsCheck, bundledPacks, allowUnconfined bool
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
				state, network, runner, err := checkServeSocket(ctx, os.Getenv("OPENNEKO_SERVE_SOCKET"))
				unconfined := ""
				if errors.Is(err, errTurnsOff) {
					unconfined, err = acceptUnconfinedTurns(ctx, err, allowUnconfined)
				}
				if err != nil {
					return err
				}
				fmt.Fprintln(out, "serve_socket=ok")
				if unconfined != "" {
					fmt.Fprintf(out, "unconfined_turns=on (%s)\n", unconfined)
				}
				fmt.Fprintf(out, "serve_confinement=%s\n", state)
				fmt.Fprintf(out, "serve_network_confinement=%s\n", network)
				fmt.Fprintf(out, "runner_confinement=%s\n", runner)
			}
			if sandboxCheck {
				state, err := checkSandboxSocket(ctx, os.Getenv("OPENNEKO_SANDBOX_URL"), os.Getenv("OPENNEKO_SERVE_SOCKET"), os.Getenv("OPENNEKO_CONFIG_CHECK_SOCKET"))
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "sandbox=%s\n", state)
				if state != "off" {
					egress := checkSandboxEgress(ctx, sandbox.ConfigFromEnv(secretenv.Get))
					fmt.Fprintf(out, "sandbox_egress=%s\n", egress)
					if webfront.OperatorEnforcesEgress() {
						fmt.Fprintf(out, "sandbox_egress_by_operator=on\n")
					} else if egress != "closed" {
						return errors.New(sandboxEgressFailure(egress))
					}
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&scheduler, "scheduler", false, "The workflow scheduler finished a database tick since this web started")
	f.StringVar(&datasourceOrg, "datasource", "", "Run --query through GraphJin as this org's service role and require a row")
	f.StringVar(&mcpURL, "mcp-url", "http://graphjin:8080/api/v1/mcp", "GraphJin MCP endpoint for --datasource")
	f.StringVar(&query, "query", "query { soh(limit: 1, order_by: { id: desc }) { id } }", "GraphQL query for --datasource")
	f.BoolVar(&serveSocket, "serve-socket", false, "Connect to the agent socket of openneko serve (OPENNEKO_SERVE_SOCKET); fail when serve holds a model key or accepts one")
	f.BoolVar(&allowUnconfined, "allow-unconfined", false, "With --serve-socket, pass a system that cannot confine agent turns when an admin turned on turns without OS confinement")
	f.BoolVar(&sandboxCheck, "sandbox", false, "The sandbox is off, or its socket answers in a folder that holds none of serve's sockets; prints whether its network has a route out")
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

// serveStatus asks the serve on socket for its status frame.
func serveStatus(ctx context.Context, socket string) (serve.Status, error) {
	var status serve.Status
	if socket == "" {
		return status, errors.New("OPENNEKO_SERVE_SOCKET is not set")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return status, fmt.Errorf("serve socket %s: %w", socket, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	var reply serve.Frame
	if err := json.NewEncoder(conn).Encode(serve.Frame{Type: "status"}); err == nil {
		err = json.NewDecoder(conn).Decode(&reply)
	}
	if reply.Type != "status" || json.Unmarshal(reply.Result, &status) != nil {
		return status, fmt.Errorf("openneko serve on %s did not report whether it holds a model key; run serve from the same release as web", socket)
	}
	return status, nil
}

// statusStates reads the confinement of serve's turn part, its network part
// and its runners from a status frame.
func statusStates(socket string, status serve.Status) (host, network, runner confine.State, err error) {
	if len(status.Confinement) == 0 || json.Unmarshal(status.Confinement, &host) != nil || len(status.NetworkConfinement) == 0 || json.Unmarshal(status.NetworkConfinement, &network) != nil {
		return host, network, runner, fmt.Errorf("openneko serve on %s did not report its confinement; run serve from the same release as web", socket)
	}
	if len(status.RunnerConfinement) > 0 && json.Unmarshal(status.RunnerConfinement, &runner) != nil {
		return host, network, runner, fmt.Errorf("openneko serve on %s sent a runner confinement that this release cannot read; run serve from the same release as web", socket)
	}
	return host, network, runner, nil
}

func checkServeSocket(ctx context.Context, socket string) (host, network, runner confine.State, err error) {
	var none confine.State
	status, err := serveStatus(ctx, socket)
	if err != nil {
		return none, none, none, err
	}
	if len(status.KeyEnv) > 0 {
		return none, none, none, fmt.Errorf("openneko serve started with the secret variables %s; remove them from serve's environment and keep them in web (INSTALL.md, \"Model keys\")", strings.Join(status.KeyEnv, ", "))
	}
	if !status.RequireDoor {
		return none, none, none, errors.New("openneko serve accepts a model key in a frame because it runs in development mode; run serve without OPENNEKO_ENV=development")
	}
	state, network, runner, err := statusStates(socket, status)
	if err != nil {
		return none, none, none, err
	}
	if status.TurnsOff != "" {
		return state, network, runner, fmt.Errorf("%w: %s", errTurnsOff, status.TurnsOff)
	}
	if !runner.Confined && runner.Mechanism != "" {
		return state, network, runner, fmt.Errorf("agent runners run unconfined in production mode: %s", runner.Reason)
	}
	if !runner.Confined {
		return state, network, runner, fmt.Errorf("%s: agent turns run unconfined in production mode (%s)", confine.NotAvailable, runner.Reason)
	}
	if !state.Confined && state.Mechanism != "" {
		return state, network, runner, fmt.Errorf("openneko serve runs unconfined in production mode: %s", state.Reason)
	}
	if !network.Confined && network.Mechanism != "" {
		return state, network, runner, fmt.Errorf("the network part of openneko serve runs unconfined in production mode: %s", network.Reason)
	}
	return state, network, runner, nil
}

var errTurnsOff = errors.New("openneko serve refuses agent turns")

// acceptUnconfinedTurns passes a serve that cannot confine agent turns only
// when an admin turned on turns without OS confinement and the caller passed
// --allow-unconfined.
func acceptUnconfinedTurns(ctx context.Context, turnsOff error, allow bool) (string, error) {
	on, line, err := unconfinedTurnsState(ctx, webAdminSocket())
	switch {
	case err != nil:
		return "", fmt.Errorf("%w; the admin setting for turns without confinement cannot be read: %v", turnsOff, err)
	case !on:
		return "", turnsOff
	case !allow:
		return "", fmt.Errorf("%s; pass --allow-unconfined to accept it", line)
	}
	return line, nil
}

// unconfinedTurnsState reads the admin setting for agent turns without OS
// confinement from web's admin socket.
func unconfinedTurnsState(ctx context.Context, socket string) (bool, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://web/health/agent-unconfined", nil)
	if err != nil {
		return false, "", err
	}
	res, err := webAdminClient(socket).Do(req)
	if err != nil {
		return false, "", webAdminUnreachable(socket, err)
	}
	defer res.Body.Close()
	var state struct {
		On   bool   `json:"on"`
		Line string `json:"line"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&state); err != nil || res.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("web did not report the setting (HTTP %d)", res.StatusCode)
	}
	return state.On, state.Line, nil
}

func checkSandboxSocket(ctx context.Context, url string, stackSockets ...string) (string, error) {
	box := sandbox.Config{URL: url}
	if !box.Enabled() {
		return "off", nil
	}
	if box.SharesFolder(stackSockets...) {
		return "", fmt.Errorf("the sandbox socket %s is in the folder of serve's sockets; give the sandbox a socket folder of its own (INSTALL.md, \"Sandbox\")", url)
	}
	path, ok := sandbox.SocketPath(url)
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

func checkSandboxEgress(ctx context.Context, cfg sandbox.Config) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	egress, err := sandbox.ProbeEgress(ctx, cfg)
	if err != nil {
		return "unknown"
	}
	return egress
}

func sandboxEgressFailure(egress string) string {
	if egress == "open" {
		return "the sandbox network has a route out, so the terminal is off; run the sandbox on an internal network (INSTALL.md, \"Sandbox\"), or set " + webfront.OperatorEgressEnv + "=1 when you enforce its egress yourself"
	}
	return "the sandbox did not report that its network is closed, so the terminal is off; rebuild it from recipes/sandbox and run it on an internal network (INSTALL.md, \"Sandbox\"), or set " + webfront.OperatorEgressEnv + "=1 when you enforce its egress yourself"
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
