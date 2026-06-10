package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/compose"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/version"
)

func newStartCmd() *cobra.Command {
	var mode string
	var detach bool
	var skipMigrate bool
	var pullPolicy string
	var agentRuntime string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Bring up the OpenNeko stack",
		Long: `Bring up the OpenNeko stack via docker compose.

Modes:
  prod  Core services only (default; production stack)
  dev   Core + dev tooling
  demo  Core + AdventureWorks trial bundle`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m := compose.Mode(mode)
			if m == "" {
				m = compose.ModeProd
			}
			// Pin compose's image tags to this binary's version unless the
			// caller has already set OPENNEKO_VERSION — that lets the smoke
			// workflow (which builds openneko fresh from source, so its
			// embedded version is "0.0.0-dev") test against a real release
			// tag.
			if os.Getenv("OPENNEKO_VERSION") == "" {
				_ = os.Setenv("OPENNEKO_VERSION", "v"+version.Version)
			}

			if agentRuntime != "" && agentRuntime != "openshell" {
				return fmt.Errorf("--runtime must be openshell (got %q)", agentRuntime)
			}
			if agentRuntime == "openshell" {
				if err := configureOpenShellStateDir(); err != nil {
					return err
				}
			}

			sup := compose.New(assets.ComposeFS)
			sup.AgentRuntime = agentRuntime
			files, err := sup.Materialize(m)
			if err != nil {
				return err
			}
			project, err := sup.ProjectName(m)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			pullFlag := []string{}
			if pullPolicy != "" {
				switch pullPolicy {
				case "always", "missing", "never":
					pullFlag = []string{"--pull", pullPolicy}
				default:
					return fmt.Errorf("--pull must be one of: always, missing, never (got %q)", pullPolicy)
				}
			}

			// Stage 1: bring up neko-db only, wait healthy, run migrations.
			if !skipMigrate {
				if _, err := sup.Run(ctx, project, files, append([]string{"up", "-d"}, append(pullFlag, "neko-db")...), os.Stdout, os.Stderr); err != nil {
					return err
				}
				if err := waitDBHealthy(ctx, time.Minute); err != nil {
					return err
				}
				if err := runMigrations(ctx, cmd); err != nil {
					return err
				}
			}

			// Pre-pull the agent sandbox image at install time so the gateway's
			// first sandbox-create (the user's first chat) never blocks on a
			// large pull. Best-effort: a failure just falls back to a lazy pull.
			if agentRuntime == "openshell" {
				agentImg := agentImageRef(os.Getenv("OPENNEKO_AGENT_IMAGE"), os.Getenv("OPENNEKO_VERSION"))
				fmt.Fprintf(os.Stderr, "Pre-pulling agent sandbox image %s ...\n", agentImg)
				if err := sup.EnsureImage(ctx, agentImg, os.Stdout, os.Stderr); err != nil {
					fmt.Fprintf(os.Stderr, "warning: agent image pre-pull failed (%v); it will pull on first use\n", err)
				}
			}

			// Stage 2: bring up the rest.
			upArgs := []string{"up"}
			if detach {
				upArgs = append(upArgs, "-d")
			}
			upArgs = append(upArgs, pullFlag...)
			code, err := sup.Run(ctx, project, files, upArgs, os.Stdout, os.Stderr)
			if err != nil {
				return err
			}
			if code != 0 {
				return WithExit(code, nil)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "prod", "Stack mode: prod|dev|demo")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "Run in the background after services start")
	cmd.Flags().BoolVar(&skipMigrate, "skip-migrate", false, "Skip running migrations on start (advanced)")
	cmd.Flags().StringVar(&pullPolicy, "pull", "", "Override compose pull policy: always|missing|never (default: compose decides)")
	cmd.Flags().StringVar(&agentRuntime, "runtime", "", "Agent runtime: 'openshell' sandboxes the agent + plugins via a containerized OpenShell gateway (default: in-process)")
	return cmd
}

// agentImageRef resolves the agent sandbox image: an explicit override wins,
// else the default repo at the running version.
func agentImageRef(override, version string) string {
	if override != "" {
		return override
	}
	return "ghcr.io/open-neko/agent:" + version
}

// openShellStateDirOverride returns the OPENSHELL_STATE_DIR to set for goos, or
// "" to keep the compose default. The containerized gateway bind-mounts its PKI
// and the per-sandbox JWT from this dir into sandboxes; the in-VM docker daemon
// must resolve the SAME host path. macOS/OrbStack only maps paths under the
// user's home into its Linux VM — a /var/lib/... source comes back as an empty
// mount and the sandbox crash-loops on a missing JWT — so on macOS the state
// dir must live under $HOME. On Linux the compose default
// (/var/lib/openneko/openshell) is correct and docker creates it. An
// already-set value is always respected.
func openShellStateDirOverride(goos, home, existing string) string {
	if goos != "darwin" || existing != "" {
		return ""
	}
	return filepath.Join(home, ".openneko", "openshell")
}

func configureOpenShellStateDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := openShellStateDirOverride(runtime.GOOS, home, os.Getenv("OPENSHELL_STATE_DIR"))
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.Setenv("OPENSHELL_STATE_DIR", dir)
}

func waitDBHealthy(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := pgx.Connect(ctx, defaultConn().DSN())
		if err == nil {
			_ = conn.Close(ctx)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("neko-db did not become reachable within %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func runMigrations(ctx context.Context, cmd *cobra.Command) error {
	conn, err := pgx.Connect(ctx, defaultConn().DSN())
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	mig := &db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}
	out := cmd.OutOrStdout()
	_, err = mig.Apply(ctx, conn, func(format string, args ...any) {
		fmt.Fprintf(out, format+"\n", args...)
	})
	if err != nil && !errors.Is(err, ctx.Err()) {
		return err
	}
	return err
}

// defaultConn resolves the metadata-DB connection. Precedence: the local
// config.json (written by /setup after the operator rotates the bootstrap
// password) wins over env vars, which win over hardcoded defaults. This
// matches the TS reader in packages/db/src/local-config.ts so every consumer
// — Go migrate, web, worker, graphjin — converges on the rotated password.
func defaultConn() db.ConnConfig {
	conn := db.ConnConfig{
		Host:     envOr("NEKO_PG_HOST", "127.0.0.1"),
		Port:     envInt("NEKO_PG_PORT", envInt("OPENNEKO_DB_PORT", 5432)),
		User:     envOr("NEKO_PG_USER", "neko"),
		Password: envOr("NEKO_PG_PASSWORD", "secret"),
		Database: envOr("NEKO_PG_DATABASE", "neko"),
		SSLMode:  envOr("NEKO_PG_SSLMODE", "disable"),
	}
	local, _ := config.ReadLocal("")
	if local.Pg != nil {
		if local.Pg.Host != "" {
			conn.Host = local.Pg.Host
		}
		if local.Pg.Port != 0 {
			conn.Port = local.Pg.Port
		}
		if local.Pg.User != "" {
			conn.User = local.Pg.User
		}
		if local.Pg.Password != "" {
			conn.Password = local.Pg.Password
		}
		if local.Pg.Database != "" {
			conn.Database = local.Pg.Database
		}
		if local.Pg.SSLMode != "" {
			conn.SSLMode = local.Pg.SSLMode
		}
	}
	return conn
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		var x int
		_, err := fmt.Sscanf(v, "%d", &x)
		if err == nil {
			return x
		}
	}
	return fallback
}
