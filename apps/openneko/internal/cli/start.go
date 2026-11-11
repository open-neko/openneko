package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/compose"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/setup"
)

func newStartCmd() *cobra.Command {
	var mode string
	var detach bool
	var skipMigrate bool
	var pullPolicy string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Bring up the OpenNeko stack",
		Long: `Bring up the OpenNeko stack via docker compose.

Modes:
  prod  Core services only (default; production stack)
  dev   Developer defaults; source checkouts should use pnpm dev:setup + pnpm dev
  demo  Core + AdventureWorks trial bundle`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			selectedMode, err := modeForStart(cmd, mode)
			if err != nil {
				return err
			}
			// Same host checks setup runs; `start` used to skip them and race
			// straight into an opaque compose failure (port conflict with a
			// neighboring stack, docker gone away). A stack already answering
			// legitimately holds the ports, so only a fresh bring-up checks.
			if !setup.NewClient(webBaseURL()).Ready(ctx) {
				if err := runPreflight(cmd.OutOrStdout()); err != nil {
					return err
				}
			}
			return bringUpStack(ctx, cmd, selectedMode, bringUpOptions{
				detach:      detach,
				skipMigrate: skipMigrate,
				pullPolicy:  pullPolicy,
			})
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "prod", "Stack mode: prod|dev|demo")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "Run in the background after services start")
	cmd.Flags().BoolVar(&skipMigrate, "skip-migrate", false, "Skip running migrations on start (advanced)")
	cmd.Flags().StringVar(&pullPolicy, "pull", "", "Override compose pull policy: always|missing|never (default: compose decides)")
	return cmd
}

// bringUpOptions controls a stack bring-up. `start` and `setup` share the same
// staged flow; setup sets quiet to trim the install chatter into clean step
// lines (it adds --quiet-pull and skips the verbose pre-pull banner).
type bringUpOptions struct {
	detach      bool
	skipMigrate bool
	pullPolicy  string
	quiet       bool
}

// bringUpStack runs the start steps in order, then `compose up`. `start` and
// `setup` share it.
func bringUpStack(ctx context.Context, cmd *cobra.Command, mode compose.Mode, opts bringUpOptions) error {
	m := mode
	if m == "" {
		m = compose.ModeProd
	}
	sup := compose.New(assets.ComposeFS)
	project, err := sup.ProjectName(m)
	if err != nil {
		return err
	}
	files, err := stackFiles(ctx, sup, m, project)
	if err != nil {
		return err
	}
	previousSkipMigrate, hadSkipMigrate := os.LookupEnv("OPENNEKO_SKIP_MIGRATE")
	if opts.skipMigrate {
		_ = os.Setenv("OPENNEKO_SKIP_MIGRATE", "1")
	} else {
		_ = os.Unsetenv("OPENNEKO_SKIP_MIGRATE")
	}
	defer func() {
		if hadSkipMigrate {
			_ = os.Setenv("OPENNEKO_SKIP_MIGRATE", previousSkipMigrate)
		} else {
			_ = os.Unsetenv("OPENNEKO_SKIP_MIGRATE")
		}
	}()

	pullFlag := []string{}
	if opts.pullPolicy != "" {
		switch opts.pullPolicy {
		case "always", "missing", "never":
			pullFlag = []string{"--pull", opts.pullPolicy}
		default:
			return fmt.Errorf("--pull must be one of: always, missing, never (got %q)", opts.pullPolicy)
		}
	}
	quietPull := []string{}
	if opts.quiet {
		quietPull = []string{"--quiet-pull"}
	}

	runFlags := append(append([]string{}, pullFlag...), quietPull...)
	if err := runStartSteps(ctx, sup, project, files, m, runFlags); err != nil {
		return err
	}

	// --remove-orphans retires services a newer release dropped.
	upArgs := []string{"up", "--remove-orphans"}
	if opts.detach {
		upArgs = append(upArgs, "-d")
	}
	upArgs = append(upArgs, pullFlag...)
	upArgs = append(upArgs, quietPull...)
	code, err := sup.Run(ctx, project, files, upArgs, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return WithExit(code, nil)
	}
	return nil
}

// defaultConn resolves the metadata-DB connection. Precedence: the local
// config.json (written by /setup after the operator rotates the bootstrap
// password) wins over env vars, which win over hardcoded defaults. This
// matches the TS reader in packages/db/src/local-config.ts so every consumer
// — Go migrate, web, worker, graphjin — converges on the rotated password.
func defaultConn() (db.ConnConfig, error) {
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
	// OPENNEKO_PG_ENV_OVERRIDE=1 puts set env vars ahead of config.json, as
	// packages/db does for host processes in development.
	if os.Getenv("OPENNEKO_PG_ENV_OVERRIDE") == "1" {
		conn.Host = envOr("NEKO_PG_HOST", conn.Host)
		conn.Port = envInt("NEKO_PG_PORT", conn.Port)
		conn.User = envOr("NEKO_PG_USER", conn.User)
		conn.Password = envOr("NEKO_PG_PASSWORD", conn.Password)
		conn.Database = envOr("NEKO_PG_DATABASE", conn.Database)
		conn.SSLMode = envOr("NEKO_PG_SSLMODE", conn.SSLMode)
	}
	url, err := databaseURL(local)
	if err != nil {
		return db.ConnConfig{}, err
	}
	conn.URL = url
	return conn, nil
}

// databaseURL returns the configured database URL: the file that
// OPENNEKO_DATABASE_URL_FILE names, else database_url in config.json.
func databaseURL(local config.Local) (string, error) {
	file := strings.TrimSpace(os.Getenv("OPENNEKO_DATABASE_URL_FILE"))
	if file == "" {
		return strings.TrimSpace(local.DatabaseURL), nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read OPENNEKO_DATABASE_URL_FILE: %w", err)
	}
	url := strings.TrimSpace(string(raw))
	if url == "" {
		return "", fmt.Errorf("OPENNEKO_DATABASE_URL_FILE (%s) is empty", file)
	}
	return url, nil
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
