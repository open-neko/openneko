package webfront

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const (
	nodePayload     = `{"b":1.5,"a":[1,"é",1e21,0.1],"10":true,"2":null,"！":1,"😀":2}`
	nodePayloadHash = "2f45035a07c9bfe3f5d5468cb6a18a4f4f9c1d4da01784236cb08caa84444f7c"
)

func filesFront(t *testing.T) (*Front, *pgxpool.Pool) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENNEKO_BUILTIN_SKILLS_ROOT", t.TempDir())
	t.Setenv("OPENNEKO_SANDBOX_SKILLS_ROOT", t.TempDir())
	t.Setenv("OPENNEKO_SERVE_URL", "")
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	seed(t, db, `insert into work_thread (id, org_id, created_by_user_id) values ($1, 'org', 'member'), ($2, 'org', 'admin')`, threadMine, threadOther)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values ($1, 'org', $2, 'test', 'member', 'member')`, runMine, threadMine)
	return f, db
}

func TestAuditExport(t *testing.T) {
	f, db := filesFront(t)
	ctx := context.Background()
	for _, p := range []map[string]any{{"step": "one"}, {"step": "two", "n": 2}} {
		if err := f.API.appendAudit(ctx, auditEvent{OrgID: "org", EntityKind: "memory", EntityID: "m1", Event: "created", Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	var prev string
	if err := db.QueryRow(ctx, `select chain_hash from audit_chain where org_id = 'org' and seq = 2`).Scan(&prev); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into audit_chain (org_id, seq, entity_kind, entity_id, event, payload, payload_hash, prev_hash, chain_hash, created_at)
		values ('org', 3, 'action', 'a1', 'approved', $1::jsonb, $2, $3, $4, '2026-11-12T08:00:00.123456Z')`,
		nodePayload, nodePayloadHash, prev, sha256Hex(prev+"\x003\x00"+nodePayloadHash))
	admin := sessionCookie("admin")

	if w := call(f, "GET", "/api/v1/work/audit/export", "", sessionCookie("member")); w.Code != 403 || w.Body.String() != `{"error":"admin only"}` {
		t.Errorf("member: %d %s", w.Code, w.Body)
	}
	w := call(f, "GET", "/api/work/audit/export?since=abc", "", admin)
	lines := strings.Split(w.Body.String(), "\n")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-ndjson" || w.Header().Get("X-Audit-Chain-Ok") != "true" ||
		w.Header().Get("X-Audit-Chain-Length") != "3" || w.Header().Get("X-Audit-Chain-Broken-At") != "" || len(lines) != 3 {
		t.Fatalf("export: %d %v %q", w.Code, w.Header(), w.Body)
	}
	if want := `{"seq":3,"entityKind":"action","entityId":"a1","event":"approved","payload":{"2":null,"10":true,"a":[1,"é",1e+21,0.1],"b":1.5,"！":1,"😀":2},` +
		`"payloadHash":"` + nodePayloadHash + `","prevHash":"` + prev + `","chainHash":"` + sha256Hex(prev+"\x003\x00"+nodePayloadHash) + `","createdAt":"2026-11-12T08:00:00.123Z"}`; lines[2] != want {
		t.Errorf("line 3:\n%s\nwant\n%s", lines[2], want)
	}
	if !strings.HasPrefix(lines[0], `{"seq":1,"entityKind":"memory","entityId":"m1","event":"created","payload":{"step":"one"},"payloadHash":"`) {
		t.Errorf("line 1: %s", lines[0])
	}
	for q, want := range map[string]int{"since=2": 1, "since=1.5": 2, "since=-4": 3, "since=3": 0, "since=Infinity": 0} {
		w := call(f, "GET", "/api/v1/work/audit/export?"+q, "", admin)
		got := 0
		if w.Body.Len() > 0 {
			got = strings.Count(w.Body.String(), "\n") + 1
		}
		if w.Code != 200 || got != want {
			t.Errorf("%s: %d, %d lines, want %d", q, w.Code, got, want)
		}
	}

	seed(t, db, `update audit_chain set payload = '{"step":"edited"}' where org_id = 'org' and seq = 2`)
	w = call(f, "GET", "/api/v1/work/audit/export", "", admin)
	if w.Header().Get("X-Audit-Chain-Ok") != "false" || w.Header().Get("X-Audit-Chain-Broken-At") != "2" || w.Header().Get("X-Audit-Chain-Length") != "3" {
		t.Errorf("edited: %v", w.Header())
	}
	seed(t, db, `delete from audit_chain where org_id = 'org' and seq = 1`)
	w = call(f, "GET", "/api/v1/work/audit/export", "", admin)
	if w.Header().Get("X-Audit-Chain-Broken-At") != "1" || w.Header().Get("X-Audit-Chain-Length") != "2" {
		t.Errorf("gap: %v", w.Header())
	}
}

func TestWorkFileDownload(t *testing.T) {
	f, db := filesFront(t)
	roots, err := orgWorkspace("org")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := filepath.Join(roots.Runs, runMine, "artifacts")
	writeTree(t, artifacts, map[string]string{"quote.xlsx": "workbook", "quotes/final.csv": "a,b\n", "unpublished.csv": "x", `my "q".txt`: "q"})
	writeTree(t, roots.Uploads, map[string]string{"thread-1/private.txt": "private"})
	if err := os.Symlink(filepath.Join(roots.Uploads, "thread-1", "private.txt"), filepath.Join(artifacts, "link.txt")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"runs/" + runMine + "/artifacts/quote.xlsx", "/workspace/org/runs/" + runMine + "/artifacts/quotes/final.csv",
		"runs/" + runMine + "/artifacts/link.txt", "runs/" + runMine + "/artifacts/../escape.txt", "runs/" + runMine + `/artifacts/my "q".txt`} {
		payload, _ := json.Marshal(map[string]any{"type": "artifact", "artifact": map[string]any{"path": p}})
		seed(t, db, `insert into work_run_event (org_id, thread_id, run_id, kind, payload) values ('org', $1, $2, 'artifact', $3)`, threadMine, runMine, string(payload))
	}
	seed(t, db, `insert into work_run_event (org_id, thread_id, run_id, kind, payload) values ('org', $1, $2, 'text', '{"type":"text","artifact":{"path":"runs/`+runMine+`/artifacts/unpublished.csv"}}')`, threadMine, runMine)
	member := sessionCookie("member")
	base := "/api/v1/work/files/runs/" + runMine + "/artifacts/"

	w := call(f, "GET", base+"quote.xlsx", "", member)
	if w.Code != 200 || w.Body.String() != "workbook" || w.Header().Get("Content-Disposition") != `attachment; filename="quote.xlsx"` ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Type") != mimeByExt[".xlsx"] {
		t.Errorf("download: %d %v %q", w.Code, w.Header(), w.Body)
	}
	if w := call(f, "GET", "/api/work/files/runs/"+runMine+"/artifacts/quotes/final.csv", "", member); w.Code != 200 || w.Body.String() != "a,b\n" {
		t.Errorf("nested, absolute event path: %d %s", w.Code, w.Body)
	}
	if w := call(f, "GET", base+"my%20%22q%22.txt", "", member); w.Code != 200 || w.Header().Get("Content-Disposition") != `attachment; filename="my q.txt"` ||
		w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("quoted name: %d %v", w.Code, w.Header())
	}
	for _, c := range []struct {
		path string
		who  map[string]string
	}{
		{base + "quote.xlsx", sessionCookie("admin")},
		{base + "quote.xlsx", nil},
		{"/api/v1/work/files/uploads/thread-1/private.txt", member},
		{"/api/v1/work/files/memory/MEMORY.md", member},
		{"/api/v1/work/files/skills/pdf/SKILL.md", member},
		{"/api/v1/work/files/runs/" + runMine + "/not-artifacts/quote.xlsx", member},
		{"/api/v1/work/files/runs/" + runMine + "/artifacts", member},
		{base + "unpublished.csv", member},
		{base + "link.txt", member},
		{base + "%2E%2E/escape.txt", member},
		{base + "missing.csv", member},

		{"/api/v1/work/files/runs/not-a-uuid/artifacts/quote.xlsx", member},
	} {
		w := call(f, "GET", c.path, "", c.who)
		if c.who == nil {
			if w.Code != 302 {
				t.Errorf("%s signed out: %d", c.path, w.Code)
			}
			continue
		}
		if w.Code != 404 || w.Body.String() != `{"error":"not found"}` {
			t.Errorf("%s: %d %s", c.path, w.Code, w.Body)
		}
	}
	for _, c := range []struct {
		value string
		ok    bool
	}{
		{"uploads/thread-1/input.csv", false}, {"runs/other/artifacts/final.xlsx", false},
		{"runs/" + runMine + "/artifacts//final.xlsx", false}, {"runs/" + runMine + "/artifacts", false},
		{"/tmp/runs/" + runMine + "/artifacts-evil/final.xlsx", false}, {"  ", false},
		{`runs\` + runMine + `\artifacts\a.csv`, true}, {"x/runs/" + runMine + "/artifacts/a\x00.csv", false},
	} {
		if _, ok := canonicalRunArtifactPath(c.value, runMine); ok != c.ok {
			t.Errorf("canonical %q: %v", c.value, ok)
		}
	}
}

type fakeServe struct {
	mu     sync.Mutex
	status int
	puts   []string
	bodies [][]byte
	tokens []string
}

func (s *fakeServe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	s.puts = append(s.puts, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Content-Type"))
	s.bodies = append(s.bodies, body)
	s.tokens = append(s.tokens, r.Header.Get("Authorization"))
	if s.status != 0 {
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(`{"error":"disk full"}`))
		return
	}
	_, _ = w.Write([]byte(`{"path":"x","size":1,"modified_at":""}`))
}

type part struct {
	name, filename, contentType, body string
}

func multipartBody(t *testing.T, parts ...part) (string, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		if p.filename != "-" {
			h.Set("Content-Disposition", `form-data; name="`+p.name+`"; filename="`+p.filename+`"`)
			if p.contentType != "" {
				h.Set("Content-Type", p.contentType)
			}
		} else {
			h.Set("Content-Disposition", `form-data; name="`+p.name+`"`)
		}
		w, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(p.body))
	}
	_ = mw.Close()
	return buf.String(), mw.FormDataContentType()
}

func upload(f http.Handler, cookie map[string]string, body, contentType string) *httptest.ResponseRecorder {
	h := map[string]string{"Content-Type": contentType}
	for k, v := range cookie {
		h[k] = v
	}
	return call(f, "POST", "/api/v1/work/upload", body, h)
}

func TestWorkUpload(t *testing.T) {
	f, db := filesFront(t)
	serve := &fakeServe{}
	srv := httptest.NewServer(serve)
	t.Cleanup(srv.Close)
	member := sessionCookie("member")
	roots, _ := orgWorkspace("org")
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("upload-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	thread := part{name: "threadId", filename: "-", body: " " + threadMine + " "}

	for _, c := range []struct {
		body, contentType string
		want              int
		msg               string
	}{
		{"x", "text/plain", 400, "Invalid multipart body"},
		{"threadId=" + threadMine, "application/x-www-form-urlencoded", 400, "threadId and file are required"},
	} {
		if w := upload(f, member, c.body, c.contentType); w.Code != c.want || w.Body.String() != `{"error":"`+c.msg+`"}` {
			t.Errorf("%s: %d %s", c.contentType, w.Code, w.Body)
		}
	}
	for _, c := range []struct {
		parts []part
		want  int
		msg   string
	}{
		{[]part{thread}, 400, `threadId and file are required`},
		{[]part{{name: "file", filename: "a.csv", body: "a"}}, 400, `threadId and file are required`},
		{[]part{thread, {name: "file", filename: "-", body: "a"}}, 400, `threadId and file are required`},
		{[]part{thread, {name: "file", filename: "a.csv", contentType: "IMAGE/PNG", body: "a"}}, 415, `Image uploads are not supported yet.`},
		{[]part{thread, {name: "file", filename: "big.csv", body: strings.Repeat("x", maxUploadSize+1)}}, 413, `\"big.csv\" is over 10 MB.`},
		{[]part{thread, {name: "file", filename: "run.EXE", body: "a"}}, 415, `Unsupported file type \".exe\".`},
		{[]part{thread, {name: "file", filename: "README", body: "a"}}, 415, `Unsupported file type \"(none)\".`},
		{[]part{{name: "threadId", filename: "-", body: threadOther}, {name: "file", filename: "a.csv", body: "a"}}, 404, `Thread not found`},
		{[]part{{name: "threadId", filename: "-", body: "nope"}, {name: "file", filename: "a.csv", body: "a"}}, 404, `Thread not found`},
	} {
		body, ct := multipartBody(t, c.parts...)
		r := httptest.NewRequest("POST", "/api/v1/work/upload", strings.NewReader(body))
		r.ContentLength = -1
		r.Header.Set("Content-Type", ct)
		r.Header.Set("Cookie", member["Cookie"])
		w := httptest.NewRecorder()
		f.ServeHTTP(w, r)
		if w.Code != c.want || w.Body.String() != `{"error":"`+c.msg+`"}` {
			t.Errorf("%v: %d %s", c.parts[len(c.parts)-1].filename, w.Code, w.Body)
		}
	}
	body, ct := multipartBody(t, thread, part{name: "file", filename: "x.csv", body: strings.Repeat("x", maxUploadSize+8192)})
	if w := upload(f, member, body, ct); w.Code != 413 || w.Body.String() != `{"error":"File is over 10 MB."}` {
		t.Errorf("content-length: %d %s", w.Code, w.Body)
	}

	body, ct = multipartBody(t, thread, part{name: "file", filename: "../Q3 report (final).csv", contentType: "text/csv", body: "a,b\n"})
	w := upload(f, member, body, ct)
	saved := filepath.Join(roots.Uploads, threadMine, "Q3_report__final_.csv")
	if want := `{"file":{"name":"Q3_report__final_.csv","size":4,"relativePath":"uploads/` + threadMine + `/Q3_report__final_.csv","absolutePath":"` + saved + `"}}`; w.Code != 200 || w.Body.String() != want {
		t.Errorf("upload: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(saved); string(b) != "a,b\n" || len(serve.puts) != 0 {
		t.Errorf("saved %q, serve calls %v", b, serve.puts)
	}

	t.Setenv("OPENNEKO_SERVE_URL", srv.URL+"/")
	for i := 0; i < 2; i++ {
		body, ct = multipartBody(t, thread, part{name: "file", filename: "notes.md", contentType: "text/markdown", body: "# Notes\n"})
		if w := upload(f, member, body, ct); w.Code != 200 {
			t.Fatalf("catalog upload: %d %s", w.Code, w.Body)
		}
	}
	var rows int
	var path, docThread, status, hash string
	if err := db.QueryRow(context.Background(), `select count(*) over (), relative_path, source_thread_id::text, status, content_hash from library_document where org_id = 'org' and user_id = 'member'`).
		Scan(&rows, &path, &docThread, &status, &hash); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || path != "files/library/"+hash+"/notes.md" || docThread != threadMine || status != "ready" || hash != sha256Hex("# Notes\n") {
		t.Errorf("library row: %d %s %s %s %s", rows, path, docThread, status, hash)
	}
	if len(serve.puts) != 2 || serve.puts[0] != "PUT /v1/files/content?path=library%2F"+hash+"%2Fnotes.md " || string(serve.bodies[0]) != "# Notes\n" {
		t.Errorf("serve calls: %v", serve.puts)
	}
	claims := strings.Split(strings.TrimPrefix(serve.tokens[0], "Bearer "), ".")
	raw, _ := base64.RawURLEncoding.DecodeString(claims[1])
	if !strings.HasPrefix(string(raw), `{"org":"org","sub":"member","exp":`) {
		t.Errorf("token claims: %s", raw)
	}

	body, ct = multipartBody(t, thread, part{name: "catalog", filename: "-", body: "false"}, part{name: "file", filename: "skip.txt", body: "s"})
	if w := upload(f, member, body, ct); w.Code != 200 || len(serve.puts) != 2 {
		t.Errorf("catalog=false: %d, serve calls %d", w.Code, len(serve.puts))
	}
	serve.status = 500
	body, ct = multipartBody(t, thread, part{name: "file", filename: "fail.txt", body: "f"})
	if w := upload(f, member, body, ct); w.Code != 200 || len(serve.puts) != 3 {
		t.Errorf("serve failure: %d, serve calls %d", w.Code, len(serve.puts))
	}
	if err := db.QueryRow(context.Background(), `select count(*) from library_document where org_id = 'org'`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("library rows after failure: %d %v", rows, err)
	}
}

func TestSafeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"hello world.csv": "hello_world.csv", "rep@rt 2025.pdf": "rep_rt_2025.pdf", ".env": "_env",
		"..hidden.json": "_hidden.json", "../../etc/passwd": "passwd", `foo\bar.txt`: "bar.txt", "": "upload.bin",
		"dir/": "dir", "/": "upload.bin", "résumé😀.md": "r_sum___.md", strings.Repeat("a", 250) + ".txt": strings.Repeat("a", 200),
	} {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
