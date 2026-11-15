package cli

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"testing"
)

func TestCLISourcesAreGofmt(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("Go files: %v %v", files, err)
	}
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		formatted, err := format.Source(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(src, formatted) {
			t.Errorf("%s is not gofmt; run gofmt -w on it", name)
		}
	}
}
