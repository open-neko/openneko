package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
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
			_ = os.Setenv("OPENNEKO_VERSION", "v"+version.Version)

			sup := compose.New(assets.ComposeFS)
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

			// Stage 1: bring up neko-db only, wait healthy, run migrations.
			if !skipMigrate {
				if _, err := sup.Run(ctx, project, files, []string{"up", "-d", "neko-db"}, os.Stdout, os.Stderr); err != nil {
					return err
				}
				if err := waitDBHealthy(ctx, time.Minute); err != nil {
					return err
				}
				if err := runMigrations(ctx, cmd); err != nil {
					return err
				}
			}

			// Stage 2: bring up the rest.
			upArgs := []string{"up"}
			if detach {
				upArgs = append(upArgs, "-d")
			}
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
	return cmd
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
		Port:     envInt("OPENNEKO_DB_PORT", 5432),
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
