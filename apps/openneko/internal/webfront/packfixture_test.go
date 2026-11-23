package webfront

import (
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

func seedBundledPack(t *testing.T, db *pgxpool.Pool, packID string) *packs.Bundle {
	t.Helper()
	abs, err := filepath.Abs("../../../../packs")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_PACKS_DIR", abs)
	bundle, err := packBundle("org", packID, "")
	if err != nil {
		t.Fatal(err)
	}
	var install string
	if err := db.QueryRow(t.Context(), `insert into pack_install (org_id, pack_id, version, status, manifest_hash)
		values ('org', $1, $2, 'installed', 'h') returning id::text`, packID, bundle.Manifest.Metadata.Version).Scan(&install); err != nil {
		t.Fatal(err)
	}
	for _, art := range bundle.Artifacts {
		seed(t, db, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash,
			last_applied_hash, ownership, readiness, metadata) values ($1, 'org', $2, $3, $4, $5, $5, 'managed', 'ready', $6::jsonb)`,
			install, string(art.Kind), art.Key, art.TargetRef, art.Hash, packArtifactMetadata(bundle, art, map[string]string{}, art.TargetRef))
	}
	return bundle
}
