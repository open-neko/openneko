//go:build linux || darwin

package webfront

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/files"
)

var listenLine = regexp.MustCompile(`openneko serve listening on (\S+)`)

// TestConfinedNetworkPartServesAWholeFile starts the release binary's
// serve-network under its confinement and reads a 64 KiB file through
// /v1/files/content. The part needs no database to answer that route.
func TestConfinedNetworkPartServesAWholeFile(t *testing.T) {
	if st := confine.Available(); !st.Confined {
		if os.Getenv("CI") != "" {
			t.Fatalf("this runner cannot prove the confinement: %s", st.Reason)
		}
		t.Skip(st.Reason)
	}
	bin := releaseBinary(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	filesDir := filepath.Join(base, "files")
	want := make([]byte, 64<<10)
	_, _ = rand.Read(want)
	stored := filepath.Join(filesDir, "orgs", "org-1", "users", "user-1", "library", "alternates.xlsx")
	if err := os.MkdirAll(filepath.Dir(stored), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stored, want, 0o600); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	start, _ := json.Marshal(map[string]any{"token_key": key, "database_url": "postgres://openneko@127.0.0.1:1/none?sslmode=disable",
		"addr": "127.0.0.1:0", "files_dir": filesDir, "index_dir": filepath.Join(base, "index"), "temp_dir": filepath.Join(base, "tmp")})

	cmd := exec.Command(bin, "serve-network")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(base, "home"), "OPENNEKO_ENV=production"}
	logs := &lockedBuffer{}
	cmd.Stderr = logs
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	if _, err := stdin.Write(append(start, '\n')); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 16)
	go func() {
		r := bufio.NewScanner(stdout)
		for r.Scan() {
			lines <- r.Text()
		}
		close(lines)
	}()
	var state confine.State
	var addr string
	deadline := time.After(30 * time.Second)
	for addr == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("serve-network ended: %s", logs.String())
			}
			var ready struct {
				Confinement *confine.State `json:"confinement"`
				Error       string         `json:"error"`
			}
			if json.Unmarshal([]byte(line), &ready) == nil && ready.Error != "" {
				t.Fatalf("serve-network: %s\n%s", ready.Error, logs.String())
			} else if ready.Confinement != nil {
				state = *ready.Confinement
			}
			if m := listenLine.FindStringSubmatch(line); m != nil {
				addr = m[1]
			}
		case <-deadline:
			t.Fatalf("serve-network did not listen: %s", logs.String())
		}
	}
	if !state.Confined {
		t.Fatalf("serve-network runs unconfined: %+v", state)
	}

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/files/content?path=library/alternates.xlsx", nil)
	req.Header.Set("Authorization", "Bearer "+files.MintToken(key, "org-1", "user-1", time.Now().Add(time.Minute)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	got, err := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || err != nil || !bytes.Equal(got, want) {
		t.Fatalf("confined (%s): status %d, %d of %d bytes, %v\n%s", state.Mechanism, res.StatusCode, len(got), len(want), err, logs.String())
	}
	if strings.Contains(logs.String(), "[files]") {
		t.Fatalf("serve-network logged a copy failure: %s", logs.String())
	}
}
