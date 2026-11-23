package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestArchivedObjectNamesReadsTheTableOfContents(t *testing.T) {
	contents := `;
; Archive created at 2026-11-10 18:00:00 UTC
;
3; 3079 16385 EXTENSION - vector 
7; 2615 16390 SCHEMA - pgboss neko
225; 1259 16401 TABLE public file_content_file neko
226; 1259 16402 TABLE public Odd Name neko
4300; 0 16401 TABLE DATA public spend_ledger neko
227; 1259 16403 SEQUENCE public counter neko
228; 0 0 SEQUENCE OWNED BY public owned_seq neko
229; 1259 16404 VIEW public item_view neko
230; 1259 16405 MATERIALIZED VIEW public item_matview neko
4301; 0 16405 MATERIALIZED VIEW DATA public data_only neko
231; 1255 16406 FUNCTION public plus(integer, integer) neko
232; 1255 16407 PROCEDURE public noop() neko
233; 1247 16408 TYPE public mood neko
234; 1247 16409 DOMAIN public positive neko
`
	got := archivedObjectNames(contents)
	for _, want := range []string{"schema:pgboss", "relation:file_content_file", "relation:odd name", "relation:counter", "relation:item_view",
		"relation:item_matview", "routine:plus", "routine:noop", "type:mood", "type:positive"} {
		if !got[want] {
			t.Errorf("%s is missing from %v", want, got)
		}
	}
	for _, unwanted := range []string{"relation:spend_ledger", "relation:owned_seq", "relation:data_only", "schema:vector", "relation:-"} {
		if got[unwanted] {
			t.Errorf("%s must not be read as an object", unwanted)
		}
	}
}

func TestMigrationObjectNamesKnowCreatedDroppedAndRenamedObjects(t *testing.T) {
	known, err := migrationObjectNames()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"relation:schema_migrations", "schema:pgboss", "schema:openneko_internal", "relation:organization", "relation:file_content_file"} {
		if !known[want] {
			t.Errorf("%s is not known", want)
		}
	}
	if known["relation:app_other"] {
		t.Fatal("a name that no migration uses is known")
	}
}

func TestRestoreNeedsTheFlagAndTheDatabaseNameToDropForeignObjects(t *testing.T) {
	foreign := []string{"table public.app_other", "schema billing"}
	var out bytes.Buffer
	if ok, err := confirmForeignDrop(strings.NewReader("neko\n"), &out, nil, "neko", false); ok || err != nil {
		t.Fatalf("no foreign objects: ok=%v err=%v", ok, err)
	}
	ok, err := confirmForeignDrop(strings.NewReader("neko\n"), &out, foreign, "neko", false)
	if ok || err == nil || !strings.Contains(err.Error(), "--drop-foreign-objects") || !strings.Contains(err.Error(), "a database and a login of its own") {
		t.Fatalf("no flag: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(out.String(), "  table public.app_other\n  schema billing") {
		t.Fatalf("the list is not printed:\n%s", &out)
	}
	for _, typed := range []string{"", "yes\n", "RESTORE\n", "nek\n"} {
		if ok, err := confirmForeignDrop(strings.NewReader(typed), &out, foreign, "neko", true); ok || err == nil {
			t.Fatalf("typed %q: ok=%v err=%v", typed, ok, err)
		}
	}
	if ok, err := confirmForeignDrop(strings.NewReader("neko\n"), &out, foreign, "neko", true); !ok || err != nil {
		t.Fatalf("flag and name: ok=%v err=%v", ok, err)
	}
	if ok, err := confirmForeignDrop(strings.NewReader("\n"), &out, foreign, "", true); ok || err == nil {
		t.Fatalf("an unknown database name must not confirm: ok=%v err=%v", ok, err)
	}
}

func TestForeignObjectsListsOnlyWhatOpenNekoDidNotMake(t *testing.T) {
	dsn := testpg.URL(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) })
	name := fmt.Sprintf("dbforeign_%d", time.Now().UnixNano())
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
	super, err := pgx.Connect(ctx, at(""))
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close(ctx)
	if _, err := super.Exec(ctx, "create extension vector"); err != nil {
		t.Fatal(err)
	}
	login, err := pgx.Connect(ctx, at(name))
	if err != nil {
		t.Fatal(err)
	}
	defer login.Close(ctx)
	if _, err := (&db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}).Apply(ctx, login, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := login.Exec(ctx, "create schema pgboss; create table pgboss.job (id int)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReconcileStorage(ctx, login, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}

	foreign, err := foreignObjects(ctx, login, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 0 {
		t.Fatalf("a migrated database has foreign objects: %v", foreign)
	}
	var out bytes.Buffer
	_ = checkDatabase(ctx, at(name), &out)
	if strings.Contains(out.String(), "did not make") {
		t.Fatalf("database check warns on a migrated database:\n%s", &out)
	}

	if _, err := login.Exec(ctx, `
create table app_other (id int);
create schema billing; create table billing.invoice (id int);
create function app_touch() returns int language sql as 'select 1';
create type app_state as enum ('a', 'b');
create view app_view as select id from app_other`); err != nil {
		t.Fatal(err)
	}
	if _, err := super.Exec(ctx, "create table operator_notes (id int)"); err != nil {
		t.Fatal(err)
	}
	foreign, err = foreignObjects(ctx, login, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "function app_touch(), schema billing, table public.app_other, type app_state, view public.app_view"
	if strings.Join(foreign, ", ") != want {
		t.Fatalf("foreign = %q, want %q", strings.Join(foreign, ", "), want)
	}
	foreign, err = foreignObjects(ctx, login, map[string]bool{"relation:app_other": true, "schema:billing": true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(foreign, ", ") != "function app_touch(), type app_state, view public.app_view" {
		t.Fatalf("the archive's own objects are listed: %q", foreign)
	}
	out.Reset()
	_ = checkDatabase(ctx, at(name), &out)
	if strings.Contains(out.String(), "FAIL  "+name+" owns") {
		t.Fatalf("foreign objects must warn, not fail:\n%s", &out)
	}
	if !strings.Contains(out.String(), "warn  "+name+" owns 5 objects in "+name+" that OpenNeko did not make (function app_touch(), schema billing,") ||
		!strings.Contains(out.String(), "a login of its own") {
		t.Fatalf("output:\n%s", &out)
	}
}
