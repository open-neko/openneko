package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/installation"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func readFileOr(path, missing string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return missing
	}
	return string(raw)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestArchiveAndRestoreFolder(t *testing.T) {
	include := func(top string) bool { return top == "files" || top == "graphjin" }
	src := t.TempDir()
	writeTree(t, src, map[string]string{"files/u1/a.txt": "A", "graphjin/agentic.yml": "G", "logs/web.log": "L"})
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "files", "link")); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := archiveFolder(folderUnder(src, include), &archive); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	writeTree(t, dst, map[string]string{"files/old.txt": "gone", "logs/web.log": "kept"})
	if err := restoreFolder(folderUnder(dst, include), &archive); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"files/u1/a.txt": "A", "graphjin/agentic.yml": "G", "logs/web.log": "kept", "files/old.txt": "missing", "files/link": "missing"} {
		if got := readFileOr(filepath.Join(dst, name), "missing"); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	var empty bytes.Buffer
	if err := archiveFolder(folderUnder(filepath.Join(src, "absent"), include), &empty); err != nil || empty.Len() == 0 {
		t.Errorf("a missing folder gives an empty archive: %d bytes, %v", empty.Len(), err)
	}
}

func TestRestoreFolderRefusesAnEscape(t *testing.T) {
	raw := testVolumeTar(t, map[string]string{"../escape.txt": "x"})
	err := restoreFolder(folderUnder(t.TempDir(), func(string) bool { return true }), bytes.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "leaves the folder") {
		t.Fatalf("got %v", err)
	}
}

func TestReplaceFolderAndFile(t *testing.T) {
	base := t.TempDir()
	writeTree(t, base, map[string]string{"new/index.html": "v2", "new/assets/a.js": "js2", "web/index.html": "v1", "web/assets/old.js": "old", "newbin": "bin2", "bin": "bin1"})
	if err := replaceFolder(filepath.Join(base, "new"), filepath.Join(base, "web")); err != nil {
		t.Fatal(err)
	}
	if got := readFileOr(filepath.Join(base, "web", "index.html"), ""); got != "v2" {
		t.Errorf("index.html = %q", got)
	}
	if _, err := os.Stat(filepath.Join(base, "web", "assets", "old.js")); err == nil {
		t.Error("a file of the old web app is left")
	}
	if _, err := os.Stat(filepath.Join(base, "web.old")); err == nil {
		t.Error("web.old is left")
	}
	if err := replaceFile(filepath.Join(base, "newbin"), filepath.Join(base, "bin")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(base, "bin"))
	if err != nil || readFileOr(filepath.Join(base, "bin"), "") != "bin2" || info.Mode().Perm() != 0o755 {
		t.Errorf("bin: %v %v", info, err)
	}
	if !sameFile(filepath.Join(base, "bin"), filepath.Join(base, "bin")) || sameFile(filepath.Join(base, "bin"), filepath.Join(base, "newbin")) {
		t.Error("sameFile")
	}
}

func TestPostgresToolVersion(t *testing.T) {
	if got := toolVersionOf("pg_dump (PostgreSQL) 16.15 (Ubuntu 16.15-0ubuntu0.24.04.1)\n"); got != "16.15" {
		t.Errorf("toolVersionOf = %q", got)
	}
	if majorOf("16.15 (Debian 16.15-1.pgdg12+2)") != "16" || majorOf("17devel") != "17" {
		t.Error("majorOf")
	}
	if line, ok := postgresToolLine(context.Background(), "openneko-no-such-tool", "16.4"); ok || !strings.Contains(line, "PostgreSQL 16 client tools") {
		t.Errorf("missing tool: %q %v", line, ok)
	}
	if !isBackupVolume("native-config") || !isBackupVolume("native-data") || isBackupVolume("native-run") {
		t.Error("isBackupVolume")
	}
}

type nativeFixture struct {
	name, target, root, home string
	settings                 installation.Settings
}

func newNativeFixture(t *testing.T) nativeFixture {
	t.Helper()
	dsn := testpg.URL(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) })
	name := fmt.Sprintf("dbnative_%d", time.Now().UnixNano())
	for _, s := range []string{"create role " + name + " login password 'pw'", "create database " + name + " owner " + name} {
		if _, err := admin.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "drop database if exists "+name+" with (force)")
		_, _ = admin.Exec(ctx, "drop role if exists "+name)
	})
	u := mustURL(t, dsn)
	u.User, u.Path = url.UserPassword(name, "pw"), "/"+name
	target := u.String()
	su := mustURL(t, dsn)
	su.Path = "/" + name
	super, err := pgx.Connect(ctx, su.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := super.Exec(ctx, "create extension vector"); err != nil {
		t.Fatal(err)
	}
	super.Close(ctx)

	home := t.TempDir()
	for k, v := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state"} {
		t.Setenv(k, filepath.Join(home, v))
	}
	t.Setenv("OPENNEKO_BACKUP_DIR", "")
	t.Setenv("OPENNEKO_BACKUP_KEY_FILE", "")
	t.Setenv("OPENNEKO_DATABASE_URL_FILE", "")
	if err := config.WriteLocalDatabaseURL("", target); err != nil {
		t.Fatal(err)
	}
	settings := installation.Settings{Version: installation.CurrentVersion, Mode: "prod", WebPort: 3999, WebBindAddress: "127.0.0.1", Runtime: installation.RuntimeNative, WebDir: filepath.Join(home, "web")}
	if err := installation.Save(config.Dir(""), settings); err != nil {
		t.Fatal(err)
	}
	root := nativeLayout().Root
	writeTree(t, root, map[string]string{"files/u1/report.pdf": "before", "logs/web.log": "log"})
	return nativeFixture{name: name, target: target, root: root, home: home, settings: settings}
}

func TestNativeBackupAndRestore(t *testing.T) {
	f := newNativeFixture(t)
	ctx := context.Background()
	name, target, root, home, settings := f.name, f.target, f.root, f.home, f.settings

	conn, err := pgx.Connect(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "create table notes (body text); insert into notes values ('before')"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	file, err := createNativeBackup(ctx, "", &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(file, filepath.Join(home, "data", "openneko", "backups")) {
		t.Errorf("backup at %s", file)
	}

	if _, err := conn.Exec(ctx, "update notes set body = 'after'; create table extra (id int)"); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{"files/u1/report.pdf": "after", "files/u1/new.txt": "new"})
	conn.Close(ctx)

	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(name + "\n"))
	out.Reset()
	cmd.SetOut(&out)
	if err := runNativeRestore(ctx, cmd, file, "", true); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "public.extra") || !strings.Contains(out.String(), "`openneko start` starts OpenNeko") {
		t.Errorf("restore output:\n%s", out.String())
	}
	conn, err = pgx.Connect(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var body string
	if err := conn.QueryRow(ctx, "select body from notes").Scan(&body); err != nil || body != "before" {
		t.Errorf("notes = %q, %v", body, err)
	}
	for p, want := range map[string]string{"files/u1/report.pdf": "before", "files/u1/new.txt": "missing", "logs/web.log": "log"} {
		if got := readFileOr(filepath.Join(root, p), "missing"); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	if s, ok := nativeInstall(); !ok || s.WebDir != settings.WebDir {
		t.Errorf("installation.json changed: %+v", s)
	}
	if got, err := nativeDatabaseURL(); err != nil || got != target {
		t.Errorf("database URL after restore: %v", err)
	}
}

// TestNativeBackupNamesAnOldClient gives backup and restore a pg_dump and a
// pg_restore one major version older than the server, the way a runner's
// stock client tools are.
func TestNativeBackupNamesAnOldClient(t *testing.T) {
	f := newNativeFixture(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.target)
	if err != nil {
		t.Fatal(err)
	}
	var server string
	err = conn.QueryRow(ctx, "select current_setting('server_version')").Scan(&server)
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	major, _ := strconv.Atoi(majorOf(server))
	old := fmt.Sprintf("%d.9", major-1)
	var out bytes.Buffer
	file, err := createNativeBackup(ctx, "", &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	bin := t.TempDir()
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then echo '%[1]s (PostgreSQL) %[2]s (Ubuntu %[2]s-1)'; exit 0; fi\n"+
			"echo '%[1]s: error: server version: %[3]s; %[1]s version: %[2]s' >&2\necho '%[1]s: error: aborting because of server version mismatch' >&2\nexit 1\n", tool, old, server)
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	backups, _ := os.ReadDir(filepath.Dir(file))

	for tool, run := range map[string]func() error{
		"pg_dump": func() error { _, err := createNativeBackup(ctx, "", io.Discard); return err },
		"pg_restore": func() error {
			cmd := &cobra.Command{}
			cmd.SetIn(strings.NewReader(f.name + "\n"))
			cmd.SetOut(io.Discard)
			return runNativeRestore(ctx, cmd, file, "", true)
		},
	} {
		err := run()
		if err == nil {
			t.Fatalf("%s %s: no error", tool, old)
		}
		msg := err.Error()
		for _, want := range []string{tool + " " + old, "(" + server + ")", fmt.Sprintf("install the PostgreSQL %d client tools", major)} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: want %q in the error: %s", tool, want, msg)
			}
		}
		if strings.Contains(msg, "\n") || strings.Contains(msg, "aborting") {
			t.Errorf("%s: the error is not one clear line: %s", tool, msg)
		}
	}
	if after, _ := os.ReadDir(filepath.Dir(file)); len(after) != len(backups) {
		t.Errorf("a failed backup left a file: %d files, then %d", len(backups), len(after))
	}
}
