package webfront

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func libraryUpload(t *testing.T, f *Front, who map[string]string, parts ...part) *httptest.ResponseRecorder {
	body, ct := multipartBody(t, parts...)
	h := map[string]string{"Content-Type": ct}
	for k, v := range who {
		h[k] = v
	}
	return call(f, "POST", "/api/v1/library/upload", body, h)
}

func TestLibraryBrowse(t *testing.T) {
	f, db := filesFront(t)
	member, admin := sessionCookie("member"), sessionCookie("admin")
	seed(t, db, `insert into library_document (org_id, user_id, filename, relative_path, content_hash, size_bytes, status, created_at)
		select 'org', 'member', case when i = 7 then 'Q4 price_list 100%.xlsx' else 'Report ' || lpad(i::text, 3, '0') || '.pdf' end,
			'files/library/' || i || '/doc.pdf', 'browser-' || i, 100, case when i = 3 then 'failed' else 'ready' end,
			timestamptz '2026-11-01 00:00:00Z' + i * interval '1 minute'
		from generate_series(0, 124) i`)
	seed(t, db, `insert into library_document (org_id, user_id, filename, relative_path, content_hash, size_bytes, status)
		values ('org', 'admin', 'Admin secret.pdf', 'files/library/x/doc.pdf', 'admin', 1, 'ready')`)

	for _, q := range []string{"page=0", "page=-1", "page=1.5", "page=Infinity", "page=", "page=x", "pageSize=101", "pageSize=0",
		"sort=bad", "sort=", "status=cataloged", "q=" + strings.Repeat("x", 201), "q=" + strings.Repeat("%F0%9F%98%80", 101)} {
		if w := call(f, "GET", "/api/v1/library?"+q, "", member); w.Code != 400 || w.Body.String() != `{"error":"Invalid library filters."}` {
			t.Errorf("%s: %d %s", q, w.Code, w.Body)
		}
	}
	type page struct {
		Total, Page, PageSize int
		Documents             []map[string]any
	}
	get := func(who map[string]string, q string) page {
		t.Helper()
		w := call(f, "GET", "/api/v1/library?"+q, "", who)
		var p page
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
		return p
	}
	if p := get(member, ""); p.Total != 125 || p.Page != 1 || p.PageSize != 50 || len(p.Documents) != 50 || p.Documents[0]["filename"] != "Report 124.pdf" {
		t.Errorf("first page: %+v", p.Documents[0])
	}
	if p := get(member, "page=3"); len(p.Documents) != 25 || p.Page != 3 {
		t.Errorf("last page: %d %d", p.Page, len(p.Documents))
	}
	if p := get(member, "page=99&pageSize=1e1"); p.Page != 13 || p.PageSize != 10 || len(p.Documents) != 5 {
		t.Errorf("clamped page: %d %d %d", p.Page, p.PageSize, len(p.Documents))
	}
	if p := get(member, "sort=name&pageSize=2"); p.Documents[0]["filename"] != "Q4 price_list 100%.xlsx" || p.Documents[1]["filename"] != "Report 000.pdf" {
		t.Errorf("name sort: %v", p.Documents)
	}
	if p := get(admin, ""); p.Total != 1 || p.Documents[0]["filename"] != "Admin secret.pdf" {
		t.Errorf("admin sees: %+v", p)
	}
	if p := get(member, "q="+url.QueryEscape(" price_list  100% ")); p.Total != 1 || p.Documents[0]["filename"] != "Q4 price_list 100%.xlsx" {
		t.Errorf("literal words: %+v", p)
	}
	for q, want := range map[string]int{"q=Admin": 0, "q=report+12": 7, "q=%25": 1, "q=_": 1, "status=failed": 1, "status=": 125} {
		if p := get(member, q); p.Total != want {
			t.Errorf("%s: %d", q, p.Total)
		}
	}
	w := call(f, "GET", "/api/v1/library?status=failed", "", member)
	var id string
	_ = db.QueryRow(context.Background(), `select id::text from library_document where content_hash = 'browser-3'`).Scan(&id)
	if want := `{"total":1,"page":1,"pageSize":50,"documents":[{"id":"` + id + `","filename":"Report 003.pdf","relativePath":"files/library/3/doc.pdf","sizeBytes":100,"status":"failed","error":null,"createdAt":"2026-11-01T00:03:00.000Z"}]}`; w.Body.String() != want {
		t.Errorf("shape:\n%s\n%s", w.Body, want)
	}
}

func TestLibraryDocument(t *testing.T) {
	f, db := filesFront(t)
	serve := &fakeServe{}
	srv := httptest.NewServer(serve)
	t.Cleanup(srv.Close)
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("library-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	member, admin := sessionCookie("member"), sessionCookie("admin")
	var mine, other, moved string
	ctx := context.Background()
	_ = db.QueryRow(ctx, `insert into library_document (org_id, user_id, source_thread_id, filename, relative_path, content_hash, size_bytes, status, error, created_at, updated_at)
		values ('org', 'member', $1, 'a.md', 'files/library/h/a.md', 'h', 3, 'ready', null, '2026-11-02T10:00:00Z', '2026-11-02T11:00:00.5Z') returning id::text`, threadMine).Scan(&mine)
	_ = db.QueryRow(ctx, `insert into library_document (org_id, user_id, filename, relative_path, content_hash, size_bytes, status, error)
		values ('org', 'admin', 'b.md', 'files/library/g/b.md', 'g', 1, 'failed', 'bad') returning id::text`).Scan(&other)
	_ = db.QueryRow(ctx, `insert into library_document (org_id, user_id, filename, relative_path, content_hash, size_bytes, status)
		values ('org', 'member', 'old.md', 'library/uploads/old.md', 'o', 1, 'ready') returning id::text`).Scan(&moved)

	for _, path := range []string{"nope", other, strings.ToUpper(mine) + "0"} {
		if w := call(f, "GET", "/api/v1/library/documents/"+path, "", member); w.Code != 404 || w.Body.String() != `{"error":"Not found."}` {
			t.Errorf("get %s: %d %s", path, w.Code, w.Body)
		}
		if w := call(f, "DELETE", "/api/v1/library/documents/"+path, "", member); w.Code != 404 || w.Body.String() != `{"error":"not found"}` {
			t.Errorf("delete %s: %d %s", path, w.Code, w.Body)
		}
	}
	w := call(f, "GET", "/api/v1/library/documents/"+strings.ToUpper(mine), "", member)
	if want := `{"document":{"id":"` + mine + `","orgId":"org","userId":"member","sourceThreadId":"` + threadMine + `","filename":"a.md","relativePath":"files/library/h/a.md","contentHash":"h","sizeBytes":3,"status":"ready","error":null,"createdAt":"2026-11-02T10:00:00.000Z","updatedAt":"2026-11-02T11:00:00.500Z"}}`; w.Code != 200 || w.Body.String() != want {
		t.Errorf("get:\n%s\n%s", w.Body, want)
	}
	if w := call(f, "GET", "/api/v1/library/documents/"+other, "", admin); w.Code != 200 || !strings.Contains(w.Body.String(), `"error":"bad"`) {
		t.Errorf("admin get: %d %s", w.Code, w.Body)
	}

	t.Setenv("OPENNEKO_SERVE_URL", srv.URL)
	if w := call(f, "DELETE", "/api/v1/library/documents/"+mine, "", member); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if len(serve.puts) != 1 || serve.puts[0] != "DELETE /v1/files/content?path=library%2Fh%2Fa.md " {
		t.Errorf("serve calls: %v", serve.puts)
	}
	serve.status = 500
	if w := call(f, "DELETE", "/api/v1/library/documents/"+moved, "", member); w.Code != 200 || len(serve.puts) != 1 {
		t.Errorf("delete outside the store: %d, serve calls %v", w.Code, serve.puts)
	}
	var left int
	_ = db.QueryRow(ctx, `select count(*) from library_document where org_id = 'org'`).Scan(&left)
	if left != 1 {
		t.Errorf("rows left: %d", left)
	}
	if w := call(f, "DELETE", "/api/v1/library/documents/"+mine, "", member); w.Code != 404 {
		t.Errorf("second delete: %d", w.Code)
	}
}

func TestLibraryUpload(t *testing.T) {
	f, db := filesFront(t)
	serve := &fakeServe{}
	srv := httptest.NewServer(serve)
	t.Cleanup(srv.Close)
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("library-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	member := sessionCookie("member")
	big := strings.Repeat("x", 51*1024*1024)

	for _, c := range []struct {
		parts []part
		want  int
		msg   string
	}{
		{nil, 400, `Choose at least one file to import.`},
		{[]part{{name: "files", filename: "-", body: "text"}}, 400, `Choose at least one file to import.`},
		{[]part{{name: "files", filename: "a.csv", body: "a"}, {name: "files", filename: "e.csv", body: ""}}, 400, `\"e.csv\" is empty. Choose a non-empty file.`},
		{[]part{{name: "files", filename: "a.pdf", body: big}, {name: "file", filename: "b.pdf", body: big}}, 413, libraryOverLimit},
		{[]part{{name: "files", filename: "a b.csv", body: "a"}, {name: "file", filename: "a_b.csv", body: "b"}}, 400,
			`More than one selected file becomes \"a_b.csv\" after filename cleanup. Rename one and try again.`},
		{[]part{{name: "files", filename: "a.csv", body: "a"}, {name: "files", filename: "run.EXE", body: "b"}}, 415, `Unsupported file type \".exe\".`},
		{[]part{{name: "files", filename: "README", body: "b"}}, 415, `Unsupported file type \"(none)\".`},
		{[]part{{name: "files", filename: "a.csv", body: "a"}}, 503, `File storage is off: openneko serve is not configured.`},
	} {
		if w := libraryUpload(t, f, member, c.parts...); w.Code != c.want || w.Body.String() != `{"error":"`+c.msg+`"}` {
			t.Errorf("%v: %d %.200s", len(c.parts), w.Code, w.Body)
		}
	}
	if w := call(f, "POST", "/api/v1/library/upload", "x", map[string]string{"Content-Type": "text/plain", "Cookie": member["Cookie"]}); w.Code != 400 || w.Body.String() != `{"error":"Invalid multipart body"}` {
		t.Errorf("not a form: %d %s", w.Code, w.Body)
	}
	body, ct := multipartBody(t, part{name: "files", filename: "a.pdf", body: strings.Repeat("x", maxLibraryUploadRequest)})
	for _, length := range []int64{int64(len(body)), -1} {
		r := httptest.NewRequest("POST", "/api/v1/library/upload", strings.NewReader(body))
		r.ContentLength = length
		r.Header.Set("Content-Type", ct)
		r.Header.Set("Cookie", member["Cookie"])
		w := httptest.NewRecorder()
		f.ServeHTTP(w, r)
		if w.Code != 413 || w.Body.String() != `{"error":"`+libraryOverLimit+`"}` {
			t.Errorf("request limit %d: %d %.200s", length, w.Code, w.Body)
		}
	}
	if len(serve.puts) != 0 {
		t.Fatalf("serve called before checks passed: %v", serve.puts)
	}

	t.Setenv("OPENNEKO_SERVE_URL", srv.URL)
	w := libraryUpload(t, f, member, part{name: "files", filename: "../Q3 report.csv", contentType: "text/csv", body: "a,b\n"},
		part{name: "note", filename: "-", body: "ignored"}, part{name: "file", filename: "notes.md", body: "# N\n"})
	var got struct {
		OK        bool
		Documents []struct {
			Document libraryDocument
			Created  bool
		}
		Document libraryDocument
		Created  bool
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !got.OK || !got.Created || len(got.Documents) != 2 || got.Document.ID != got.Documents[0].Document.ID {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if !strings.HasPrefix(w.Body.String(), `{"ok":true,"documents":[{"document":{"id":"`) || !strings.HasSuffix(w.Body.String(), `"created":true}`) {
		t.Errorf("order: %s", w.Body)
	}
	d := got.Documents[0].Document
	hash := sha256Hex("a,b\n")
	if d.Filename != "Q3_report.csv" || d.RelativePath != "files/library/"+hash+"/Q3_report.csv" || d.ContentHash != hash || d.SizeBytes != 4 ||
		d.Status != "ready" || *d.UserID != "member" || d.SourceThreadID != nil || d.OrgID != "org" {
		t.Errorf("document: %+v", d)
	}
	if len(serve.puts) != 2 || serve.puts[0] != "PUT /v1/files/content?path=library%2F"+hash+"%2FQ3_report.csv text/csv" ||
		serve.puts[1] != "PUT /v1/files/content?path=library%2F"+sha256Hex("# N\n")+"%2Fnotes.md " || string(serve.bodies[0]) != "a,b\n" {
		t.Errorf("serve calls: %v", serve.puts)
	}

	w = libraryUpload(t, f, member, part{name: "file", filename: "again.csv", body: "a,b\n"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"`+d.ID+`"`) || !strings.HasSuffix(w.Body.String(), `"created":false}`) {
		t.Errorf("same content: %d %s", w.Code, w.Body)
	}

	serve.status = 507
	w = libraryUpload(t, f, member, part{name: "files", filename: "c.txt", body: "c"})
	if w.Code != 502 || w.Body.String() != `{"error":"\"c.txt\" was not stored: disk full","documents":[]}` {
		t.Errorf("serve failure: %d %s", w.Code, w.Body)
	}
	serve.status = 413
	if w := libraryUpload(t, f, member, part{name: "files", filename: "c.txt", body: "c"}); w.Code != 413 {
		t.Errorf("serve refusal: %d %s", w.Code, w.Body)
	}
	srv.Close()
	w = libraryUpload(t, f, member, part{name: "files", filename: "d.txt", body: "d"})
	if w.Code != 502 || w.Body.String() != `{"error":"\"d.txt\" was not stored: openneko serve did not answer","documents":[]}` {
		t.Errorf("serve down: %d %s", w.Code, w.Body)
	}
	var rows int
	_ = db.QueryRow(context.Background(), `select count(*) from library_document where org_id = 'org'`).Scan(&rows)
	if rows != 2 {
		t.Errorf("rows: %d", rows)
	}
}
