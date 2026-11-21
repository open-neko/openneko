//go:build !windows

package webfront

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func TestCopyBackReadsNoFileOutsideTheRun(t *testing.T) {
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENNEKO_SANDBOX_URL", "")
	host, err := turnWorkspace("org", "thread-1", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	st, err := stageTurn(host, stageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(st.Dir)
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret.key")
	writeTestFile(t, secret, "the secret key")
	art := st.Workspace.ArtifactRoot

	if err := os.Link(secret, filepath.Join(art, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(art, "chart.csv"), "a,b")
	if err := st.copyBack(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(host.ArtifactRoot, "linked.txt")); err == nil {
		t.Error("a second name of a file outside the run came back")
	}
	if b, _ := os.ReadFile(filepath.Join(host.ArtifactRoot, "chart.csv")); string(b) != "a,b" {
		t.Error("the artifact did not come back")
	}

	root, err := os.OpenRoot(art)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	swap := func(t *testing.T, name string, replace func(p string)) {
		t.Helper()
		p := filepath.Join(art, filepath.FromSlash(name))
		writeTestFile(t, p, "artifact")
		seen, err := root.Lstat(filepath.FromSlash(name))
		if err != nil {
			t.Fatal(err)
		}
		replace(p)
		if data, err := readStagedFile(root, name, seen, 1<<20); err == nil {
			t.Errorf("read %q after the swap: %q", name, data)
		}
	}
	t.Run("file swapped for a link", func(t *testing.T) {
		swap(t, "a.txt", func(p string) {
			os.Remove(p)
			if err := os.Symlink(secret, p); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("file swapped for another file", func(t *testing.T) {
		swap(t, "b.txt", func(p string) {
			writeTestFile(t, p+".new", "other")
			if err := os.Rename(p+".new", p); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("folder swapped for a link", func(t *testing.T) {
		writeTestFile(t, filepath.Join(secretDir, "c.txt"), "the secret key")
		swap(t, "deep/c.txt", func(p string) {
			os.RemoveAll(filepath.Dir(p))
			if err := os.Symlink(secretDir, filepath.Dir(p)); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("file swapped for a pipe", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			swap(t, "d.txt", func(p string) {
				os.Remove(p)
				if err := syscall.Mkfifo(p, 0o600); err != nil {
					t.Error(err)
				}
			})
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the read blocked on a pipe")
		}
	})
}

// TestRunnerFilesAreReadWithALimit plants a sparse file far over the limits
// and one just over them: web refuses each without reading it.
func TestRunnerFilesAreReadWithALimit(t *testing.T) {
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENNEKO_SANDBOX_URL", "")
	host, err := turnWorkspace("org", "thread-1", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	st, err := stageTurn(host, stageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(st.Dir)
	art := st.Workspace.ArtifactRoot
	sized := func(p string, size int64, sparse bool) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if sparse {
			if err := f.Truncate(size); err != nil {
				t.Fatal(err)
			}
			return
		}
		if _, err := io.CopyN(f, zeroReader{}, size); err != nil {
			t.Fatal(err)
		}
	}
	sized(filepath.Join(art, "huge.bin"), 100<<30, true)
	sized(filepath.Join(art, "hole.bin"), 1<<20, true)
	sized(filepath.Join(art, "over.bin"), sandbox.MaxTransfer+1, false)
	sized(filepath.Join(art, "at.bin"), 1<<20, false)
	if err := st.copyBack(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"huge.bin": false, "hole.bin": false, "over.bin": false, "at.bin": true} {
		_, err := os.Stat(filepath.Join(host.ArtifactRoot, name))
		if (err == nil) != want {
			t.Errorf("artifact %s came back: %v, want %v", name, err == nil, want)
		}
	}

	skills := t.TempDir()
	sized(filepath.Join(skills, "big", "SKILL.md"), 100<<30, true)
	sized(filepath.Join(skills, "over", "SKILL.md"), skillArchiveMax+1, false)
	writeTestFile(t, filepath.Join(skills, "small", "SKILL.md"), "ok")
	hashes := hashSkillTree(skills)
	for _, rel := range []string{"big/SKILL.md", "over/SKILL.md"} {
		if !strings.HasPrefix(hashes[rel], "refused: ") {
			t.Errorf("skill file %s hashed as %q, want a refusal", rel, hashes[rel])
		}
	}
	if len(hashes["small/SKILL.md"]) != 64 {
		t.Errorf("small skill file hashed as %q", hashes["small/SKILL.md"])
	}
	root, err := os.OpenRoot(skills)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	info, _ := root.Lstat("over/SKILL.md")
	if _, err := readStagedFile(root, "over/SKILL.md", info, skillArchiveMax); err == nil || !strings.Contains(err.Error(), "over the limit") {
		t.Errorf("read an oversized skill file: %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
