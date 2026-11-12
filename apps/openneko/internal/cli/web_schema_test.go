package cli

import (
	"bytes"
	"context"
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestWebSchemaCheck(t *testing.T) {
	pool := testpg.Pool(t)
	ctx := context.Background()
	names, err := fs.Glob(assets.MigrationsFS, "migrations/*.sql")
	if err != nil || len(names) < 2 {
		t.Fatalf("embedded migrations: %v %v", names, err)
	}
	first, last := path.Base(names[0]), path.Base(names[len(names)-1])

	t.Setenv("OPENNEKO_SKIP_MIGRATE", "")
	var out bytes.Buffer
	err = checkSchema(ctx, &out, pool)
	if err == nil || !strings.Contains(err.Error(), "the first "+first+": run `openneko migrate`") {
		t.Fatalf("no ledger: %v", err)
	}

	if _, err := pool.Exec(ctx, `create table schema_migrations (name text primary key, applied_at timestamptz not null default now())`); err != nil {
		t.Fatal(err)
	}
	for _, n := range names[:len(names)-1] {
		if _, err := pool.Exec(ctx, `insert into schema_migrations(name) values ($1)`, path.Base(n)); err != nil {
			t.Fatal(err)
		}
	}
	err = checkSchema(ctx, &out, pool)
	if err == nil || !strings.Contains(err.Error(), "lacks 1 migration(s) of this release, the first "+last) {
		t.Fatalf("one pending: %v", err)
	}

	t.Setenv("OPENNEKO_SKIP_MIGRATE", "1")
	out.Reset()
	if err := checkSchema(ctx, &out, pool); err != nil {
		t.Fatalf("one pending with OPENNEKO_SKIP_MIGRATE: %v", err)
	}
	if !strings.Contains(out.String(), "OPENNEKO_SKIP_MIGRATE is set, so web starts") {
		t.Fatalf("one pending with OPENNEKO_SKIP_MIGRATE said %q", out.String())
	}

	t.Setenv("OPENNEKO_SKIP_MIGRATE", "")
	if _, err := pool.Exec(ctx, `insert into schema_migrations(name) values ($1), ('9998_later.sql'), ('9999_latest.sql')`, last); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := checkSchema(ctx, &out, pool); err != nil {
		t.Fatalf("a newer database: %v", err)
	}
	if !strings.Contains(out.String(), "holds 2 migration(s) that this release does not know, the last 9999_latest.sql") {
		t.Fatalf("a newer database said %q", out.String())
	}

	if _, err := pool.Exec(ctx, `delete from schema_migrations where name like '999%'`); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := checkSchema(ctx, &out, pool); err != nil || out.Len() != 0 {
		t.Fatalf("an up-to-date database: %v %q", err, out.String())
	}
}

func TestWebSchemaCheckWithNoDatabase(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://neko:secret@127.0.0.1:1/neko?connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var out bytes.Buffer
	if err := checkSchema(context.Background(), &out, pool); err != nil {
		t.Fatalf("an unreachable database stopped web: %v", err)
	}
	if !strings.Contains(out.String(), "the migration check did not run") {
		t.Fatalf("an unreachable database said %q", out.String())
	}
}
