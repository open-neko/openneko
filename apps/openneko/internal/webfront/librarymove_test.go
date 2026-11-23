package webfront

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dosco/graphjin/core/v3/fstable"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/filecontent"
	"github.com/open-neko/neko/apps/openneko/internal/files"
)

// recordingServe is openneko serve's real files service. It keeps each PUT
// body, and with cut set it stops each file it sends after 512 bytes, as the
// macOS confined network part did when sendfile failed.
type recordingServe struct {
	next http.Handler
	mu   sync.Mutex
	puts []string
	cut  bool
}

func realServe(t *testing.T, a *API, db *pgxpool.Pool) *recordingServe {
	t.Helper()
	store, err := fstable.NewLocal(fstable.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ix, err := filecontent.Open(db, t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	key, err := config.SigningSecret(a.SecretsDir, files.TokenLabel)
	if err != nil {
		t.Fatal(err)
	}
	return &recordingServe{next: files.New(store, ix, key, files.Options{Refresh: time.Millisecond}).Handler()}
}

func (s *recordingServe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cut := s.cut
	if r.Method == http.MethodPut {
		body, _ := io.ReadAll(r.Body)
		s.puts = append(s.puts, string(body))
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	s.mu.Unlock()
	if cut && r.Method == http.MethodGet {
		w = &cutWriter{ResponseWriter: w, left: 512}
	}
	s.next.ServeHTTP(w, r)
}

type cutWriter struct {
	http.ResponseWriter
	left int
}

func (c *cutWriter) Write(p []byte) (int, error) {
	if len(p) > c.left {
		n, _ := c.ResponseWriter.Write(p[:c.left])
		c.left = 0
		return n, errors.New("sendfile: operation not permitted")
	}
	c.left -= len(p)
	return c.ResponseWriter.Write(p)
}

// TestLibraryMoveKeepsTheOriginalWhenServeSendsLess: serve stores the file
// but sends back only part of it. The move keeps the original and the row,
// and a later move with a whole read-back finishes the job.
func TestLibraryMoveKeepsTheOriginalWhenServeSendsLess(t *testing.T) {
	f, db := filesFront(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("library-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 24705)
	_, _ = rand.Read(data)
	original := filepath.Join(orgAgentRoot("org"), "library", "uploads", "member", "h1", "alternates.xlsx")
	if err := os.MkdirAll(filepath.Dir(original), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, data, 0o600); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into library_document (org_id, user_id, filename, relative_path, content_hash, size_bytes, status, error) values
		('org', 'member', 'alternates.xlsx', 'library/uploads/member/h1/alternates.xlsx', 'h1', 24705, 'ready', null)`)
	row := func() string {
		var s string
		if err := db.QueryRow(ctx, `select relative_path || ' ' || status from library_document where filename = 'alternates.xlsx'`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	serve := realServe(t, f.API, db)
	serve.cut = true
	srv := httptest.NewServer(serve)
	t.Cleanup(srv.Close)
	t.Setenv("OPENNEKO_SERVE_URL", srv.URL)

	moved, _, err := f.API.MoveLibraryDocuments(ctx)
	if err == nil || moved != 0 || !strings.Contains(err.Error(), "read back") {
		t.Fatalf("a cut read-back: moved %d, %v", moved, err)
	}
	if got, err := os.ReadFile(original); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the original is gone or changed after a cut read-back: %v", err)
	}
	if got := row(); got != "library/uploads/member/h1/alternates.xlsx ready" {
		t.Fatalf("row after a cut read-back: %s", got)
	}

	serve.mu.Lock()
	serve.cut = false
	serve.mu.Unlock()
	moved, _, err = f.API.MoveLibraryDocuments(ctx)
	if err != nil || moved != 1 {
		t.Fatalf("move: %d %v", moved, err)
	}
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Fatalf("the original stays after a whole read-back: %v", err)
	}
	path := strings.TrimPrefix(strings.TrimSuffix(row(), " ready"), "files/")
	got, err := f.API.getServeFile(ctx, actor{OrgID: "org", UserID: "member"}, path, int64(len(data)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("serve holds %d of %d bytes: %v", len(got), len(data), err)
	}
}

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

	serve := realServe(t, f.API, db)
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
	if len(serve.puts) != 2 || !slices.Contains(serve.puts, "a,b\n") || !slices.Contains(serve.puts, "# N\n") {
		t.Errorf("serve got: %q", serve.puts)
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
