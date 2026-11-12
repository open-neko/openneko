// Package testpg gives tests a fresh Postgres schema.
package testpg

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/assets"
)

// Pool returns a pool on a new schema with the given embedded migrations
// applied. It skips the test when OPENNEKO_TEST_DATABASE_URL is unset.
func Pool(t testing.TB, migrations ...string) *pgxpool.Pool {
	t.Helper()
	return pool(t, false, migrations)
}

// Migrated returns a pool on a new schema with every embedded migration
// applied. The public schema stays on the search path for the vector type.
func Migrated(t testing.TB) *pgxpool.Pool {
	t.Helper()
	names, err := fs.Glob(assets.MigrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		names[i] = path.Base(name)
	}
	return pool(t, true, names)
}

func pool(t testing.TB, public bool, migrations []string) *pgxpool.Pool {
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
	if public {
		// Created inside the test schema, the extension would go when that
		// schema is dropped, under the tests of other packages.
		if err := publicVector(ctx, admin); err != nil {
			t.Fatal(err)
		}
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
	if public {
		cfg.ConnConfig.RuntimeParams["search_path"] = schema + ", public"
	}
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

func publicVector(ctx context.Context, admin *pgxpool.Pool) error {
	tx, err := admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('testpg-vector'))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `create extension if not exists vector schema public`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
