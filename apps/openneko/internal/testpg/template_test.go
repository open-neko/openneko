package testpg

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestMigratedGivesEachTestItsOwnDatabase(t *testing.T) {
	ctx := context.Background()
	first := Migrated(t)
	if errors.Is(templateErr, errNoCreateDB) {
		t.Skip("the test role cannot create databases; each test gets a schema")
	}
	second := Migrated(t)
	var a, b string
	if err := first.QueryRow(ctx, `select current_database()`).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := second.QueryRow(ctx, `select current_database()`).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both pools use the database %s", a)
	}
	if _, err := first.Exec(ctx, `create table only_in_first (id int)`); err != nil {
		t.Fatal(err)
	}
	var seen bool
	if err := second.QueryRow(ctx, `select to_regclass('only_in_first') is not null`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen {
		t.Fatal("a table of the first database shows in the second")
	}
	var applied, files int
	if err := second.QueryRow(ctx, `select count(*) from information_schema.tables where table_schema = current_schema()`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	names, _, err := migrationNames()
	if err != nil {
		t.Fatal(err)
	}
	files = len(names)
	if applied == 0 || files == 0 {
		t.Fatalf("the copy holds %d tables from %d migration files", applied, files)
	}
	var organization bool
	if err := second.QueryRow(ctx, `select to_regclass('organization') is not null`).Scan(&organization); err != nil {
		t.Fatal(err)
	}
	if !organization {
		t.Fatal("the copy has no organization table")
	}
}

func TestMigratedDropsItsDatabaseAfterTheTest(t *testing.T) {
	ctx := context.Background()
	var name string
	t.Run("inner", func(t *testing.T) {
		if err := Migrated(t).QueryRow(ctx, `select current_database()`).Scan(&name); err != nil {
			t.Fatal(err)
		}
	})
	if errors.Is(templateErr, errNoCreateDB) {
		t.Skip("the test role cannot create databases; each test gets a schema")
	}
	admin, err := pgx.Connect(ctx, URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx) //nolint:errcheck
	var left bool
	if err := admin.QueryRow(ctx, `select exists (select 1 from pg_database where datname = $1)`, name).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left {
		t.Fatalf("the test database %s stays after its test", name)
	}
}

func TestEnsureTemplateRefusesARoleThatCannotCreateDatabases(t *testing.T) {
	ctx := context.Background()
	base := URL(t)
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) }) //nolint:errcheck
	role := fmt.Sprintf("ontest_role_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "create role "+role+" login password 'pw' nocreatedb nosuperuser"); err != nil {
		t.Skipf("the test role cannot create roles: %v", err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "drop role if exists "+role) }) //nolint:errcheck
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "pw")
	if _, err := ensureTemplate(u.String()); !errors.Is(err, errNoCreateDB) {
		t.Fatalf("ensureTemplate = %v, want errNoCreateDB", err)
	}
}

func TestDropStaleRemovesOnlyOldTestDatabases(t *testing.T) {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) }) //nolint:errcheck
	var can bool
	if err := admin.QueryRow(ctx, `select rolcreatedb or rolsuper from pg_roles where rolname = current_user`).Scan(&can); err != nil {
		t.Fatal(err)
	}
	if !can {
		t.Skip("the test role cannot create databases")
	}
	stamp := time.Now().UnixNano()
	old := fmt.Sprintf("%s%d_stale_%d", testDBPrefix, time.Now().Add(-3*staleAfter).Unix(), stamp)
	fresh := fmt.Sprintf("%s%d_fresh_%d", testDBPrefix, time.Now().Unix(), stamp)
	for _, name := range []string{old, fresh} {
		if _, err := admin.Exec(ctx, "create database "+name); err != nil {
			t.Fatal(err)
		}
		name := name
		t.Cleanup(func() { admin.Exec(context.Background(), "drop database if exists "+name) }) //nolint:errcheck
	}
	dropStale(ctx, admin)
	exists := func(name string) bool {
		var found bool
		if err := admin.QueryRow(ctx, `select exists (select 1 from pg_database where datname = $1)`, name).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	if exists(old) {
		t.Fatalf("%s stays", old)
	}
	if !exists(fresh) {
		t.Fatalf("%s was dropped", fresh)
	}
}
