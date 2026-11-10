// Package testpg gives tests a fresh Postgres schema.
package testpg

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/assets"
)

// Pool returns a pool on a new schema with the given embedded migrations
// applied. It skips the test when OPENNEKO_TEST_DATABASE_URL is unset.
func Pool(t testing.TB, migrations ...string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("OPENNEKO_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set OPENNEKO_TEST_DATABASE_URL to a Postgres database")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "drop schema "+schema+" cascade") //nolint:errcheck
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, m := range migrations {
		ddl, err := assets.MigrationsFS.ReadFile("migrations/" + m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(ddl)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}
