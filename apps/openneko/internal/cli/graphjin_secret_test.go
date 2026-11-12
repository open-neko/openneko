package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/gjconfig"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const jwtConfig = `# sources mode
auth:
  type: jwt
  jwt:
    # per-org actor-token secret
    secret: "` + gjconfig.Placeholder + `"
`

func TestPrepareGraphjinAuthCreatesOneOrgAndWritesItsSecret(t *testing.T) {
	url := testpg.URL(t)
	ctx := context.Background()
	base, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("gj_secret_%d", time.Now().UnixNano())
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
	defer mig.Close(ctx)
	if _, err := (&db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}).Apply(ctx, mig, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "secret-key"), []byte("test-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gj := filepath.Join(t.TempDir(), "agentic.yml")
	if err := os.WriteFile(gj, []byte(jwtConfig), 0o640); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := prepareGraphjinAuth(ctx, &out, conn, configDir, gj); err != nil {
		t.Fatal(err)
	}
	var orgID string
	if err := mig.QueryRow(ctx, "select id::text from organization").Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	secret, err := gjconfig.SigningSecretB64(configDir, orgID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(gj)
	if !strings.Contains(string(got), `secret: "`+secret+`"`) {
		t.Fatalf("secret for %s not written:\n%s", orgID, got)
	}
	if info, _ := os.Stat(gj); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
	if !strings.Contains(out.String(), "created organization "+orgID) {
		t.Fatalf("output: %s", out.String())
	}

	if err := os.WriteFile(filepath.Join(configDir, "secret-key"), []byte("rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := prepareGraphjinAuth(ctx, &out, conn, configDir, gj); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := mig.QueryRow(ctx, "select count(*) from organization").Scan(&n); err != nil || n != 1 {
		t.Fatalf("organizations: %d %v", n, err)
	}
	rotated, _ := gjconfig.SigningSecretB64(configDir, orgID)
	got, _ = os.ReadFile(gj)
	if rotated == secret || !strings.Contains(string(got), `secret: "`+rotated+`"`) {
		t.Fatal("a rotated secret-key did not repair the JWT secret")
	}
	entries, _ := os.ReadDir(filepath.Dir(gj))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}

	out.Reset()
	if err := prepareGraphjinAuth(ctx, &out, conn, configDir, gj); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(gj); !bytes.Equal(again, got) || out.Len() != 0 {
		t.Fatalf("third run changed something: %q", out.String())
	}
}

func TestGraphjinSigningSecretMatchesTheWorker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret-key"), []byte("test-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := gjconfig.SigningSecretB64(dir, "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ibno238bJn61Spt+F4HgTaSuQgtchJjpDNJgQzGH+qM="; got != want {
		t.Fatalf("got %s, want %s (graphjinSigningSecretB64 in packages/llm)", got, want)
	}
}
