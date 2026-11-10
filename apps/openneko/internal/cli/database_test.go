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
		"ALTER DATABASE \"" + name + "\" OWNER TO \"" + name + "\";",
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
	if strings.Contains(out.String(), "FAIL") || !strings.Contains(out.String(), "ok    the vector extension") {
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
