package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/files"
)

func TestServeInitGivesServeOnlyTheFileTables(t *testing.T) {
	url := os.Getenv("OPENNEKO_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set OPENNEKO_TEST_DATABASE_URL to a Postgres database")
	}
	ctx := context.Background()
	base, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("serve_init_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "drop database "+name+" with (force)") //nolint:errcheck
		admin.Close(context.Background())
	})

	conn := db.ConnConfig{Host: base.Host, Port: int(base.Port), User: base.User, Password: base.Password, Database: name, SSLMode: "disable"}
	mig, err := pgx.Connect(ctx, conn.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}).Apply(ctx, mig, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := mig.Exec(ctx, `insert into organization (id, name) values (gen_random_uuid(), 'x')`); err != nil {
		t.Fatal(err)
	}
	mig.Close(ctx)

	configDir := t.TempDir()
	out := filepath.Join(t.TempDir(), "credentials")
	for i := 0; i < 2; i++ {
		if err := writeServeCredentials(ctx, conn, configDir, out); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	for _, f := range []string{serveTokenKeyFile, serveDatabaseFile} {
		info, err := os.Stat(filepath.Join(out, f))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", f, info.Mode().Perm())
		}
	}

	key, dsn, err := serveKeyAndDatabase(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := config.SigningSecret(configDir, files.TokenLabel)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, want) {
		t.Fatal("token key does not match the key web and worker sign with")
	}
	secret, err := os.ReadFile(filepath.Join(configDir, "secret-key"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{serveTokenKeyFile, serveDatabaseFile} {
		raw, _ := os.ReadFile(filepath.Join(out, f))
		if bytes.Contains(raw, bytes.TrimSpace(secret)) || bytes.Contains(raw, []byte(base.Password)) {
			t.Fatalf("%s holds the secret-key or the main database password", f)
		}
	}

	role, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as %s: %v", filesRole, err)
	}
	defer role.Close(ctx)
	if _, err := role.Exec(ctx, `insert into file_content_file (key, scope, id, base, version) values ('k', 's', 'i', 'b', 'v')`); err != nil {
		t.Fatal(err)
	}
	if _, err := role.Exec(ctx, `insert into file_content_unit (key, location, body) values ('k', 'l', 'hello world')`); err != nil {
		t.Fatal(err)
	}
	if _, err := role.Exec(ctx, `insert into file_content_table (scope, name, key, ord, file, info) values ('s', 't', 'k', 0, 'f', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := role.Exec(ctx, `update file_content_file set text = 't' where key = 'k'`); err != nil {
		t.Fatal(err)
	}
	if _, err := role.Exec(ctx, `delete from file_content_file where key = 'k'`); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`select count(*) from organization`,
		`select count(*) from app_user`,
		`select count(*) from data_source_secret`,
		`select count(*) from llm_provider_config`,
		`select count(*) from workflow_output`,
		`create table serve_owned (id int)`,
		`create role serve_made`,
	} {
		_, err := role.Exec(ctx, sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s running %q: want permission denied, got %v", filesRole, sql, err)
		}
	}
}

func TestRoleDatabaseURLKeepsHostAndTLS(t *testing.T) {
	got, err := roleDatabaseURL(db.ConnConfig{
		URL: "postgres://neko:old@db.example.com:6432/meta?sslmode=verify-full&sslrootcert=%2Fetc%2Fca.pem",
	}, filesRole, "p@ss/word")
	if err != nil {
		t.Fatal(err)
	}
	want := "postgres://neko_files:p%40ss%2Fword@db.example.com:6432/meta?sslmode=verify-full&sslrootcert=%2Fetc%2Fca.pem"
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}

	got, err = roleDatabaseURL(db.ConnConfig{Host: "neko-db", Port: 5432, User: "neko", Password: "x", Database: "neko"}, filesRole, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://neko_files:pw@neko-db:5432/neko?sslmode=disable" {
		t.Fatalf("fields: got %s", got)
	}

	if _, err := roleDatabaseURL(db.ConnConfig{URL: "mysql://x@y/z"}, filesRole, "pw"); err == nil {
		t.Fatal("a non-postgres URL must fail")
	}
}

func TestSeedGraphjinConfigCreatesOnceAndDropsTheShellSupervisor(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "graphjin")
	seed := filepath.Join(t.TempDir(), "seed.yml")
	if err := os.WriteFile(seed, []byte("secrets:\n  keystore:\n    key: \"REPLACE_WITH_BASE64_32_BYTE_KEY\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "agentic.yml")
	created, err := seedGraphjinConfig(file, seed)
	if err != nil || !created {
		t.Fatalf("first seed: created=%v err=%v", created, err)
	}
	first, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first, []byte(graphjinKeystorePlaceholder)) {
		t.Fatalf("the keystore placeholder is still in the config:\n%s", first)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", info.Mode().Perm())
	}

	stale := filepath.Join(dir, ".openneko-graphjin-supervisor.sh")
	if err := os.WriteFile(stale, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	created, err = seedGraphjinConfig(file, seed)
	if err != nil || created {
		t.Fatalf("second seed: created=%v err=%v", created, err)
	}
	again, _ := os.ReadFile(file)
	if !bytes.Equal(first, again) {
		t.Fatal("an existing config was replaced")
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the shell supervisor copy is still there: %v", err)
	}

	if _, err := seedGraphjinConfig(filepath.Join(t.TempDir(), "agentic.yml"), ""); err == nil {
		t.Fatal("a missing config with no seed must fail")
	}
}
