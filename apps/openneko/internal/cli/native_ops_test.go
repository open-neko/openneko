package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
	if err := archiveFolder(src, include, &archive); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	writeTree(t, dst, map[string]string{"files/old.txt": "gone", "logs/web.log": "kept"})
	if err := restoreFolder(dst, include, &archive); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"files/u1/a.txt": "A", "graphjin/agentic.yml": "G", "logs/web.log": "kept", "files/old.txt": "missing", "files/link": "missing"} {
		if got := readFileOr(filepath.Join(dst, name), "missing"); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	var empty bytes.Buffer
	if err := archiveFolder(filepath.Join(src, "absent"), include, &empty); err != nil || empty.Len() == 0 {
		t.Errorf("a missing folder gives an empty archive: %d bytes, %v", empty.Len(), err)
	}
}

func TestRestoreFolderRefusesAnEscape(t *testing.T) {
	raw := testVolumeTar(t, map[string]string{"../escape.txt": "x"})
	err := restoreFolder(t.TempDir(), func(string) bool { return true }, bytes.NewReader(raw))
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

func TestNativeBackupAndRestore(t *testing.T) {
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
