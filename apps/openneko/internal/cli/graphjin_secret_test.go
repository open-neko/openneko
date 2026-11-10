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
)

const jwtConfig = `# sources mode
auth:
  type: jwt
  jwt:
    # per-org actor-token secret
    secret: "` + graphjinSecretPlaceholder + `"
`

func TestPatchGraphjinJWTSecret(t *testing.T) {
	seeded, changed := patchGraphjinJWTSecret(jwtConfig, "old==")
	if !changed || !strings.Contains(seeded, `secret: "old=="`) || strings.Contains(seeded, graphjinSecretPlaceholder) {
		t.Fatalf("placeholder not replaced:\n%s", seeded)
	}
	repaired, changed := patchGraphjinJWTSecret(seeded, "new==")
	if !changed || !strings.Contains(repaired, `secret: "new=="`) || strings.Contains(repaired, "old==") {
		t.Fatalf("stale secret not replaced:\n%s", repaired)
	}
	if again, changed := patchGraphjinJWTSecret(repaired, "new=="); changed || again != repaired {
		t.Fatal("a current secret changed the file")
	}
	anon := "auth:\n  type: none\n"
	if got, changed := patchGraphjinJWTSecret(anon, "s=="); changed || got != anon {
		t.Fatal("a config without jwt auth changed")
	}
}

func TestMigrateGraphjinSystemSource(t *testing.T) {
	legacy := `mode: agentic
sources:
  - name: graphjin
    kind: graphjin
    capabilities:
      config.write: true
  - name: adventureworks
    kind: database
    default: true
`
	got, changed, err := editGraphjinYAML(legacy, migrateGraphjinSystemSource)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	for _, want := range []string{"name: adventureworks", "system:", "catalog.read: true", "raw_graphql.mutate: false", "gj_catalog: authenticated", "gj_config: admin"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "kind: graphjin") {
		t.Fatalf("legacy source kept:\n%s", got)
	}

	explicit := `mode: agentic
system:
  capabilities:
    config.write: false
  root_access:
    gj_catalog: admin
sources:
  - name: graphjin
    kind: graphjin
`
	got, _, err = editGraphjinYAML(explicit, migrateGraphjinSystemSource)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"config.write: false", "gj_catalog: admin", "runtime.read: true", "gj_runtime: admin"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}

	current := "mode: agentic\nsources:\n  - name: app\n    kind: database\n"
	if got, changed, err := editGraphjinYAML(current, migrateGraphjinSystemSource); err != nil || changed || got != current {
		t.Fatalf("a current config changed: %v %v", changed, err)
	}
}

func TestReconcileGraphjinWritePolicy(t *testing.T) {
	base := `mode: agentic
mcp:
  allow_mutations: false
  allow_raw_queries: true
roles:
  - name: user
    comment: Authenticated user
system:
  capabilities:
    raw_graphql.query: true
    raw_graphql.mutate: false
sources: []
`
	first, changed, err := editGraphjinYAML(base, reconcileGraphjinWritePolicy)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	for _, want := range []string{"allow_mutations: true", "raw_graphql.mutate: true", "name: member", "name: service", "allow_raw_queries: true", "name: user"} {
		if !strings.Contains(first, want) {
			t.Fatalf("missing %q in\n%s", want, first)
		}
	}
	if second, changed, err := editGraphjinYAML(first, reconcileGraphjinWritePolicy); err != nil || changed || second != first {
		t.Fatalf("second pass changed the file: %v %v", changed, err)
	}

	sparse, changed, err := editGraphjinYAML("mode: agentic\nsources: []\n", reconcileGraphjinWritePolicy)
	if err != nil || !changed || !strings.Contains(sparse, "allow_mutations: true") || !strings.Contains(sparse, "raw_graphql.mutate: true") || !strings.Contains(sparse, "name: service") {
		t.Fatalf("sparse config: %v %v\n%s", changed, err, sparse)
	}
	legacy := "database:\n  type: postgres\n"
	if got, changed, err := editGraphjinYAML(legacy, reconcileGraphjinWritePolicy); err != nil || changed || got != legacy {
		t.Fatal("a config without sources changed")
	}
	if _, _, err := editGraphjinYAML("sources: []\nroles: x\n", reconcileGraphjinWritePolicy); err == nil {
		t.Fatal("roles as a scalar passed")
	}
}

func TestShippedGraphjinSeedsNeedOnlyTheSecret(t *testing.T) {
	for _, seed := range []string{"customer.sources.example.yml", "dev.sources.example.yml", "dev.example.yml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "db", "graphjin", seed))
		if err != nil {
			t.Fatal(err)
		}
		r, err := reconcileGraphjinConfig(string(raw), "c2VjcmV0")
		if err != nil {
			t.Fatalf("%s: %v", seed, err)
		}
		if r.system || r.writePolicy {
			t.Fatalf("%s: a new install would rewrite the YAML (system=%v writePolicy=%v)", seed, r.system, r.writePolicy)
		}
		want := strings.ReplaceAll(string(raw), graphjinSecretPlaceholder, "c2VjcmV0")
		if r.content != want {
			t.Fatalf("%s: more than the secret changed", seed)
		}
	}
}

func TestPrepareGraphjinAuthCreatesOneOrgAndWritesItsSecret(t *testing.T) {
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
	secret, err := graphjinSigningSecretB64(configDir, orgID)
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
	rotated, _ := graphjinSigningSecretB64(configDir, orgID)
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
	got, err := graphjinSigningSecretB64(dir, "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ibno238bJn61Spt+F4HgTaSuQgtchJjpDNJgQzGH+qM="; got != want {
		t.Fatalf("got %s, want %s (graphjinSigningSecretB64 in packages/llm)", got, want)
	}
}
