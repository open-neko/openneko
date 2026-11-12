package gjconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const jwtConfig = `# sources mode
auth:
  type: jwt
  jwt:
    # per-org actor-token secret
    secret: "` + Placeholder + `"
`

func TestPatchGraphjinJWTSecret(t *testing.T) {
	seeded, changed := PatchJWTSecret(jwtConfig, "old==")
	if !changed || !strings.Contains(seeded, `secret: "old=="`) || strings.Contains(seeded, Placeholder) {
		t.Fatalf("placeholder not replaced:\n%s", seeded)
	}
	repaired, changed := PatchJWTSecret(seeded, "new==")
	if !changed || !strings.Contains(repaired, `secret: "new=="`) || strings.Contains(repaired, "old==") {
		t.Fatalf("stale secret not replaced:\n%s", repaired)
	}
	if again, changed := PatchJWTSecret(repaired, "new=="); changed || again != repaired {
		t.Fatal("a current secret changed the file")
	}
	anon := "auth:\n  type: none\n"
	if got, changed := PatchJWTSecret(anon, "s=="); changed || got != anon {
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
	got, changed, err := editYAML(legacy, migrateSystemSource)
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
	got, _, err = editYAML(explicit, migrateSystemSource)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"config.write: false", "gj_catalog: admin", "runtime.read: true", "gj_runtime: admin"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}

	current := "mode: agentic\nsources:\n  - name: app\n    kind: database\n"
	if got, changed, err := editYAML(current, migrateSystemSource); err != nil || changed || got != current {
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
	first, changed, err := editYAML(base, reconcileWritePolicy)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	for _, want := range []string{"allow_mutations: true", "raw_graphql.mutate: true", "name: member", "name: service", "allow_raw_queries: true", "name: user"} {
		if !strings.Contains(first, want) {
			t.Fatalf("missing %q in\n%s", want, first)
		}
	}
	if second, changed, err := editYAML(first, reconcileWritePolicy); err != nil || changed || second != first {
		t.Fatalf("second pass changed the file: %v %v", changed, err)
	}

	sparse, changed, err := editYAML("mode: agentic\nsources: []\n", reconcileWritePolicy)
	if err != nil || !changed || !strings.Contains(sparse, "allow_mutations: true") || !strings.Contains(sparse, "raw_graphql.mutate: true") || !strings.Contains(sparse, "name: service") {
		t.Fatalf("sparse config: %v %v\n%s", changed, err, sparse)
	}
	legacy := "database:\n  type: postgres\n"
	if got, changed, err := editYAML(legacy, reconcileWritePolicy); err != nil || changed || got != legacy {
		t.Fatal("a config without sources changed")
	}
	if _, _, err := editYAML("sources: []\nroles: x\n", reconcileWritePolicy); err == nil {
		t.Fatal("roles as a scalar passed")
	}
}

func TestShippedGraphjinSeedsNeedOnlyTheSecret(t *testing.T) {
	for _, seed := range []string{"customer.sources.example.yml", "dev.sources.example.yml", "dev.example.yml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "db", "graphjin", seed))
		if err != nil {
			t.Fatal(err)
		}
		r, err := Reconcile(string(raw), "c2VjcmV0")
		if err != nil {
			t.Fatalf("%s: %v", seed, err)
		}
		if r.System || r.WritePolicy {
			t.Fatalf("%s: a new install would rewrite the YAML (system=%v writePolicy=%v)", seed, r.System, r.WritePolicy)
		}
		want := strings.ReplaceAll(string(raw), Placeholder, "c2VjcmV0")
		if r.Content != want {
			t.Fatalf("%s: more than the secret changed", seed)
		}
	}
}
