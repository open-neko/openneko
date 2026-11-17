package webfront

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/native"
)

func graphjinPath(configFile, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(configFile), p)
}

func TestManagedPathsResolveBesideTheConfig(t *testing.T) {
	t.Setenv("OPENNEKO_GRAPHJIN_FILES_LOCAL_DIR", "")
	update, err := buildGraphjinConfigUpdate(jsObjOf("action", "register_source", "name", "orders", "kind", "api",
		"specAssetId", "88888888-8888-4888-8888-888888888888"), nil, configUpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	specsDir := jsStr(update.vals["update_sources"].([]any)[0].(*jsObj).vals["specs_dir"])
	root, err := managedFileSourceRuntimeRoot("org", "docs", "files")
	if err != nil {
		t.Fatal(err)
	}
	for name, configFile := range map[string]string{
		"docker": filepath.Join(t.TempDir(), "config", "graphjin", "agentic.yml"),
		"native": native.Layout{Root: t.TempDir()}.GraphJin(),
	} {
		if got, want := graphjinPath(configFile, specsDir), filepath.Join(filepath.Dir(configFile), "specs"); got != want {
			t.Errorf("%s: specs_dir %q resolves to %s, the specs are in %s", name, specsDir, got, want)
		}
		stored, err := newManagedSource(configFile, "org", "docs").ensureDir()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stored, "a.csv"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(graphjinPath(configFile, root), "a.csv")); err != nil {
			t.Errorf("%s: root %q does not hold the stored file: %v", name, root, err)
		}
	}
}
