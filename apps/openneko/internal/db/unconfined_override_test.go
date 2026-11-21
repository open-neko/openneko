package db

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/open-neko/neko/apps/openneko/assets"
)

func TestUpgradeRemovesAStoredUnconfinedOverride(t *testing.T) {
	target, _ := testDatabase(t)
	ctx := context.Background()
	conn, err := Connect(ctx, target, "openneko migrate")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	before := fstest.MapFS{}
	names, err := fs.Glob(assets.MigrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.TrimPrefix(name, "migrations/") >= "0154" {
			continue
		}
		data, err := fs.ReadFile(assets.MigrationsFS, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: data}
	}
	if _, err := (&Migrator{FS: before, Dir: "migrations"}).Apply(ctx, conn, t.Logf); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `insert into install_setting (name, value) values ('agent_turns_unconfined', '{"on":true,"hostId":"h","kernel":"k"}')`); err != nil {
		t.Fatal(err)
	}

	if _, err := (&Migrator{FS: assets.MigrationsFS, Dir: "migrations"}).Apply(ctx, conn, t.Logf); err != nil {
		t.Fatal(err)
	}
	var table *string
	if err := conn.QueryRow(ctx, `select to_regclass('install_setting')::text`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table != nil {
		t.Fatalf("after the upgrade the database still holds %s, where the override for unconfined agent turns was kept", *table)
	}
	for _, tbl := range []string{"work_run", "processing_job"} {
		var n int
		if err := conn.QueryRow(ctx, `select count(*) from information_schema.columns where table_name = $1 and column_name = 'ran_unconfined'`, tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s lost ran_unconfined, which the runs before 6.0.0 keep", tbl)
		}
	}
}
