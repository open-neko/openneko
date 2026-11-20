package assets

import (
	"archive/tar"
	"archive/zip"
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

type releaseFile struct{ name, body string }

func nativeArchive(t *testing.T, windows bool, files []releaseFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	if windows {
		zw := zip.NewWriter(&buf)
		for _, f := range files {
			h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
			h.SetMode(0o755)
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(f.body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
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
	return buf.Bytes()
}

func serveNativeRelease(t *testing.T, name string, archive []byte, sum string) string {
	t.Helper()
	if sum == "" {
		raw := sha256.Sum256(archive)
		sum = hex.EncodeToString(raw[:])
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v9.9.9/" + name:
			_, _ = w.Write(archive)
		case "/v9.9.9/checksums-native.txt":
			_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  other.tar.gz\n" + sum + "  " + name + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func hostArch(t *testing.T) string {
	t.Helper()
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
	if arch == "" {
		t.Skip("no native release for " + runtime.GOARCH)
	}
	return arch
}

func needTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is needed: %v", tool, err)
		}
	}
}

func runInstallNative(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(repoRootForTest(t), "scripts", "install-native.sh")}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallNativeUpgradeRunsTheNewExecutable(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install-native.sh is for Linux and macOS")
	}
	needTools(t, "curl", "tar")
	if _, err := os.Stat("/etc/openneko/server.json"); err == nil {
		t.Skip("this host has a server install")
	}
	work := t.TempDir()
	record := filepath.Join(work, "ran")
	archive := nativeArchive(t, false, []releaseFile{
		{"bin/openneko", "#!/bin/sh\necho \"$0 $*\" > " + record + "\n"},
		{"share/openneko/web/index.html", "<html></html>"},
		{"share/openneko/skills/sandbox/xlsx/SKILL.md", "x"},
		{"share/openneko/later/a.txt", "x"},
	})
	name := "openneko-native_9.9.9_" + runtime.GOOS + "_" + hostArch(t) + ".tar.gz"
	url := serveNativeRelease(t, name, archive, "")

	prefix := filepath.Join(work, "opt")
	installed := filepath.Join(prefix, "bin", "openneko")
	env := []string{"HOME=" + work, "OPENNEKO_PREFIX=" + prefix, "OPENNEKO_BIN_DIR=" + filepath.Join(work, "bin"),
		"OPENNEKO_RELEASE_URL=" + url, "OPENNEKO_VERSION=9.9.9"}
	if out, err := runInstallNative(t, env, "--upgrade"); err == nil || !strings.Contains(out, "no OpenNeko in") {
		t.Fatalf("no install: %v %s", err, out)
	}
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := runInstallNative(t, env); err == nil || !strings.Contains(out, "--upgrade") {
		t.Fatalf("install over an install: %v %s", err, out)
	}
	out, err := runInstallNative(t, env, "--upgrade", "--skip-backup")
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

// macOSTools gives the script a PATH that looks like macOS to it: uname and
// sysctl answer as an Apple silicon Mac under Rosetta, and only shasum hashes.
func macOSTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"sh", "curl", "tar", "gzip", "grep", "sed", "cut", "cat", "mkdir", "mktemp", "ln", "chmod", "rm", "id", "shasum", "perl"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is needed to emulate macOS: %v", tool, err)
		}
		if err := os.Symlink(path, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"uname":  "#!/bin/sh\ncase \"$1\" in -s) echo Darwin ;; -m) echo x86_64 ;; *) exit 1 ;; esac\n",
		"sysctl": "#!/bin/sh\n[ \"$*\" = \"-n hw.optional.arm64\" ] && echo 1\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInstallNativeFreshInstall(t *testing.T) {
	type target struct {
		goos, arch, path string
	}
	var targets []target
	switch runtime.GOOS {
	case "linux":
		targets = []target{{"linux", hostArch(t), ""}, {"darwin", "arm64", macOSTools(t)}}
	case "darwin":
		targets = []target{{"darwin", hostArch(t), ""}}
	default:
		t.Skip("install-native.sh is for Linux and macOS")
	}
	needTools(t, "curl", "tar")
	for _, tc := range targets {
		t.Run(tc.goos, func(t *testing.T) {
			work := t.TempDir()
			record := filepath.Join(work, "ran")
			archive := nativeArchive(t, false, []releaseFile{
				{"bin/openneko", "#!/bin/sh\necho \"$*\" >> " + record + "\n[ \"$1\" != release-check ] || [ ! -e " + filepath.Join(work, "bad") + " ]\n"},
				{"share/openneko/web/index.html", "<html></html>"},
				{"extra/x.txt", "x"},
			})
			name := "openneko-native_9.9.9_" + tc.goos + "_" + tc.arch + ".tar.gz"
			prefix := filepath.Join(work, "opt")
			bindir := filepath.Join(work, "bin")
			install := func(sum string) (string, error) {
				env := []string{"HOME=" + work, "OPENNEKO_PREFIX=" + prefix, "OPENNEKO_BIN_DIR=" + bindir,
					"OPENNEKO_RELEASE_URL=" + serveNativeRelease(t, name, archive, sum), "OPENNEKO_VERSION=9.9.9"}
				if tc.path != "" {
					env = append(env, "PATH="+tc.path)
				}
				return runInstallNative(t, env)
			}

			if out, err := install(strings.Repeat("a", 64)); err == nil || !strings.Contains(out, "checksum mismatch for "+name) {
				t.Fatalf("a wrong checksum: %v %s", err, out)
			}
			if _, err := os.Stat(prefix); !os.IsNotExist(err) {
				t.Fatalf("a wrong checksum left %s: %v", prefix, err)
			}
			if out, err := install("not-a-sum"); err == nil || !strings.Contains(out, "does not list "+name) {
				t.Fatalf("no checksum line: %v %s", err, out)
			}

			out, err := install("")
			if err != nil {
				t.Fatalf("%v %s", err, out)
			}
			if !strings.Contains(out, "for "+tc.goos+"/"+tc.arch) {
				t.Fatalf("the script did not pick %s/%s: %s", tc.goos, tc.arch, out)
			}
			ran, _ := os.ReadFile(record)
			if string(ran) != "version --short\nrelease-check --skills --bundled-packs\n" {
				t.Fatalf("the installed executable ran as %q", ran)
			}
			if target, err := os.Readlink(filepath.Join(bindir, "openneko")); err != nil || target != filepath.Join(prefix, "bin", "openneko") {
				t.Fatalf("link: %q %v", target, err)
			}
			if _, err := os.Stat(filepath.Join(prefix, "share", "openneko", "web", "index.html")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(prefix, "extra")); !os.IsNotExist(err) {
				t.Fatalf("the script unpacked a folder beside bin and share: %v", err)
			}

			if err := os.RemoveAll(prefix); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(work, "bad"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := install(""); err == nil || !strings.Contains(out, "lacks its bundled skills or packs") {
				t.Fatalf("a failed release-check: %v %s", err, out)
			}
		})
	}
}

func TestInstallNativeRefusesServerOnMacOS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("emulates macOS on Linux")
	}
	work := t.TempDir()
	out, err := runInstallNative(t, []string{"HOME=" + work, "PATH=" + macOSTools(t)}, "--server")
	if err == nil || !strings.Contains(out, "--server is for Linux only") {
		t.Fatalf("%v %s", err, out)
	}
}

func powerShell(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"pwsh", "powershell"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	if runtime.GOOS == "windows" {
		t.Fatal("no PowerShell on Windows")
	}
	t.Skip("no pwsh on PATH")
	return ""
}

func fakeOpenNeko(t *testing.T) []byte {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		gobin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	dir := t.TempDir()
	src := `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	f, err := os.OpenFile(os.Getenv("FAKE_OPENNEKO_RECORD"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(9)
	}
	fmt.Fprintf(f, "%s|%s\n", os.Args[0], strings.Join(os.Args[1:], " "))
	f.Close()
	if len(os.Args) > 1 && os.Args[1] == "release-check" && os.Getenv("FAKE_OPENNEKO_BAD") != "" {
		os.Exit(3)
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fake\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "fake.exe")
	cmd := exec.Command(gobin, "build", "-o", exe, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the fake openneko: %v %s", err, out)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInstallNativePowerShell(t *testing.T) {
	shell := powerShell(t)
	arch := hostArch(t)
	work := t.TempDir()
	record := filepath.Join(work, "ran")
	archive := nativeArchive(t, true, []releaseFile{
		{"bin/openneko.exe", string(fakeOpenNeko(t))},
		{"share/openneko/web/index.html", "<html></html>"},
		{"share/openneko/skills/sandbox/xlsx/SKILL.md", "x"},
	})
	name := "openneko-native_9.9.9_windows_" + arch + ".zip"
	local := filepath.Join(work, "local")
	prefix := filepath.Join(local, "Programs", "OpenNeko")
	exe := filepath.Join(prefix, "bin", "openneko.exe")
	run := func(sum string, extra []string, args ...string) (string, error) {
		cmd := exec.Command(shell, append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
			"-File", filepath.Join(repoRootForTest(t), "scripts", "install-native.ps1")}, args...)...)
		cmd.Env = append(os.Environ(), "LOCALAPPDATA="+local, "PROCESSOR_ARCHITECTURE="+strings.ToUpper(arch), "PROCESSOR_ARCHITEW6432=",
			"OPENNEKO_PREFIX=", "OPENNEKO_RELEASE_URL="+serveNativeRelease(t, name, archive, sum), "OPENNEKO_VERSION=9.9.9",
			"FAKE_OPENNEKO_RECORD="+record)
		cmd.Env = append(cmd.Env, extra...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	ran := func() []string {
		b, _ := os.ReadFile(record)
		_ = os.Remove(record)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}

	if out, err := run("", nil, "-Upgrade"); err == nil || !strings.Contains(out, "no OpenNeko in") {
		t.Fatalf("upgrade with no install: %v %s", err, out)
	}
	if out, err := run(strings.Repeat("a", 64), nil); err == nil || !strings.Contains(out, "checksum mismatch for "+name) {
		t.Fatalf("a wrong checksum: %v %s", err, out)
	}
	if _, err := os.Stat(prefix); !os.IsNotExist(err) {
		t.Fatalf("a wrong checksum left %s: %v", prefix, err)
	}
	if out, err := run("", nil, "--skip-backup"); err == nil || !strings.Contains(out, "the options go after -Upgrade") {
		t.Fatalf("an option with no -Upgrade: %v %s", err, out)
	}

	out, err := run("", nil)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if got := ran(); len(got) != 2 || got[0] != exe+"|version --short" || got[1] != exe+"|release-check --skills --bundled-packs" {
		t.Fatalf("the installed executable ran as %q\n%s", got, out)
	}
	if _, err := os.Stat(filepath.Join(prefix, "share", "openneko", "web", "index.html")); err != nil {
		t.Fatal(err)
	}
	if out, err := run("", nil); err == nil || !strings.Contains(out, "-Upgrade") {
		t.Fatalf("install over an install: %v %s", err, out)
	}

	out, err = run("", nil, "-Upgrade", "--skip-backup")
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	got := ran()
	if len(got) != 1 {
		t.Fatalf("upgrade ran %q", got)
	}
	exeArg, args, _ := strings.Cut(got[0], "|")
	fields := strings.Fields(args)
	sep := string(filepath.Separator)
	if !strings.HasSuffix(exeArg, sep+"release"+sep+"bin"+sep+"openneko.exe") || len(fields) != 6 || fields[0] != "upgrade" || fields[1] != "--from" ||
		!strings.HasSuffix(fields[2], sep+"release") || fields[3] != "--exe" || fields[4] != exe || fields[5] != "--skip-backup" {
		t.Fatalf("the new executable ran as %q", got)
	}

	if err := os.RemoveAll(prefix); err != nil {
		t.Fatal(err)
	}
	if out, err := run("", []string{"FAKE_OPENNEKO_BAD=1"}); err == nil || !strings.Contains(out, "lacks its bundled skills or packs") {
		t.Fatalf("a failed release-check: %v %s", err, out)
	}
}
