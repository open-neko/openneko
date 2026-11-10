package files

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dosco/graphjin/core/v3/fstable"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/filecontent"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func mint(key []byte, org, user string, exp time.Time) string {
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, _ := json.Marshal(claims{Org: org, Sub: user, Exp: exp.Unix()})
	msg := head + "." + enc.EncodeToString(body)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return msg + "." + enc.EncodeToString(mac.Sum(nil))
}

func newServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	store, err := fstable.NewLocal(fstable.LocalConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	ix, err := filecontent.Open(testpg.Pool(t, "0121_file_content.sql"), t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store, ix, testKey, Options{DuckDB: os.Getenv("OPENNEKO_TEST_DUCKDB"), Refresh: time.Millisecond})
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return srv, root
}

func call(t *testing.T, srv *httptest.Server, token, method, path string, body []byte) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out) //nolint:errcheck
	return res.StatusCode, out
}

func put(t *testing.T, srv *httptest.Server, token, p string, data []byte) {
	t.Helper()
	if code, out := call(t, srv, token, http.MethodPut, "/v1/files/content?path="+url.QueryEscape(p), data); code != 200 {
		t.Fatalf("put %s = %d %v", p, code, out)
	}
}

func TestFilesArePrivateToTheirUser(t *testing.T) {
	srv, root := newServer(t)
	alice := mint(testKey, "org1", "alice", time.Now().Add(time.Minute))
	bob := mint(testKey, "org1", "bob", time.Now().Add(time.Minute))

	put(t, srv, alice, "policies/travel.md", []byte("# Per diem\nTier 1 cities pay 70 per day.\n"))
	put(t, srv, alice, "pricing/q4 prices.csv", []byte("SKU,Price\nHI-1,12.5\nHI-2,7\n"))
	put(t, srv, bob, "notes.md", []byte("# Per diem\nBob's per diem notes.\n"))
	if _, err := os.Stat(filepath.Join(root, "orgs", "org1", "users", "alice", "policies", "travel.md")); err != nil {
		t.Fatalf("file not in alice's folder: %v", err)
	}

	code, out := call(t, srv, alice, http.MethodPost, "/v1/files/search", []byte(`{"query":"per diem tier 1"}`))
	matches, _ := out["matches"].([]any)
	if code != 200 || len(matches) == 0 {
		t.Fatalf("search = %d %v", code, out)
	}
	for _, m := range matches {
		if p := m.(map[string]any)["path"]; p == "notes.md" {
			t.Fatalf("alice found bob's file: %v", out)
		}
	}
	if first := matches[0].(map[string]any)["path"]; first != "policies/travel.md" {
		t.Fatalf("first match = %v", first)
	}

	code, out = call(t, srv, alice, http.MethodPost, "/v1/files/read", []byte(`{"path":"policies/travel.md","max_chars":10}`))
	if code != 200 || out["text"] != "# Per diem" || out["truncated"] != true {
		t.Fatalf("read = %d %v", code, out)
	}
	for _, p := range []string{"notes.md", "../bob/notes.md", "/../../bob/notes.md"} {
		body, _ := json.Marshal(map[string]string{"path": p})
		if code, out := call(t, srv, alice, http.MethodPost, "/v1/files/read", body); code == 200 {
			t.Fatalf("alice read %s: %v", p, out)
		}
	}

	code, out = call(t, srv, bob, http.MethodGet, "/v1/files", nil)
	if files, _ := out["files"].([]any); code != 200 || len(files) != 1 {
		t.Fatalf("bob list = %d %v", code, out)
	}

	code, _ = call(t, srv, "", http.MethodGet, "/v1/files", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", code)
	}
	for _, tok := range []string{
		mint(testKey, "org1", "alice", time.Now().Add(-time.Second)),
		mint([]byte("another key of thirty-two bytes!"), "org1", "alice", time.Now().Add(time.Minute)),
		mint(testKey, "org1", "../bob", time.Now().Add(time.Minute)),
	} {
		if code, _ := call(t, srv, tok, http.MethodGet, "/v1/files", nil); code != http.StatusUnauthorized {
			t.Fatalf("bad token accepted: %d", code)
		}
	}

	if os.Getenv("OPENNEKO_TEST_DUCKDB") != "" {
		code, out = call(t, srv, alice, http.MethodPost, "/v1/files/sql", []byte(`{"sql":"select sum(price) as total from q4_prices"}`))
		if code != 200 || !strings.Contains(toJSON(out["rows"]), "19.5") {
			t.Fatalf("sql = %d %v", code, out)
		}
		code, out = call(t, srv, bob, http.MethodPost, "/v1/files/sql", []byte(`{"sql":"select * from q4_prices"}`))
		if code != http.StatusBadRequest {
			t.Fatalf("bob queried alice's table: %d %v", code, out)
		}
	}

	if code, _ := call(t, srv, alice, http.MethodDelete, "/v1/files/content?path=policies/travel.md", nil); code != 200 {
		t.Fatalf("delete = %d", code)
	}
	_, out = call(t, srv, alice, http.MethodPost, "/v1/files/search", []byte(`{"query":"tier cities"}`))
	if matches, _ := out["matches"].([]any); len(matches) != 0 {
		t.Fatalf("deleted file still found: %v", out)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestSigningSecretMatchesTheNodeDerivation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret-key"), []byte("fixture-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := config.SigningSecret(dir, TokenLabel)
	if err != nil {
		t.Fatal(err)
	}
	const want = "4fc869c2fcfaf11034f04f0ea823c141e82f489797c6da24a1571bc1fb052113"
	if got := hex.EncodeToString(key); got != want {
		t.Fatalf("SigningSecret = %s", got)
	}
}
