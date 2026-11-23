package webfront

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dosco/graphjin/core/v3/fstable"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/filecontent"
	"github.com/open-neko/neko/apps/openneko/internal/files"
)

func startFilesServe(t *testing.T, a *API, db *pgxpool.Pool) {
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
	srv := httptest.NewServer(files.New(store, ix, key, files.Options{Refresh: time.Millisecond}).Handler())
	t.Cleanup(srv.Close)
	t.Setenv("OPENNEKO_SERVE_URL", srv.URL)
}

// walkUpToLibrary finds library/uploads the way a 4.9.1 skill does: from the
// run folder up to the first parent that holds it.
func walkUpToLibrary(t *testing.T, runDir string) (string, map[string][]byte) {
	t.Helper()
	for dir := runDir; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		lib := filepath.Join(dir, "library", "uploads")
		if _, err := os.Stat(lib); err != nil {
			continue
		}
		got := map[string][]byte{}
		err := filepath.WalkDir(lib, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(lib, p)
			data, err := os.ReadFile(p)
			got[filepath.ToSlash(rel)] = data
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return lib, got
	}
	return "", nil
}

func uploadToLibrary(t *testing.T, a *API, user, name string, data []byte) string {
	t.Helper()
	if err := a.catalogUpload(context.Background(), actor{OrgID: "org", UserID: user}, threadMine, name, data); err != nil {
		t.Fatal(err)
	}
	hash, _ := libraryPath(name, data)
	return user + "/" + hash + "/" + name
}

func TestAMemberRunSeesOnlyTheirOwnLibrary(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	startFilesServe(t, a, db)

	alternates := []byte("PK\x03\x04 sku,alternate\nHI-1,HI-9\n")
	mine := uploadToLibrary(t, a, "member", "Alternates Nov.xlsx", alternates)
	theirs := uploadToLibrary(t, a, "other", "Other private.csv", []byte("secret,1\n"))
	admins := uploadToLibrary(t, a, "admin", "Admin private.csv", []byte("secret,2\n"))
	big := uploadToLibrary(t, a, "member", "big.csv", bytes.Repeat([]byte("x"), 4096))
	seed(t, db, `update library_document set created_at = now() - interval '1 day' where filename = 'big.csv'`)

	legacy, legacyOther := "library/uploads/member/abc123/old rates.csv", "library/uploads/other/def456/their rates.csv"
	for _, p := range []string{legacy, legacyOther, "planted.txt"} {
		full := filepath.Join(orgAgentRoot("org"), filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("rate,1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seed(t, db, `insert into library_document (org_id, user_id, filename, relative_path, content_hash, size_bytes, status) values
		('org', 'member', 'old rates.csv', $1, 'abc123', 7, 'ready'),
		('org', 'other', 'their rates.csv', $2, 'def456', 7, 'ready'),
		('org', 'member', 'escape.csv', 'library/uploads/../../planted.txt', 'e', 7, 'ready'),
		('org', 'member', 'broken.csv', 'files/library/gone/broken.csv', 'gone', 1, 'failed')`, legacy, legacyOther)

	old := runLibraryBudget
	runLibraryBudget = int64(len(alternates)) + 7 + 100
	t.Cleanup(func() { runLibraryBudget = old })
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	var runID string
	if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, actor_user_id, actor_role) values ('org', $1, 'ax', 'member', 'member') returning id::text`,
		threadMine).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	var lib string
	var seen map[string][]byte
	var writeErr error
	_, err := a.runChatTurn(ctx, chatTurnOptions{OrgID: "org", ThreadID: threadMine, RunID: runID, Message: "Find alternates for HI-1",
		Channel: "web", Emit: (&eventLog{}).emit, Summarize: noSummary(t), resolve: fixedBackend("http://unused"),
		keepBot: func(context.Context, botKeepInput) error { return nil },
		runCore: func(_ context.Context, in agentCoreInput) (axRunResult, error) {
			lib, seen = walkUpToLibrary(t, in.Workspace.RunRoot)
			if _, err := os.Stat(filepath.Join(in.Workspace.OrgRoot, "planted.txt")); err == nil {
				t.Error("a library path that climbs out of library/uploads reached the stage")
			}
			if lib != "" {
				writeErr = os.WriteFile(filepath.Join(lib, filepath.FromSlash(mine)), []byte("edited"), 0o644)
			}
			return axRunResult{Status: "completed", FinalText: "Done"}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if lib == "" {
		t.Fatal("the run found no library/uploads above its run folder")
	}
	if !strings.HasPrefix(lib, agentRunsRoot()) || filepath.Base(filepath.Dir(filepath.Dir(lib))) != filepath.Base(orgAgentRoot("org")) {
		t.Errorf("library staged at %s, want under the staged org root", lib)
	}
	var names []string
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"member/abc123/old rates.csv", mine}; !slices.Equal(names, want) {
		t.Errorf("staged library %q, want %q (left out: %s, %s, %s)", names, want, theirs, admins, big)
	}
	if !bytes.Equal(seen[mine], alternates) {
		t.Errorf("staged %s = %q", mine, seen[mine])
	}
	if !errors.Is(writeErr, fs.ErrPermission) {
		t.Errorf("the run wrote to a staged library file: %v", writeErr)
	}
	if !strings.Contains(logs.String(), "library file big.csv (4096 bytes) left out: over the") {
		t.Errorf("no log line for the file over the budget:\n%s", logs.String())
	}
}

func TestAWorkflowRunSeesItsOwnersLibrary(t *testing.T) {
	a, db, wf := workflowTurnFront(t, `[{"id":"s1","description":"Find alternates","script":"skills/alternates/run.py"}]`)
	startFilesServe(t, a, db)
	mine := uploadToLibrary(t, a, "member", "alternates.xlsx", []byte("sku,alternate\n"))
	uploadToLibrary(t, a, "other", "theirs.xlsx", []byte("secret\n"))
	seed(t, db, `update work_run set actor_user_id = 'member', actor_role = 'member' where id = $1`, wfTurnWorkRun)

	o := workflowTurnOpts(wf, &eventLog{})
	var seen map[string][]byte
	o.runCore = func(_ context.Context, in agentCoreInput) (axRunResult, error) {
		_, seen = walkUpToLibrary(t, in.Workspace.RunRoot)
		return axRunResult{Status: "completed", FinalText: "Done"}, nil
	}
	if _, err := a.runWorkflowTurn(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || string(seen[mine]) != "sku,alternate\n" {
		t.Errorf("workflow run library %v, want only the owner's %s", seen, mine)
	}
}
