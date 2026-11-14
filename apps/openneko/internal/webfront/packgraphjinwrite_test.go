package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
)

// fakeSupervisor answers the ping and restart files as gjhost's supervisor does.
func fakeSupervisor(t *testing.T, dir string) *[]string {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var seen []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		acked := map[string]string{}
		for ctx.Err() == nil {
			for request, ack := range map[string]string{gjhost.PingFile: gjhost.PingAckFile, gjhost.RestartFile: gjhost.RestartAckFile} {
				raw, _ := os.ReadFile(filepath.Join(dir, request))
				if token := strings.TrimSpace(string(raw)); token != "" && acked[request] != token {
					acked[request] = token
					mu.Lock()
					seen = append(seen, request)
					mu.Unlock()
					_ = os.WriteFile(filepath.Join(dir, ack), []byte(token), 0o644)
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return &seen
}

func TestApplyPackGraphjinConfigFiles(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "prod.yml")
	original := "sources:\n  - name: slack\n    kind: api\n    read_only: false\ntables:\n  - name: kept\n    source: slack\n"
	if err := os.WriteFile(configFile, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	secretsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(secretsDir, "secret-key"), []byte("files-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var updates []string
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct{ Query string }
		_ = json.Unmarshal(raw, &body)
		reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"gj_config": v}}) }
		switch {
		case strings.Contains(body.Query, "PackConfigRevision_"):
			reply(map[string]any{"catalog_revision": strings.Repeat("a", 64), "sources": `[{"name":"slack"}]`, "tables": []any{}, "relationships": []any{}})
		case strings.Contains(body.Query, `mode: "preview"`):
			mu.Lock()
			updates = append(updates, body.Query)
			stale := len(updates) == 1
			mu.Unlock()
			if stale {
				_, _ = io.WriteString(w, `{"errors":[{"message":"expected_catalog_revision does not match; current catalog_revision `+strings.Repeat("b", 64)+`"}]}`)
				return
			}
			reply(map[string]any{"valid": true, "preview_id": "p9"})
		case strings.Contains(body.Query, `mode: "apply"`):
			mu.Lock()
			updates = append(updates, body.Query)
			mu.Unlock()
			reply(map[string]any{"applied": true, "catalog_revision": "c2"})
		default:
			_, _ = io.WriteString(w, `{"data":{"__typename":"Query"}}`)
		}
	}))
	t.Cleanup(gj.Close)
	seen := fakeSupervisor(t, dir)
	a := &API{SecretsDir: secretsDir}
	ctx := context.Background()
	update := jsObjOf(
		"source_patches", []any{jsObjOf("name", "slack", "read_only", true, "access", jsObjOf("read", "blocked", "write", "blocked", "delete", "blocked"))},
		"tables", []any{jsObjOf("name", "added", "source", "slack", "database", "slack")})
	applied, err := a.applyPackGraphjinConfig(ctx, gj.URL, "org", configFile, packGraphjinUpdate{update: update, ownedSources: map[string]bool{"slack": true}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if applied.catalogRevision != "c2" {
		t.Errorf("revision %v", applied.catalogRevision)
	}
	if len(updates) != 3 || !strings.Contains(updates[2], strings.Repeat("b", 64)) || !strings.Contains(updates[2], `preview_id: "p9"`) {
		t.Errorf("the retry did not carry the revision from the conflict: %v", updates)
	}
	raw, _ := os.ReadFile(configFile)
	var written struct {
		Sources []map[string]any
		Tables  []map[string]any
	}
	if err := yaml.Unmarshal(raw, &written); err != nil {
		t.Fatal(err)
	}
	access, _ := written.Sources[0]["access"].(map[string]any)
	if written.Sources[0]["read_only"] != true || access["read"] != "blocked" || access["delete"] != "blocked" {
		t.Errorf("source %v", written.Sources)
	}
	if len(written.Tables) != 2 || written.Tables[1]["name"] != "added" || written.Tables[1]["database"] != nil {
		t.Errorf("tables %v", written.Tables)
	}
	if info, _ := os.Stat(configFile); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", info.Mode())
	}
	if strings.Join(*seen, ",") != gjhost.PingFile+","+gjhost.RestartFile {
		t.Errorf("supervisor saw %v", *seen)
	}
	if _, err := os.Stat(filepath.Join(dir, ".prod.yml.openneko.lock")); !os.IsNotExist(err) {
		t.Errorf("the lock is still held: %v", err)
	}

	release, err := acquireGraphjinConfigLock(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.applyPackGraphjinConfig(ctx, gj.URL, "org", configFile, packGraphjinUpdate{update: update}, false); err == nil || err.Error() != "GraphJin config has another approved change in progress" {
		t.Errorf("a held lock: %v", err)
	}
	release()

	if err := applied.restore(ctx); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(configFile); string(raw) != original {
		t.Errorf("restore:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.enc.yml")); !os.IsNotExist(err) {
		t.Errorf("restore made a keystore: %v", err)
	}
	if err := os.WriteFile(configFile, []byte(original+"# edited\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := applied.restore(ctx); err == nil || !strings.Contains(err.Error(), "rollback preserved the newer state") {
		t.Errorf("restore over a newer change: %v", err)
	}
}

func TestApplyPackGraphjinConfigOwnership(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "prod.yml")
	if err := os.WriteFile(configFile, []byte("tables:\n  - name: orders\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secretsDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(secretsDir, "secret-key"), []byte("k\n"), 0o600)
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"gj_config":{"catalog_revision":"r","sources":[{"name":"shop"}],"tables":[],"relationships":[]}}}`)
	}))
	t.Cleanup(gj.Close)
	a := &API{SecretsDir: secretsDir}
	for _, c := range []struct {
		in   packGraphjinUpdate
		want string
	}{
		{packGraphjinUpdate{update: jsObjOf("update_sources", []any{jsObjOf("name", "shop")})}, "GraphJin source shop already exists and is not owned by this pack"},
		{packGraphjinUpdate{update: jsObjOf("tables", []any{jsObjOf("name", "orders")}), ownedTables: map[string]bool{}}, "GraphJin table orders already exists and is not owned by this pack"},
	} {
		if _, err := a.applyPackGraphjinConfig(context.Background(), gj.URL, "org", configFile, c.in, false); err == nil || err.Error() != c.want {
			t.Errorf("got %v, want %s", err, c.want)
		}
	}
	if err := unsupportedGraphjinConfigAPI([]string{`Cannot query field "preview_id" on type`}); err == nil || !strings.Contains(err.Error(), "needs GraphJin 3.18.35 or later") {
		t.Errorf("old GraphJin: %v", err)
	}
}
