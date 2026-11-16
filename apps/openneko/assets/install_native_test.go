package assets

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallNativeUpgradeRunsTheNewExecutable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("install-native.sh is for Linux")
	}
	for _, tool := range []string{"curl", "sha256sum", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is needed: %v", tool, err)
		}
	}
	if _, err := os.Stat("/etc/openneko/server.json"); err == nil {
		t.Skip("this host has a server install")
	}
	work := t.TempDir()
	record := filepath.Join(work, "ran")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{
		{"bin/openneko", "#!/bin/sh\necho \"$0 $*\" > " + record + "\n"},
		{"share/openneko/web/index.html", "<html></html>"},
		{"share/openneko/skills/sandbox/xlsx/SKILL.md", "x"},
		{"share/openneko/later/a.txt", "x"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
	if arch == "" {
		t.Skip("no native release for " + runtime.GOARCH)
	}
	name := "openneko-native_9.9.9_linux_" + arch + ".tar.gz"
	sum := sha256.Sum256(buf.Bytes())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v9.9.9/" + name:
			_, _ = w.Write(buf.Bytes())
		case "/v9.9.9/checksums-native.txt":
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + name + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	prefix := filepath.Join(work, "opt")
	installed := filepath.Join(prefix, "bin", "openneko")
	run := func(args ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{filepath.Join(repoRootForTest(t), "scripts", "install-native.sh")}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+work, "OPENNEKO_PREFIX="+prefix, "OPENNEKO_BIN_DIR="+filepath.Join(work, "bin"),
			"OPENNEKO_RELEASE_URL="+srv.URL, "OPENNEKO_VERSION=9.9.9")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("--upgrade"); err == nil || !strings.Contains(out, "no OpenNeko in") {
		t.Fatalf("no install: %v %s", err, out)
	}
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err == nil || !strings.Contains(out, "--upgrade") {
		t.Fatalf("install over an install: %v %s", err, out)
	}
	out, err := run("--upgrade", "--skip-backup")
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	ran, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the new executable did not run: %v %s", err, out)
	}
	fields := strings.Fields(string(ran))
	if len(fields) != 7 || !strings.HasSuffix(fields[0], "/release/bin/openneko") || fields[1] != "upgrade" || fields[2] != "--from" ||
		!strings.HasSuffix(fields[3], "/release") || fields[4] != "--exe" || fields[5] != installed || fields[6] != "--skip-backup" {
		t.Fatalf("the new executable ran as %q", ran)
	}
	if b, _ := os.ReadFile(installed); string(b) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("the script changed the installed executable itself: %q", b)
	}
}
