//go:build integration

// Integration test for the migrator. Spins up real pgvector/pgvector:pg16
// via testcontainers-go and runs every embedded migration against it.
//
// Run with:  go test -tags=integration ./internal/db/...
// Needs docker on the host; CI runs this on ubuntu-latest.
package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
)

func TestMigratorAppliesAllEmbeddedMigrationsAgainstRealPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pg, err := postgres.Run(ctx,
		"pgvector/pgvector:pg16",
		postgres.WithDatabase("neko"),
		postgres.WithUsername("neko"),
		postgres.WithPassword("secret"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		t.Fatalf("postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := pg.Terminate(ctx); err != nil {
			t.Logf("terminate: %v", err)
		}
	})

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connstring: %v", err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })

	mig := &db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}
	logged := []string{}
	ran, err := mig.Apply(ctx, conn, func(format string, args ...any) {
		logged = append(logged, format)
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if ran == 0 {
		t.Fatal("expected at least one migration to run against a fresh DB")
	}

	// schema_migrations should have one row per .sql file embedded.
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	files, _ := assets.MigrationsFS.ReadDir("migrations")
	wantSQL := 0
	for _, f := range files {
		if !f.IsDir() && len(f.Name()) > 4 && f.Name()[len(f.Name())-4:] == ".sql" {
			wantSQL++
		}
	}
	if count != wantSQL {
		t.Fatalf("schema_migrations has %d rows, want %d (one per embedded .sql)", count, wantSQL)
	}

	// Canonical tables installed by the migrations exist.
	for _, table := range []string{"organization", "schema_migrations"} {
		var oid *uint32
		if err := conn.QueryRow(ctx, "SELECT to_regclass($1)::oid", "public."+table).Scan(&oid); err != nil {
			t.Fatalf("regclass(%s): %v", table, err)
		}
		if oid == nil {
			t.Fatalf("expected table %s to exist after migrations", table)
		}
	}

	// Re-running is a no-op.
	ran2, err := mig.Apply(ctx, conn, nil)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if ran2 != 0 {
		t.Fatalf("expected 0 new migrations on second apply, got %d", ran2)
	}
}

func TestMigratorBootstrapAgainstExistingSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pg, err := postgres.Run(ctx,
		"pgvector/pgvector:pg16",
		postgres.WithDatabase("neko"),
		postgres.WithUsername("neko"),
		postgres.WithPassword("secret"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		t.Fatalf("postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })

	// Pre-create the canonical bootstrap table so the migrator's "existing
	// schema without tracking" path fires (mirrors what an operator who
	// upgraded from before schema_migrations existed looks like).
	if _, err := conn.Exec(ctx, `CREATE TABLE public.organization (id uuid primary key)`); err != nil {
		t.Fatal(err)
	}

	mig := &db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}
	ran, err := mig.Apply(ctx, conn, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if ran != 0 {
		t.Fatalf("bootstrap path should have marked all migrations applied without running them, ran=%d", ran)
	}
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("expected schema_migrations to be populated by bootstrap path")
	}
}
