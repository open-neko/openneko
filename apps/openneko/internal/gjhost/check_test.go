package gjhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkCandidate(t *testing.T, h http.Handler, method, body string) (int, checkResult, string) {
	t.Helper()
	req := httptest.NewRequest(method, "/v1/graphjin/config-check", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var res checkResult
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, res, rec.Body.String()
}

func writeCandidate(t *testing.T, configFile, id, content string) {
	t.Helper()
	dir := filepath.Join(filepath.Dir(configFile), CheckDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(configFile)), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func shippedSeed(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "db", "graphjin", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReplacer(
		"REPLACE_WITH_BASE64_32_BYTE_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"REPLACE_WITH_PER_ORG_SECRET_B64", "c2VjcmV0LXNlY3JldC1zZWNyZXQtc2VjcmV0LXNlY3JldA==",
	).Replace(string(raw))
}

func TestCheckHandlerLoadsTheCandidateWithThePinnedLoader(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "agentic.yml")
	h := CheckHandler(configFile)
	seed := shippedSeed(t, "customer.sources.example.yml")
	withSources := func(sources string) string {
		return strings.Replace(seed, "\nsources: []\n", "\nsources:\n"+sources, 1)
	}
	good := "00000000-0000-4000-8000-000000000001"
	writeCandidate(t, configFile, good, withSources("  - name: shop\n    kind: database\n    type: mysql\n    password: hunter2-secret\n    read_only: true\n"))
	code, res, _ := checkCandidate(t, h, http.MethodPost, `{"candidate":"`+good+`"}`)
	if code != http.StatusOK || !res.Valid || len(res.Errors) != 0 {
		t.Fatalf("good candidate: %d %+v", code, res)
	}

	bad := "00000000-0000-4000-8000-000000000002"
	writeCandidate(t, configFile, bad, withSources("  - name: shop\n    kind: database\n    password: hunter2-secret\n    access:\n      read: everyone\n"))
	code, res, body := checkCandidate(t, h, http.MethodPost, `{"candidate":"`+bad+`"}`)
	if code != http.StatusOK || res.Valid || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], `sources["shop"].access.read`) {
		t.Fatalf("bad candidate: %d %+v", code, res)
	}
	if strings.Contains(body, "hunter2") {
		t.Fatalf("the reply holds config content: %s", body)
	}
}

func TestCheckHandlerTakesACandidateIDOnly(t *testing.T) {
	root := t.TempDir()
	configFile := filepath.Join(root, "agentic.yml")
	h := CheckHandler(configFile)
	if err := os.WriteFile(filepath.Join(root, "outside.yml"), []byte("sources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"candidate":"../agentic"}`,
		`{"candidate":"/etc/passwd"}`,
		`{"candidate":"sources: []"}`,
		`{"file":"agentic.yml"}`,
		`not json`,
	} {
		if code, _, _ := checkCandidate(t, h, http.MethodPost, body); code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", body, code)
		}
	}
	if code, _, _ := checkCandidate(t, h, http.MethodGet, `{"candidate":"00000000-0000-4000-8000-000000000001"}`); code != http.StatusNotFound {
		t.Fatalf("GET: status %d", code)
	}
	if code, _, _ := checkCandidate(t, h, http.MethodPost, `{"candidate":"00000000-0000-4000-8000-000000000009"}`); code != http.StatusNotFound {
		t.Fatalf("missing candidate: status %d", code)
	}

	linked := "00000000-0000-4000-8000-000000000003"
	if err := os.MkdirAll(filepath.Join(root, CheckDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, CheckDir, linked)); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := checkCandidate(t, h, http.MethodPost, `{"candidate":"`+linked+`"}`); code != http.StatusNotFound {
		t.Fatalf("linked folder: status %d", code)
	}
	linkedFile := "00000000-0000-4000-8000-000000000004"
	if err := os.MkdirAll(filepath.Join(root, CheckDir, linkedFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.yml"), filepath.Join(root, CheckDir, linkedFile, "agentic.yml")); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := checkCandidate(t, h, http.MethodPost, `{"candidate":"`+linkedFile+`"}`); code != http.StatusNotFound {
		t.Fatalf("linked file: status %d", code)
	}
}

func TestCheckConfigAcceptsTheShippedSeeds(t *testing.T) {
	for _, seed := range []string{"customer.sources.example.yml", "dev.example.yml"} {
		conf := shippedSeed(t, seed)
		file := filepath.Join(t.TempDir(), "agentic.yml")
		if err := os.WriteFile(file, []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CheckConfig(file); err != nil {
			t.Fatalf("%s: %v", seed, err)
		}
	}
}
