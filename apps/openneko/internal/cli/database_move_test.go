package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

func runMoveCommand(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newDatabaseMoveCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.ExecuteContext(context.Background())
}

func TestDatabaseMoveRefusesBeforeItTouchesTheStack(t *testing.T) {
	isolateStackDatabase(t, "openneko-prod_"+bundledDatabaseVolume)
	t.Setenv("OPENNEKO_WORKSPACE_ROOT", t.TempDir())
	target := "postgres://neko:pw@db.internal:5432/neko?sslmode=verify-full"
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no confirmation", []string{"--database-url", target}, "--confirm MOVE"},
		{"wrong confirmation", []string{"--database-url", target, "--confirm", "yes"}, "--confirm MOVE"},
		{"loopback host", []string{"--database-url", "postgres://neko:pw@localhost:5432/neko", "--confirm", "MOVE"}, "container itself"},
		{"not postgres", []string{"--database-url", "mysql://neko@db/neko", "--confirm", "MOVE"}, "postgres://"},
	}
	for _, c := range cases {
		err := runMoveCommand(t, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestDatabaseMoveRefusesAnInstallThatHasAURL(t *testing.T) {
	isolateStackDatabase(t, "openneko-prod_"+bundledDatabaseVolume)
	root := t.TempDir()
	t.Setenv("OPENNEKO_WORKSPACE_ROOT", root)
	if err := os.MkdirAll(root+"/.openneko/runtime", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/.openneko/runtime/.project-name", []byte("openneko-prod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteLocalDatabaseURL("", "postgres://neko:pw@old.internal:5432/neko"); err != nil {
		t.Fatal(err)
	}
	err := runMoveCommand(t, "--database-url", "postgres://neko:pw@db.internal:5432/neko", "--confirm", "MOVE")
	if err == nil || !strings.Contains(err.Error(), "already uses a database URL") {
		t.Fatalf("err = %v", err)
	}
	local, _ := config.ReadLocal("")
	if local.DatabaseURL != "postgres://neko:pw@old.internal:5432/neko" {
		t.Fatalf("the stored URL changed to %q", local.DatabaseURL)
	}
}

func TestDatabaseMoveRefusesDemoMode(t *testing.T) {
	isolateStackDatabase(t)
	root := t.TempDir()
	t.Setenv("OPENNEKO_WORKSPACE_ROOT", root)
	if err := os.MkdirAll(root+"/.openneko/runtime", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/.openneko/runtime/.project-name", []byte("openneko-demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runMoveCommand(t, "--database-url", "postgres://neko:pw@db.internal:5432/neko", "--confirm", "MOVE")
	if err == nil || !strings.Contains(err.Error(), "demo mode") {
		t.Fatalf("err = %v", err)
	}
}

func TestRequireNoOwnedTablesRefusesADatabaseInUse(t *testing.T) {
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
	name := fmt.Sprintf("dbmove_%d", time.Now().UnixNano())
	for _, statement := range []string{
		"create role " + name + " login password 'pw'",
		"create database " + name + " owner " + name,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "drop database if exists "+name+" with (force)")
		_, _ = admin.Exec(ctx, "drop role if exists "+name)
	})
	superDB := connectAs(t, dsn, "", "", name)
	if _, err := superDB.Exec(ctx, "create extension if not exists vector; create table operator_notes (id int)"); err != nil {
		t.Fatal(err)
	}
	login := connectAs(t, dsn, name, "pw", name)
	if err := requireNoOwnedTables(ctx, login); err != nil {
		t.Fatalf("a database with only the operator's table and an extension was refused: %v", err)
	}
	if _, err := login.Exec(ctx, "create schema pgboss; create table pgboss.job (id int); create view public.v as select 1"); err != nil {
		t.Fatal(err)
	}
	err = requireNoOwnedTables(ctx, login)
	if err == nil || !strings.Contains(err.Error(), "owns 2 tables") || !strings.Contains(err.Error(), "pgboss.job") {
		t.Fatalf("err = %v", err)
	}
}

func connectAs(t *testing.T, dsn, user, password, database string) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		cfg.User, cfg.Password = user, password
	}
	cfg.Database = database
	conn, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}
