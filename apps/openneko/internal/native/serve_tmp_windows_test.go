package native

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServeTempFilesStayInTheDataFolder(t *testing.T) {
	l := Layout{Root: filepath.Join(t.TempDir(), "root")}
	env := ServeEnv(l)
	for _, k := range []string{"TMP", "TEMP"} {
		if env[k] != l.Tmp() {
			t.Fatalf("serve's %s is %q, want %q", k, env[k], l.Tmp())
		}
		t.Setenv(k, env[k])
	}
	if got := os.TempDir(); got != l.Tmp() {
		t.Fatalf("serve's temp folder is %s, want %s", got, l.Tmp())
	}
}
