package webfront

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestMoveLibraryDocuments(t *testing.T) {
	f, db := filesFront(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("library-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := orgAgentRoot("org")
	for path, body := range map[string]string{"library/uploads/q3.csv": "a,b\n", "threads/t1/notes.md": "# N\n", "outside.txt": "o"} {
		full := filepath.Join(root, path)
		if path == "outside.txt" {
			full = filepath.Join(filepath.Dir(root), path)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seed(t, db, `insert into library_document (org_id, user_id, filename, relative_path, content_hash, size_bytes, status, error) values
		('org', 'member', 'q3.csv', 'library/uploads/q3.csv', 'h1', 4, 'ready', null),
		('org', 'admin', 'notes.md', '/threads/t1/notes.md', 'h2', 4, 'ready', null),
		('org', null, 'orphan.csv', 'library/uploads/orphan.csv', 'h3', 1, 'ready', null),
		('org', 'member', 'gone.csv', 'library/uploads/gone.csv', 'h4', 1, 'ready', null),
		('org', 'member', 'outside.txt', '../outside.txt', 'h5', 1, 'ready', null),
		('org', 'member', 'new.csv', 'files/library/h6/new.csv', 'h6', 1, 'ready', null),
		('org', 'member', 'bad.csv', 'library/uploads/bad.csv', 'h7', 1, 'failed', 'old error')`)
	state := func() []string {
		rows, err := db.Query(ctx, `select filename || ' ' || relative_path || ' ' || status || ' ' || coalesce(error, '-')
			from library_document order by filename collate "C"`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}

	serve := &fakeServe{}
	srv := httptest.NewServer(serve)
	t.Setenv("OPENNEKO_SERVE_URL", srv.URL)
	srv.Close()
	before := state()
	if _, _, err := f.API.MoveLibraryDocuments(ctx); err == nil {
		t.Fatal("serve down: no error")
	}
	if got := state(); got[3] != before[3] || got[6] != before[6] {
		t.Errorf("serve down moved a document: %q", got)
	}

	srv = httptest.NewServer(serve)
	t.Cleanup(srv.Close)
	t.Setenv("OPENNEKO_SERVE_URL", srv.URL)
	moved, missing, err := f.API.MoveLibraryDocuments(ctx)
	if err != nil || moved != 2 || missing != 3 {
		t.Fatalf("move: %d %d %v", moved, missing, err)
	}
	q3, notes := sha256Hex("a,b\n"), sha256Hex("# N\n")
	want := []string{
		"bad.csv library/uploads/bad.csv failed old error",
		"gone.csv library/uploads/gone.csv failed The original file is missing. Remove this document and upload it again.",
		"new.csv files/library/h6/new.csv ready -",
		"notes.md files/library/" + notes + "/notes.md ready -",
		"orphan.csv library/uploads/orphan.csv failed This document has no owner. Remove it and upload it again.",
		"outside.txt ../outside.txt failed The original file is missing. Remove this document and upload it again.",
		"q3.csv files/library/" + q3 + "/q3.csv ready -",
	}
	if got := state(); !slices.Equal(got, want) {
		t.Errorf("rows:\n%q\nwant\n%q", got, want)
	}
	if bodies := []string{string(serve.bodies[0]), string(serve.bodies[1])}; len(serve.puts) != 2 || !slices.Contains(bodies, "a,b\n") || !slices.Contains(bodies, "# N\n") {
		t.Errorf("serve calls: %v", serve.puts)
	}
	if _, err := os.Stat(filepath.Join(root, "library/uploads/q3.csv")); !os.IsNotExist(err) {
		t.Errorf("library upload kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "threads/t1/notes.md")); err != nil {
		t.Errorf("thread copy removed: %v", err)
	}
	if moved, missing, err := f.API.MoveLibraryDocuments(ctx); err != nil || moved != 0 || missing != 0 || len(serve.puts) != 2 {
		t.Errorf("second run: %d %d %v %v", moved, missing, err, serve.puts)
	}
}
