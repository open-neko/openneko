package harness

import (
	"bytes"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleSourcesAreGofmt(t *testing.T) {
	checked := 0
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != ".." && (n == "testdata" || n == "node_modules" || strings.HasPrefix(n, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		formatted, err := format.Source(src)
		if err != nil {
			t.Errorf("%s: %v", path, err)
		} else if !bytes.Equal(src, formatted) {
			t.Errorf("%s is not gofmt; run gofmt -w on it", path)
		}
		checked++
		return nil
	})
	if err != nil || checked < 20 {
		t.Fatalf("walked %d Go files: %v", checked, err)
	}
}
