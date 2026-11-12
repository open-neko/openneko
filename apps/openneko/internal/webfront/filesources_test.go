package webfront

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type upPart struct{ field, filename, body string }

func uploadBody(t *testing.T, parts ...upPart) (string, map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		if p.filename == "" {
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q`, p.field))
		} else {
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.field, p.filename))
			h.Set("Content-Type", "text/csv")
		}
		w, _ := mw.CreatePart(h)
		w.Write([]byte(p.body))
	}
	mw.Close()
	return buf.String(), map[string]string{"Content-Type": mw.FormDataContentType()}
}

func withCookie(h map[string]string, cookie map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		out[k] = v
	}
	for k, v := range cookie {
		out[k] = v
	}
	return out
}

func TestFileSourceUpload(t *testing.T) {
	f, _ := settingsFront(t)
	admin := sessionCookie("admin")
	const path = "/api/v1/settings/file-sources"
	root := t.TempDir()
	configFile := filepath.Join(root, "agentic.yml")
	os.WriteFile(configFile, []byte("sources:\n  - name: live\n    type: postgres\n"), 0o644)
	sum := sha256.Sum256([]byte("org"))
	dirName := "org_" + hex.EncodeToString(sum[:])[:12] + "_documents"
	dir := filepath.Join(root, "files", dirName)

	post := func(cookie map[string]string, parts ...upPart) (int, string) {
		body, h := uploadBody(t, parts...)
		w := call(f, "POST", path, body, withCookie(h, cookie))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	doc := upPart{"sourceName", "", " documents "}
	csv := func(name, body string) upPart { return upPart{"files", name, body} }
	expect := func(name string, code int, body string, want int, wantBody string) {
		t.Helper()
		if code != want || body != wantBody {
			t.Fatalf("%s: %d %s", name, code, body)
		}
	}

	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", "")
	code, body := post(admin, doc, csv("a.csv", "x"))
	expect("no config", code, body, 503, `{"error":"managed GraphJin file storage is unavailable"}`)
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", configFile)
	t.Setenv("OPENNEKO_GRAPHJIN_FILES_LOCAL_DIR", "")

	code, body = post(sessionCookie("member"), doc, csv("a.csv", "x"))
	expect("member", code, body, 403, `{"error":"admin only"}`)
	call(f, "PATCH", "/api/v1/settings/graphjin-config", `{"sourceConfigEnabled":false}`, admin)
	code, body = post(admin, doc, csv("a.csv", "x"))
	expect("disabled", code, body, 404, `{"error":"not found"}`)
	call(f, "PATCH", "/api/v1/settings/graphjin-config", `{"sourceConfigEnabled":true}`, admin)

	big := withCookie(map[string]string{"Content-Type": "multipart/form-data; boundary=x", "Content-Length": strconv.Itoa(26<<20 + 1)}, admin)
	if w := call(f, "POST", path, "", big); w.Code != 413 || strings.TrimSpace(w.Body.String()) != `{"error":"file upload is too large"}` {
		t.Fatalf("too large: %d %s", w.Code, w.Body)
	}
	hugeBody, hugeHeader := uploadBody(t, doc, csv("huge.csv", strings.Repeat("x", 27<<20)))
	if w := call(f, "POST", path, hugeBody, withCookie(hugeHeader, admin)); w.Code != 413 {
		t.Fatalf("no length: %d %s", w.Code, w.Body)
	}
	if w := call(f, "POST", path, `{}`, withCookie(map[string]string{"Content-Type": "application/json"}, admin)); w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"error":"Content-Type was not one of \"multipart/form-data\" or \"application/x-www-form-urlencoded\"."}` {
		t.Fatalf("json: %d %s", w.Code, w.Body)
	}
	if w := call(f, "POST", path, "--x\r\nbad", withCookie(map[string]string{"Content-Type": "multipart/form-data; boundary=x"}, admin)); w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"error":"Failed to parse body as FormData."}` {
		t.Fatalf("bad multipart: %d %s", w.Code, w.Body)
	}
	if w := call(f, "POST", path, "sourceName=documents", withCookie(map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, admin)); w.Code != 400 || !strings.Contains(w.Body.String(), "at least one file") {
		t.Fatalf("urlencoded: %d %s", w.Code, w.Body)
	}

	required := `{"error":"sourceName and at least one file are required"}`
	code, body = post(admin, csv("a.csv", "x"))
	expect("no name", code, body, 400, required)
	code, body = post(admin, doc, upPart{"files", "", "not a file"})
	expect("no file", code, body, 400, required)
	many := []upPart{doc}
	for i := range 11 {
		many = append(many, csv(fmt.Sprintf("f%d.csv", i), "x"))
	}
	code, body = post(admin, many...)
	expect("eleven", code, body, 400, `{"error":"upload no more than 10 files at a time"}`)
	code, body = post(admin, doc, csv("empty.csv", ""))
	expect("empty", code, body, 400, `{"error":"\"empty.csv\" must be between 1 byte and 10 MB"}`)
	code, body = post(admin, upPart{"sourceName", "", "1bad"}, csv("a.csv", "x"))
	expect("source name", code, body, 400, `{"error":"register_source file name must start with a letter and contain only letters, digits, and underscores (64 characters maximum)"}`)
	unsafe := `{"error":"managed file names must be 1-128 safe filename characters and may not contain a path"}`
	for _, name := range []string{"../a.csv", "sub/a.csv", ".hidden", "a.csv.", "a.csv "} {
		code, body = post(admin, doc, csv(name, "x"))
		expect(name, code, body, 400, unsafe)
	}
	code, body = post(admin, upPart{"sourceName", "", "live"}, csv("a.csv", "x"))
	expect("active", code, body, 400, `{"error":"managed file source is active; use a new source name for new content"}`)

	names := []string{"b.csv", "Z.csv", "a1.csv", "a(1).csv", "A.csv", "a.csv", "a-b.csv", "a_b.csv", "a b.csv"}
	first := []upPart{doc}
	for _, n := range names[:5] {
		first = append(first, csv(n, "row "+n))
	}
	code, body = post(admin, first...)
	if code != 201 || !strings.HasPrefix(body, `{"managedLocalFiles":{"sourceName":"documents","ready":true,"files":[{"name":"A.csv","size":9,"contentType":"application/octet-stream","checksumSha256":"`) || !strings.HasSuffix(body, `],"totalSize":`+strconv.Itoa(5*4+len(strings.Join(names[:5], "")))+`}}`) {
		t.Fatalf("first: %d %s", code, body)
	}
	second := []upPart{doc}
	for _, n := range names[5:] {
		second = append(second, csv(n, "row "+n))
	}
	second = append(second, csv("ｆｕｌｌ.csv", "wide"))
	code, body = post(admin, second...)
	var order []string
	for _, s := range strings.Split(body, `{"name":"`)[1:] {
		order = append(order, s[:strings.Index(s, `"`)])
	}
	if code != 201 || strings.Join(order, "|") != "a b.csv|a_b.csv|a-b.csv|a.csv|A.csv|a(1).csv|a1.csv|b.csv|full.csv|Z.csv" {
		t.Fatalf("second: %d %s", code, body)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.csv"))
	want := sha256.Sum256([]byte("row a.csv"))
	if string(got) != "row a.csv" || !strings.Contains(body, hex.EncodeToString(want[:])) {
		t.Fatalf("content: %q %s", got, body)
	}
	if info, _ := os.Stat(filepath.Join(dir, "a.csv")); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", info.Mode())
	}

	code, body = post(admin, doc, csv("new.csv", "x"), csv("b.csv", "again"))
	expect("exists", code, body, 400, `{"error":"managed file \"b.csv\" already exists"}`)
	if _, err := os.Stat(filepath.Join(dir, "new.csv")); !os.IsNotExist(err) {
		t.Fatal("new.csv stayed after the failed batch")
	}
	code, body = post(admin, doc, csv("c.csv", "x"), csv("c.csv", "y"))
	expect("duplicate", code, body, 400, `{"error":"the upload contains duplicate file names"}`)

	lock := filepath.Join(root, "files", "."+dirName+".lock")
	os.WriteFile(lock, []byte("other"), 0o600)
	code, body = post(admin, doc, csv("c.csv", "x"))
	expect("locked", code, body, 400, `{"error":"managed file source has another operation in progress"}`)
	old := time.Now().Add(-11 * time.Minute)
	os.Chtimes(lock, old, old)
	if code, body = post(admin, doc, csv("c.csv", "x")); code != 201 {
		t.Fatalf("stale lock: %d %s", code, body)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("lock stayed")
	}

	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	code, body = post(admin, doc, csv("d.csv", "x"))
	expect("subdir", code, body, 400, `{"error":"managed file source contains an unsupported entry \"sub\""}`)
	os.Remove(filepath.Join(dir, "sub"))

	os.WriteFile(filepath.Join(root, "files", "."+dirName+".approved"), []byte("2026"), 0o600)
	code, body = post(admin, doc, csv("d.csv", "x"))
	expect("approved", code, body, 400, `{"error":"managed file source is active; use a new source name for new content"}`)

	t.Setenv("OPENNEKO_GRAPHJIN_FILES_LOCAL_DIR", filepath.Join(root, "elsewhere"))
	if code, body = post(admin, doc, csv("d.csv", "x")); code != 201 {
		t.Fatalf("local dir: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(root, "elsewhere", dirName, "d.csv")); err != nil {
		t.Fatal(err)
	}

	os.WriteFile(configFile, []byte("sources: x\n"), 0o644)
	code, body = post(admin, upPart{"sourceName", "", "other"}, csv("a.csv", "x"))
	expect("sources not a list", code, body, 400, `{"error":"GraphJin config sources must be a YAML list"}`)
	os.WriteFile(configFile, []byte("a: [\n"), 0o644)
	code, body = post(admin, upPart{"sourceName", "", "other"}, csv("a.csv", "x"))
	expect("bad yaml", code, body, 400, `{"error":"GraphJin config is unavailable for source validation"}`)
	os.WriteFile(configFile, []byte("sources:\n  - name: 5\n  - name: other2\n"), 0o644)
	if code, body = post(admin, upPart{"sourceName", "", "other"}, csv("a.csv", "x")); code != 201 {
		t.Fatalf("other: %d %s", code, body)
	}
}
