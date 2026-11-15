package assets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedMigrationsMatchTheSource(t *testing.T) {
	src := filepath.Join(repoRootForTest(t), "db", "migrations")
	dst := "migrations"
	names := map[string]bool{}
	for _, dir := range []string{src, dst} {
		found, err := filepath.Glob(filepath.Join(dir, "*.sql"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range found {
			names[filepath.Base(f)] = true
		}
	}
	if len(names) == 0 {
		t.Fatal("no migrations found")
	}
	for name := range names {
		a, errA := os.ReadFile(filepath.Join(src, name))
		b, errB := os.ReadFile(filepath.Join(dst, name))
		if errA != nil || errB != nil || !bytes.Equal(a, b) {
			t.Errorf("%s differs between db/migrations and assets/migrations; run scripts/sync-migrations.sh", name)
		}
	}
}
