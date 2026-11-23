package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/ownonly"
)

func TestSecretTempFilesStayInTheOwnersDataFolder(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "d"))
	want := nativeLayout().Tmp()
	f, err := secretTemp("openneko-move-url-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	spool, err := spoolArchive(strings.NewReader("archive"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(spool) })
	for _, path := range []string{f.Name(), spool} {
		if filepath.Dir(path) != want {
			t.Errorf("%s is outside %s", path, want)
		}
		if err := ownonly.Verify(path); err != nil {
			t.Error(err)
		}
	}
}
