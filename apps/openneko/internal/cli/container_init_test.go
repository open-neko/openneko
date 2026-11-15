package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func clearContainerEnv(t *testing.T) {
	for _, k := range []string{"NEKO_PG_HOST", "NEKO_PG_PORT", "NEKO_PG_USER", "NEKO_PG_PASSWORD", "NEKO_PG_DATABASE", "NEKO_PG_SSLMODE", "NEKO_SECRET_KEY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func readPg(t *testing.T, dir string) (map[string]any, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	pg, _ := doc["pg"].(map[string]any)
	return doc, pg
}

func TestSeedContainerConfigKeepsExistingValues(t *testing.T) {
	clearContainerEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"secretsBackend":"file","pg":{"host":"db","password":"rotated"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEKO_PG_HOST", "other")
	t.Setenv("NEKO_PG_PASSWORD", "initial")
	t.Setenv("NEKO_PG_PORT", "6543")
	t.Setenv("NEKO_SECRET_KEY", "a2V5")

	if err := seedContainerConfig(dir, false); err != nil {
		t.Fatal(err)
	}
	doc, pg := readPg(t, dir)
	if doc["secretsBackend"] != "file" || pg["host"] != "db" || pg["password"] != "rotated" || pg["port"] != float64(6543) {
		t.Fatalf("config = %v", doc)
	}
	key, err := os.ReadFile(filepath.Join(dir, "secret-key"))
	if err != nil || string(key) != "a2V5" {
		t.Fatalf("secret-key = %q, %v", key, err)
	}

	t.Setenv("NEKO_SECRET_KEY", "bmV3")
	if err := seedContainerConfig(dir, false); err != nil {
		t.Fatal(err)
	}
	if key, _ := os.ReadFile(filepath.Join(dir, "secret-key")); string(key) != "a2V5" {
		t.Fatalf("secret-key changed to %q", key)
	}

	if err := seedContainerConfig(dir, true); err != nil {
		t.Fatal(err)
	}
	_, pg = readPg(t, dir)
	if pg["host"] != "other" || pg["password"] != "initial" {
		t.Fatalf("force did not apply env: %v", pg)
	}
	if key, _ := os.ReadFile(filepath.Join(dir, "secret-key")); string(key) != "bmV3" {
		t.Fatalf("force secret-key = %q", key)
	}
	for _, name := range []string{"config.json", "secret-key"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v", name, info.Mode(), err)
		}
	}
}

func TestSeedContainerConfigWithNoEnv(t *testing.T) {
	clearContainerEnv(t)
	dir := t.TempDir()
	if err := seedContainerConfig(dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("config.json written with no env: %v", err)
	}
	key, err := os.ReadFile(filepath.Join(dir, "secret-key"))
	if err != nil || len(key) != 44 {
		t.Fatalf("secret-key = %q, %v", key, err)
	}
	t.Setenv("NEKO_PG_PORT", "x")
	if err := seedContainerConfig(dir, false); err == nil {
		t.Fatal("bad port accepted")
	}
}
