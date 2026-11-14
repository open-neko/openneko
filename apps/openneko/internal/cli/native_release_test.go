package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type tarEntry struct {
	name, body string
	kind       byte
}

func nativeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: kind, Mode: 0o755, Size: int64(len(e.body))}
		if kind != tar.TypeReg {
			hdr.Size = 0
			hdr.Linkname = "/etc/passwd"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func goodNativeEntries() []tarEntry {
	return []tarEntry{
		{name: "bin/", kind: tar.TypeDir},
		{name: "bin/openneko", body: "#!/bin/sh\necho 9.9.9\n"},
		{name: "share/openneko/web/", kind: tar.TypeDir},
		{name: "share/openneko/web/index.html", body: "<html></html>"},
		{name: "share/openneko/web/assets/app.js", body: "1"},
	}
}

func extractEntries(t *testing.T, entries []tarEntry) (string, error) {
	t.Helper()
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(archive, nativeTarGz(t, entries), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "release")
	return dst, extractNativeArchive(archive, dst)
}

func TestExtractNativeArchiveTakesTheReleaseFiles(t *testing.T) {
	dst, err := extractEntries(t, goodNativeEntries())
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dst, "bin", "openneko"))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("bin/openneko: %v %v", fi, err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "share", "openneko", "web", "assets", "app.js")); err != nil || string(b) != "1" {
		t.Fatalf("web file: %q %v", b, err)
	}
}

func TestExtractNativeArchiveRefusesUnsafeEntries(t *testing.T) {
	cases := map[string]tarEntry{
		"parent":     {name: "../evil", body: "x"},
		"absolute":   {name: "/etc/evil", body: "x"},
		"inner dots": {name: "share/openneko/web/../../../evil", body: "x"},
		"other path": {name: "etc/openneko.conf", body: "x"},
		"symlink":    {name: "share/openneko/web/link", kind: tar.TypeSymlink},
		"hard link":  {name: "share/openneko/web/link", kind: tar.TypeLink},
		"twice":      {name: "bin/openneko", body: "again"},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := extractEntries(t, append(goodNativeEntries(), bad)); err == nil {
				t.Fatal("the archive was accepted")
			}
		})
	}
	t.Run("no executable", func(t *testing.T) {
		if _, err := extractEntries(t, goodNativeEntries()[2:]); err == nil || !strings.Contains(err.Error(), "no bin/openneko") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDownloadNativeReleaseChecksTheChecksum(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native release downloads are Linux only")
	}
	archive := nativeTarGz(t, goodNativeEntries())
	name := nativeArchiveName("v9.9.9", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v9.9.9/" + nativeChecksumsAsset:
			_, _ = w.Write([]byte(sums))
		case "/v9.9.9/" + name:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("OPENNEKO_RELEASE_URL", srv.URL)

	if _, err := resolveNativeTarget(context.Background(), ""); err == nil {
		t.Fatal("latest resolved with a mirror set")
	}
	target, err := resolveNativeTarget(context.Background(), "9.9.9")
	if err != nil || target != "v9.9.9" {
		t.Fatalf("target %q %v", target, err)
	}
	parent := t.TempDir()
	release, err := downloadNativeRelease(context.Background(), target, parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(release, "share", "openneko", "web", "index.html")); err != nil {
		t.Fatal(err)
	}

	sums = strings.Repeat("0", 64) + "  " + name + "\n"
	if _, err := downloadNativeRelease(context.Background(), target, parent); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(parent, ".openneko-release-*"))
	if len(left) != 1 {
		t.Fatalf("a failed download left folders behind: %v", left)
	}
}
