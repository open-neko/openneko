package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Open returns a pool on conn that names itself app in pg_stat_activity.
// With fields instead of a URL, each new connection takes the password from
// reread, so a password change reaches the pool without a restart.
func Open(ctx context.Context, conn ConnConfig, app string, reread func() (ConnConfig, error)) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(conn.DSN())
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = app
	if conn.URL == "" && reread != nil {
		cfg.BeforeConnect = func(_ context.Context, cc *pgx.ConnConfig) error {
			if fresh, err := reread(); err == nil && fresh.URL == "" {
				cc.Password = fresh.Password
			}
			return nil
		}
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Connect opens one session that names itself app, for a lock or a migration.
func Connect(ctx context.Context, conn ConnConfig, app string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(conn.DSN())
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams["application_name"] = app
	return pgx.ConnectConfig(ctx, cfg)
}
