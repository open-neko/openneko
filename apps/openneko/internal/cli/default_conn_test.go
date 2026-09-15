package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConnEnvOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "openneko"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"pg":{"host":"neko-db","port":5432,"password":"rotated"}}`
	if err := os.WriteFile(filepath.Join(dir, "openneko", "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("NEKO_PG_HOST", "127.0.0.1")
	t.Setenv("NEKO_PG_PORT", "55432")
	t.Setenv("NEKO_PG_PASSWORD", "")

	if conn := defaultConn(); conn.Host != "neko-db" || conn.Port != 5432 {
		t.Fatalf("config.json should win without the override: %+v", conn)
	}
	t.Setenv("OPENNEKO_PG_ENV_OVERRIDE", "1")
	conn := defaultConn()
	if conn.Host != "127.0.0.1" || conn.Port != 55432 || conn.Password != "rotated" {
		t.Fatalf("set env vars should win and the rotated password should stay: %+v", conn)
	}
}
