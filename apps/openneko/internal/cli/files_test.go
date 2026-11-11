package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dosco/graphjin/core/v3/fstable"

	"github.com/open-neko/neko/apps/openneko/internal/filestore"
)

func TestCopyToStore(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("orgs/o1/users/alice/policies/travel.md", "per diem")
	write("orgs/o1/users/bob/notes.md", "bob")
	write("orgs/o1/users/bob/.gj-fstable-123", "half a write")
	write("stray.txt", "not a user file")
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "orgs/o1/users/bob/link")); err != nil {
		t.Fatal(err)
	}

	dst, err := filestore.Open(ctx, filestore.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	copied, skipped, err := copyToStore(ctx, src, dst)
	if err != nil || copied != 2 || skipped != 0 {
		t.Fatalf("first copy: %d %d %v", copied, skipped, err)
	}
	body, _, err := dst.Get(ctx, "orgs/o1/users/alice/policies/travel.md")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "per diem" {
		t.Fatalf("copied %q", data)
	}
	es, _, _ := dst.List(ctx, fstable.ListOpts{})
	for _, e := range es {
		if !strings.HasPrefix(e.Key, "orgs/") || strings.Contains(e.Key, ".gj-fstable-") || strings.HasSuffix(e.Key, "/link") {
			t.Fatalf("copied %s", e.Key)
		}
	}

	write("orgs/o1/users/bob/notes.md", "bob, longer")
	copied, skipped, err = copyToStore(ctx, src, dst)
	if err != nil || copied != 1 || skipped != 1 {
		t.Fatalf("second copy: %d %d %v", copied, skipped, err)
	}

	if copied, skipped, err := copyToStore(ctx, t.TempDir(), dst); err != nil || copied+skipped != 0 {
		t.Fatalf("empty folder: %d %d %v", copied, skipped, err)
	}
}
