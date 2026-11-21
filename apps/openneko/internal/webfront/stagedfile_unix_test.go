//go:build !windows

package webfront

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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
		if data, err := readStagedFile(root, name, seen); err == nil {
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
