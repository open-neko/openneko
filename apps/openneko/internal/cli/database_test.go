package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
)

func TestDatabaseCheckPrintsTheSQLForEachMissingRight(t *testing.T) {
	dsn := os.Getenv("OPENNEKO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set OPENNEKO_TEST_DATABASE_URL to a Postgres database")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "select pg_advisory_lock(7129)"); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("dbcheck_%d", time.Now().UnixNano())
	for _, statement := range []string{
		"create database " + name,
		"create role " + name + " login password 'pw'",
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "drop database if exists "+name+" with (force)")
		_, _ = admin.Exec(ctx, "drop role if exists "+name)
		_, _ = admin.Exec(ctx, "select pg_advisory_unlock(7129)")
	})
	at := func(user, password string) string {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if user != "" {
			u.User = url.UserPassword(user, password)
		}
		u.Path = "/" + name
		return u.String()
	}

	var out bytes.Buffer
	err = checkDatabase(ctx, at(name, "pw"), &out)
	if !errors.Is(err, errDatabaseNotReady) {
		t.Fatalf("a login with no rights: err = %v\n%s", err, &out)
	}
	for _, want := range []string{
		"FAIL  " + name + " cannot create tables in schema public",
		"FAIL  " + name + " cannot create schemas in " + name,
		"GRANT CREATE ON DATABASE \"" + name + "\" TO \"" + name + "\";",
		"GRANT CREATE ON SCHEMA public TO \"" + name + "\";",
		"ALTER ROLE \"" + name + "\" CREATEROLE;",
		"Run this SQL as a superuser, in database " + name,
		"warn  TLS is off",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output does not hold %q:\n%s", want, &out)
		}
	}
	if strings.Count(out.String(), "CREATEROLE;") != 2 {
		t.Fatalf("want the CREATEROLE line for neko_files and neko_subscriptions:\n%s", &out)
	}

	out.Reset()
	if err := checkDatabase(ctx, at("", ""), &out); err != nil {
		t.Fatalf("a superuser: err = %v\n%s", err, &out)
	}
	if strings.Contains(out.String(), "FAIL") || !strings.Contains(out.String(), "ok    the vector extension can be created (pgvector ") || !strings.Contains(out.String(), "ok    connected (PostgreSQL ") {
		t.Fatalf("a superuser:\n%s", &out)
	}
	var vector bool
	if err := admin.QueryRow(ctx, "select exists (select from pg_database d where d.datname = $1)", name).Scan(&vector); err != nil || !vector {
		t.Fatalf("database %s: %v", name, err)
	}
	probeConn, err := pgx.Connect(ctx, at("", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer probeConn.Close(ctx)
	if err := probeConn.QueryRow(ctx, "select exists (select from pg_extension where extname = 'vector')").Scan(&vector); err != nil || vector {
		t.Fatalf("the check left the vector extension behind (%v)", err)
	}
}

func TestSSLModeOf(t *testing.T) {
	for dsn, want := range map[string]string{
		"postgres://u@h/d?sslmode=verify-full":   "verify-full",
		"postgres://u@h/d":                       "prefer",
		"host=h user=u sslmode=require dbname=d": "require",
		"host=h user=u":                          "prefer",
	} {
		if got := sslModeOf(dsn); got != want {
			t.Fatalf("sslModeOf(%q) = %q, want %q", dsn, got, want)
		}
	}
}

func TestDatabaseCheckPassesALoginThatDoesNotOwnTheDatabase(t *testing.T) {
	dsn := os.Getenv("OPENNEKO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set OPENNEKO_TEST_DATABASE_URL to a Postgres database")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "select pg_advisory_lock(7129)"); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("dbnotowner_%d", time.Now().UnixNano())
	for _, statement := range []string{
		"create database " + name,
		"create role " + name + " login password 'pw' createrole",
		"grant create on database " + name + " to " + name,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "drop database if exists "+name+" with (force)")
		_, _ = admin.Exec(ctx, "drop role if exists "+name)
		_, _ = admin.Exec(ctx, "select pg_advisory_unlock(7129)")
	})
	at := func(user, password string) string {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if user != "" {
			u.User = url.UserPassword(user, password)
		}
		u.Path = "/" + name
		return u.String()
	}
	super, err := pgx.Connect(ctx, at("", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close(ctx)
	if _, err := super.Exec(ctx, "create extension vector; grant create on schema public to "+name); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := checkDatabase(ctx, at(name, "pw"), &out); err != nil {
		t.Fatalf("a login with the rights it needs: err = %v\n%s", err, &out)
	}
	if !strings.Contains(out.String(), "ok    "+name+" can create schemas in "+name) || !strings.Contains(out.String(), "ok    the vector extension exists (pgvector ") {
		t.Fatalf("output:\n%s", &out)
	}

	login, err := pgx.Connect(ctx, at(name, "pw"))
	if err != nil {
		t.Fatal(err)
	}
	defer login.Close(ctx)
	if _, err := (&db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}).Apply(ctx, login, func(string, ...any) {}); err != nil {
		t.Fatalf("migrations as a login that does not own the database: %v", err)
	}
	if err := db.InspectStorage(ctx, login, nil); err != nil {
		t.Fatalf("storage report as a login that does not own the database: %v", err)
	}
	var owner string
	if err := super.QueryRow(ctx, "select pg_get_userbyid(datdba) from pg_database where datname = current_database()").Scan(&owner); err != nil || owner == name {
		t.Fatalf("database owner = %q (%v)", owner, err)
	}
}

func TestVectorVerdictHoldsThePgvectorFloor(t *testing.T) {
	never := func() bool { t.Fatal("probe ran"); return false }
	yes := func() bool { return true }
	for _, c := range []struct {
		installed, available string
		probe                func() bool
		level, text, sql     string
	}{
		{"0.8.0", "0.8.0", never, "ok", "exists (pgvector 0.8.0)", ""},
		{"0.6.0", "0.6.0", never, "ok", "exists (pgvector 0.6.0)", ""},
		{"0.5.1", "0.8.0", never, "FAIL", "version 0.5.1. OpenNeko needs pgvector 0.6.0 or later, and the server has 0.8.0", "ALTER EXTENSION vector UPDATE;"},
		{"0.5.1", "0.5.1", never, "FAIL", "Install pgvector 0.6.0 or later on the Postgres server, then update", "ALTER EXTENSION vector UPDATE;"},
		{"", "", never, "FAIL", "the server has no pgvector", "CREATE EXTENSION vector;"},
		{"", "0.4.4", never, "FAIL", "the server has pgvector 0.4.4. Install pgvector 0.6.0 or later", "CREATE EXTENSION vector;"},
		{"", "0.10.0", yes, "ok", "can be created (pgvector 0.10.0)", ""},
		{"", "0.7.4", func() bool { return false }, "FAIL", "this login cannot create it in neko", "CREATE EXTENSION vector;"},
	} {
		f := vectorVerdict(c.installed, c.available, "neko", c.probe)
		if f.level != c.level || !strings.Contains(f.text, c.text) || strings.Join(f.sql, "") != c.sql {
			t.Errorf("installed %q, available %q: got %+v", c.installed, c.available, f)
		}
	}
}

func TestOlderVersion(t *testing.T) {
	for _, c := range []struct {
		version, floor string
		older          bool
	}{
		{"0.6.0", "0.6.0", false},
		{"0.10.1", "0.6.0", false},
		{"0.5.99", "0.6.0", true},
		{"0.6", "0.6.0", true},
		{"1.0.0", "0.6.0", false},
		{"0.7.0-dev", "0.6.0", false},
	} {
		if got := olderVersion(c.version, c.floor); got != c.older {
			t.Errorf("olderVersion(%q, %q) = %v", c.version, c.floor, got)
		}
	}
}
