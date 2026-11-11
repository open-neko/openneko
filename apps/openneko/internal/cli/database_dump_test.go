package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestSplitDatabasePasswordKeepsItOffTheCommandLine(t *testing.T) {
	plain, password, err := splitDatabasePassword("postgres://neko:p%40ss@db.example.com:5432/neko?sslmode=verify-full&sslrootcert=/run/secrets/database-ca")
	if err != nil {
		t.Fatal(err)
	}
	if password != "p@ss" || strings.Contains(plain, "p%40ss") || strings.Contains(plain, "p@ss") {
		t.Fatalf("plain=%q password=%q", plain, password)
	}
	if !strings.Contains(plain, "neko@db.example.com:5432/neko?") || !strings.Contains(plain, "sslrootcert=") {
		t.Fatalf("the URL lost its parts: %q", plain)
	}
	if _, _, err := splitDatabasePassword("host=db user=neko"); err == nil {
		t.Fatal("a key-value DSN must be refused")
	}
}

func TestRestoreListDropsTheExtensionEntries(t *testing.T) {
	contents := `;
; Archive created at 2026-11-10 18:00:00 UTC
;
3; 3079 16385 EXTENSION - vector 
4210; 0 0 COMMENT - EXTENSION vector 
225; 1259 16401 TABLE public file_content_file neko
4211; 0 0 COMMENT public TABLE file_content_file neko
4300; 0 16401 TABLE DATA public file_content_file neko
4400; 0 0 ACL public TABLE file_content_file neko
`
	got, grants := restoreList(contents)
	if strings.Contains(got, "EXTENSION") || strings.Contains(got, "ACL") {
		t.Fatalf("an extension or grant entry is left:\n%s", got)
	}
	for _, want := range []string{"TABLE public file_content_file", "COMMENT public TABLE", "TABLE DATA"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q was dropped:\n%s", want, got)
		}
	}
	if grants != "4400; 0 0 ACL public TABLE file_content_file neko\n" {
		t.Fatalf("grants = %q", grants)
	}
}

func TestGrantStatementsDropTheServeRoles(t *testing.T) {
	script := `--
-- PostgreSQL database dump
--
SET statement_timeout = 0;
SET transaction_timeout = 0;
SELECT pg_catalog.set_config('search_path', '', false);
GRANT USAGE ON SCHEMA public TO neko_files;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.file_content_file TO neko_files_neko;
GRANT SELECT ON TABLE public.workflow_output TO neko_subscriptions;
GRANT SELECT ON TABLE public.workflow_output TO "neko_subscriptions_my-db_1a2b3c4d";
REVOKE ALL ON TABLE public.workflow_output FROM neko_subscriptions_neko;
GRANT SELECT ON TABLE public.workflow_output TO reporting;
GRANT SELECT ON TABLE public.neko_files_log TO "Neko Readers";
REVOKE ALL ON SCHEMA public FROM PUBLIC;
`
	want := `GRANT SELECT ON TABLE public.workflow_output TO reporting;
GRANT SELECT ON TABLE public.neko_files_log TO "Neko Readers";
REVOKE ALL ON SCHEMA public FROM PUBLIC;
`
	if got := grantStatements(script); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDescribeDatabaseURLNamesTheTarget(t *testing.T) {
	if got := describeDatabaseURL("postgres://neko:pw@db.example.com:5432/neko?sslmode=verify-full"); got != "database neko on db.example.com:5432" {
		t.Fatalf("got %q", got)
	}
}

func TestClearOwnedObjectsKeepsExtensionsGrantsAndOthersObjects(t *testing.T) {
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
	name := fmt.Sprintf("dbclear_%d", time.Now().UnixNano())
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
	at := func(user string) string {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if user != "" {
			u.User = url.UserPassword(user, "pw")
		}
		u.Path = "/" + name
		return u.String()
	}
	exec := func(conn *pgx.Conn, statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := conn.Exec(ctx, statement); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
	}

	super, err := pgx.Connect(ctx, at(""))
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close(ctx)
	exec(super,
		"create extension vector",
		"grant create on schema public to "+name,
		"create table operator_notes (id int)",
		"grant select on operator_notes to "+name,
	)

	login, err := pgx.Connect(ctx, at(name))
	if err != nil {
		t.Fatal(err)
	}
	defer login.Close(ctx)
	exec(login,
		"create schema pgboss",
		"create table pgboss.job (id int)",
		"create type mood as enum ('ok', 'bad')",
		"create domain positive as int check (value > 0)",
		"create type pair as (a int, b int)",
		"create table item (id serial primary key, embedding vector(3), m mood, p positive, q pair)",
		"create sequence counter",
		"create sequence a_first_seq",
		"create table z_ledger (id bigint generated always as identity primary key, n bigint default nextval('a_first_seq'))",
		"alter sequence a_first_seq owned by z_ledger.n",
		"create view item_view as select id from item",
		"create materialized view item_matview as select id from item",
		"create function touch() returns trigger language plpgsql as 'begin return new; end'",
		"create trigger item_touch before insert on item for each row execute function touch()",
		"create function plus(a int, b int) returns int language sql as 'select a + b'",
		"create procedure noop() language sql as 'select 1'",
	)

	exec(super,
		"create schema operator_audit",
		"create table operator_audit.log (id int)",
		`create table "Odd Name" (id int)`,
	)
	exclude, err := dumpExclusions(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`--exclude-schema=operator_audit`, `--exclude-table=public."Odd Name"`, `--exclude-table=public.operator_notes`}
	if strings.Join(exclude, "\n") != strings.Join(want, "\n") {
		t.Fatalf("dump exclusions = %q", exclude)
	}

	exec(login, clearOwnedObjectsSQL)

	var left []string
	rows, err := super.Query(ctx, `
select 'schema ' || nspname from pg_namespace where nspowner = (select oid from pg_roles where rolname = $1)
union all select 'relation ' || relname from pg_class where relowner = (select oid from pg_roles where rolname = $1)
union all select 'routine ' || proname from pg_proc where proowner = (select oid from pg_roles where rolname = $1)
union all select 'type ' || typname from pg_type where typowner = (select oid from pg_roles where rolname = $1)`, name)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			t.Fatal(err)
		}
		left = append(left, item)
	}
	rows.Close()
	if len(left) != 0 {
		t.Fatalf("the login still owns: %v", left)
	}
	var keep bool
	if err := super.QueryRow(ctx, `select exists (select from pg_extension where extname = 'vector') and to_regclass('public.operator_notes') is not null`).Scan(&keep); err != nil || !keep {
		t.Fatalf("the extension or the operator's table is gone (err %v)", err)
	}
	exec(login, "create table after_clear (id int)", "select count(*) from operator_notes")

	exec(login, clearOwnedObjectsSQL)
}
