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

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/installation"
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
		{name: "share/openneko/packs/", kind: tar.TypeDir},
		{name: "share/openneko/packs/slack/pack.yaml", body: "id: slack"},
		{name: "share/openneko/skills/builtin/graphjin-config/SKILL.md", body: "---\nname: graphjin-config\n---\n"},
		{name: "share/openneko/skills/sandbox/xlsx/SKILL.md", body: "---\nname: xlsx\n---\n"},
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
	if b, err := os.ReadFile(filepath.Join(dst, "share", "openneko", "packs", "slack", "pack.yaml")); err != nil || string(b) != "id: slack" {
		t.Fatalf("pack file: %q %v", b, err)
	}
}

func TestExtractNativeArchiveRefusesUnsafeEntries(t *testing.T) {
	cases := map[string]tarEntry{
		"parent":           {name: "../evil", body: "x"},
		"absolute":         {name: "/etc/evil", body: "x"},
		"inner dots":       {name: "share/openneko/web/../../../evil", body: "x"},
		"other path":       {name: "etc/openneko.conf", body: "x"},
		"other share link": {name: "share/openneko/later/link", kind: tar.TypeSymlink},
		"bin folder":       {name: "bin/other", body: "x"},
		"symlink":          {name: "share/openneko/web/link", kind: tar.TypeSymlink},
		"hard link":        {name: "share/openneko/web/link", kind: tar.TypeLink},
		"twice":            {name: "bin/openneko", body: "again"},
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

func TestNativeUpgradeReplacesTheReleasePacks(t *testing.T) {
	installed := t.TempDir()
	exe := filepath.Join(installed, "bin", "openneko")
	from := t.TempDir()
	if err := os.MkdirAll(filepath.Join(from, "share", "openneko", "packs", "slack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "share", "openneko", "packs", "slack", "pack.yaml"), []byte("metadata:\n  id: slack\n  name: Slack\n  version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(installed, "share", "openneko", "packs", "gone")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceReleasePacks(from, exe); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(installed, "share", "openneko", "packs", "slack", "pack.yaml")); err != nil || !strings.Contains(string(b), "version: 2") {
		t.Fatalf("pack file: %q %v", b, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the old pack stayed: %v", err)
	}
}

func TestNativeUpgradeRefusesAReleaseWithNoPacks(t *testing.T) {
	from := t.TempDir()
	web := filepath.Join(from, "share", "openneko", "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(t.TempDir(), "ran")
	if err := os.MkdirAll(filepath.Join(from, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "bin", "openneko"), []byte("#!/bin/sh\ntouch "+ran+"\necho 9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installedWeb := t.TempDir()
	upgrade := func() error {
		cmd := &cobra.Command{}
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		return runNativeUpgrade(context.Background(), cmd, installation.Settings{WebDir: installedWeb}, from, "", true)
	}
	for name, setup := range map[string]func(){
		"no packs folder": func() {},
		"empty packs":     func() { _ = os.MkdirAll(filepath.Join(from, "share", "openneko", "packs", "slack"), 0o755) },
		"empty pack.yaml": func() { writePackYAML(t, from, "") },
		"not YAML":        func() { writePackYAML(t, from, "metadata: [name: x\n") },
		"no name":         func() { writePackYAML(t, from, "metadata:\n  id: slack\n  version: 1\n") },
		"blank name":      func() { writePackYAML(t, from, "metadata:\n  name: \"  \"\n") },
	} {
		setup()
		err := upgrade()
		if err == nil || !strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), "packs") {
			t.Fatalf("%s: err = %v", name, err)
		}
		if _, err := os.Stat(ran); !os.IsNotExist(err) {
			t.Fatalf("%s: the upgrade ran the new executable before the packs check", name)
		}
	}
	if err := replaceReleasePacks(from, filepath.Join(t.TempDir(), "bin", "openneko")); err == nil {
		t.Fatal("replaceReleasePacks took a release with no packs")
	}
}

func TestExtractNativeArchiveRefusesAnArchiveWithNoPacks(t *testing.T) {
	var noPacks []tarEntry
	for _, e := range goodNativeEntries() {
		if !strings.HasPrefix(e.name, "share/openneko/packs/") {
			noPacks = append(noPacks, e)
		}
	}
	for name, entries := range map[string][]tarEntry{
		"no packs":     noPacks,
		"packs folder": append(append([]tarEntry{}, noPacks...), tarEntry{name: "share/openneko/packs/", kind: tar.TypeDir}),
		"no pack.yaml": append(append([]tarEntry{}, noPacks...), tarEntry{name: "share/openneko/packs/slack/README.md", body: "x"}),
		"too deep":     append(append([]tarEntry{}, noPacks...), tarEntry{name: "share/openneko/packs/a/b/pack.yaml", body: "x"}),
	} {
		if _, err := extractEntries(t, entries); err == nil || !strings.Contains(err.Error(), "no built-in pack") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func writePackYAML(t *testing.T, release, body string) {
	t.Helper()
	dir := filepath.Join(release, "share", "openneko", "packs", "slack")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheRepositoryPacksPassTheReleaseCheck(t *testing.T) {
	if err := checkReleasePacks(filepath.Join("..", "..", "..", "..", "packs")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractNativeArchiveTakesALaterShareFolder(t *testing.T) {
	entries := append(goodNativeEntries(),
		tarEntry{name: "share/openneko/later/", kind: tar.TypeDir},
		tarEntry{name: "share/openneko/later/a.txt", body: "x"},
		tarEntry{name: "share/openneko/packsx/a", body: "x"})
	dst, err := extractEntries(t, entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"share/openneko/later/a.txt", "share/openneko/packsx/a"} {
		fi, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil || fi.Mode().Perm() != 0o644 {
			t.Fatalf("%s: %v %v", rel, fi, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "share", "openneko", "skills", "sandbox", "xlsx", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractNativeArchiveRefusesAnArchiveWithNoSkills(t *testing.T) {
	without := func(prefix string) []tarEntry {
		var out []tarEntry
		for _, e := range goodNativeEntries() {
			if !strings.HasPrefix(e.name, prefix) {
				out = append(out, e)
			}
		}
		return out
	}
	for name, entries := range map[string][]tarEntry{
		"no skills":  without("share/openneko/skills/"),
		"no builtin": without("share/openneko/skills/builtin/"),
		"no sandbox": without("share/openneko/skills/sandbox/"),
	} {
		if _, err := extractEntries(t, entries); err == nil || !strings.Contains(err.Error(), "no bundled skills") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func writeReleaseSkills(t *testing.T, release string, sandbox ...string) {
	t.Helper()
	skills := filepath.Join(release, "share", "openneko", "skills")
	write := func(dir string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+filepath.Base(dir)+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(skills, "builtin", "graphjin-config"))
	for _, name := range sandbox {
		write(filepath.Join(skills, "sandbox", name))
	}
}

var releaseSandboxSkills = []string{"docx", "pdf", "pptx", "skill-creator", "xlsx"}

func TestNativeUpgradeRefusesAReleaseWithNoSkills(t *testing.T) {
	from := t.TempDir()
	web := filepath.Join(from, "share", "openneko", "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePackYAML(t, from, "metadata:\n  name: Slack\n")
	ran := filepath.Join(t.TempDir(), "ran")
	if err := os.MkdirAll(filepath.Join(from, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "bin", "openneko"), []byte("#!/bin/sh\ntouch "+ran+"\necho 9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	upgrade := func() error {
		cmd := &cobra.Command{}
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		return runNativeUpgrade(context.Background(), cmd, installation.Settings{WebDir: t.TempDir()}, from, "", true)
	}
	for name, setup := range map[string]func(){
		"no skills folder": func() {},
		"no xlsx":          func() { writeReleaseSkills(t, from, "docx", "pdf", "pptx", "skill-creator") },
	} {
		setup()
		err := upgrade()
		if err == nil || !strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), "skills") {
			t.Fatalf("%s: err = %v", name, err)
		}
		if _, err := os.Stat(ran); !os.IsNotExist(err) {
			t.Fatalf("%s: the upgrade ran the new executable before the skills check", name)
		}
	}
	writeReleaseSkills(t, from, releaseSandboxSkills...)
	if err := checkReleaseSkills(filepath.Join(from, "share", "openneko", "skills")); err != nil {
		t.Fatal(err)
	}
}

func TestNativeUpgradeReplacesTheReleaseSkills(t *testing.T) {
	installed := t.TempDir()
	exe := filepath.Join(installed, "bin", "openneko")
	from := t.TempDir()
	if err := replaceReleaseSkills(from, exe); err == nil {
		t.Fatal("replaceReleaseSkills took a release with no skills")
	}
	writeReleaseSkills(t, from, releaseSandboxSkills...)
	old := filepath.Join(installed, "share", "openneko", "skills", "sandbox", "gone")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceReleaseSkills(from, exe); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(installed, "share", "openneko", "skills", "sandbox", "xlsx", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the old skill stayed: %v", err)
	}
}

func TestNativeUpgradeReplacesTheExeNamedByTheInstallScript(t *testing.T) {
	home := t.TempDir()
	for k, v := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state"} {
		t.Setenv(k, filepath.Join(home, v))
	}
	prefix := filepath.Join(home, "opt")
	exe := filepath.Join(prefix, "bin", "openneko")
	installedWeb := filepath.Join(prefix, "share", "openneko", "web")
	for _, f := range []string{exe, filepath.Join(installedWeb, "index.html")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	settings := installation.Settings{Version: installation.CurrentVersion, Mode: "prod", WebPort: 3999, WebBindAddress: "127.0.0.1", Runtime: installation.RuntimeNative, WebDir: installedWeb}
	if err := installation.Save(config.Dir(""), settings); err != nil {
		t.Fatal(err)
	}
	from := t.TempDir()
	newWeb := filepath.Join(from, "share", "openneko", "web")
	if err := os.MkdirAll(newWeb, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newWeb, "index.html"), []byte("<html>new</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(from, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "bin", "openneko"), []byte("#!/bin/sh\necho 9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePackYAML(t, from, "metadata:\n  name: Slack\n")
	writeReleaseSkills(t, from, releaseSandboxSkills...)
	later := filepath.Join(from, "share", "openneko", "later")
	if err := os.MkdirAll(later, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(later, "a.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(prefix, "share", "openneko", "later", "gone.txt")
	if err := os.MkdirAll(filepath.Dir(gone), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gone, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(self)
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := newUpgradeCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := run("--from", from, "--skip-backup", "--exe", filepath.Join(prefix, "bin")); err == nil || !strings.Contains(err.Error(), "--exe") {
		t.Fatalf("a folder as --exe: err = %v", err)
	}
	if out, err := run("--from", from, "--skip-backup", "--exe", exe); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "9.9.9") {
		t.Fatalf("--exe was not replaced: %q", b)
	}
	for _, f := range []string{"web/index.html", "packs/slack/pack.yaml", "skills/sandbox/xlsx/SKILL.md", "later/a.txt"} {
		if _, err := os.Stat(filepath.Join(prefix, "share", "openneko", filepath.FromSlash(f))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("the old file of the later folder stayed: %v", err)
	}
	if fi, err := os.Stat(exe); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("the replaced executable: %v %v", fi, err)
	}
	if after, err := os.Stat(self); err != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("the upgrade changed the running executable: %v", err)
	}
}
