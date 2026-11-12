package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type knowledgeReply struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

type knowledgeGroup struct {
	Base  map[string]knowledgeReply            `json:"base"`
	Cases map[string]map[string]knowledgeReply `json:"cases"`
}

type fakeKnowledgeServer struct {
	mu      sync.Mutex
	replies map[string]knowledgeReply
}

func (f *fakeKnowledgeServer) use(g knowledgeGroup, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = map[string]knowledgeReply{}
	for k, v := range g.Base {
		f.replies[k] = v
	}
	for k, v := range g.Cases[name] {
		f.replies[k] = v
	}
}

func (f *fakeKnowledgeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.RequestURI(), "/discovery/")
	if r.Method == http.MethodPost {
		var body struct{ Query string }
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		key = body.Query
	}
	f.mu.Lock()
	reply, ok := f.replies[key]
	f.mu.Unlock()
	if !ok {
		reply = knowledgeReply{Status: 500, Body: "{}"}
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(reply.Status)
	_, _ = io.WriteString(w, reply.Body)
}

type fileDigest struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func digestOf(text string) *fileDigest {
	sum := sha256.Sum256([]byte(text))
	return &fileDigest{Bytes: len(text), SHA256: hex.EncodeToString(sum[:])}
}

// prefetchAgenticKnowledge writes the agentic pack into destDir, as
// prefetchAgenticKnowledgePack() does.
func prefetchAgenticKnowledge(ctx context.Context, endpoint, token, destDir string) knowledgeResult {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return knowledgeFailed(err)
	}
	files, written, err := buildAgenticKnowledge(ctx, endpoint, token)
	if err != nil {
		return knowledgeFailed(err)
	}
	for _, name := range knowledgeSnapshotFiles {
		if err := os.WriteFile(filepath.Join(destDir, name), []byte(files[name]), 0o644); err != nil {
			return knowledgeFailed(err)
		}
	}
	return knowledgeResult{OK: true, Files: written}
}

func TestKnowledgePackMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/knowledgecatalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]knowledgeGroup
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("testdata/knowledgepack.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Mode     string                 `json:"mode"`
		Name     string                 `json:"name"`
		Result   *knowledgeResult       `json:"result"`
		Files    map[string]*fileDigest `json:"files"`
		Snapshot *fileDigest            `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 8 {
		t.Fatalf("fixture holds %d cases, want 8", len(cases))
	}
	defer func(d time.Duration) { discoveryRetryDelay = d }(discoveryRetryDelay)
	discoveryRetryDelay = time.Millisecond

	fake := &fakeKnowledgeServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	for _, c := range cases {
		t.Run(c.Mode+"/"+c.Name, func(t *testing.T) {
			dir := t.TempDir()
			if c.Mode == "clear" {
				if err := clearKnowledgeSnapshot(dir, "none", "legacy"); err != nil {
					t.Fatal(err)
				}
				text, _ := os.ReadFile(filepath.Join(dir, knowledgeSnapshotName))
				if got := digestOf(string(text)); *got != *c.Snapshot {
					t.Fatalf("snapshot %+v, want %+v\n%s", got, c.Snapshot, text)
				}
				return
			}
			fake.use(catalog[c.Mode], c.Name)
			var got knowledgeResult
			if c.Mode == "agentic" {
				got = prefetchAgenticKnowledge(context.Background(), srv.URL+"/graphql", "t", dir)
			} else {
				got = prefetchLegacyKnowledge(context.Background(), srv.URL+"/discovery/", dir)
			}
			if got.Files == nil {
				got.Files = []knowledgeFile{}
			}
			if !reflect.DeepEqual(got, *c.Result) {
				t.Fatalf("result %+v, want %+v", got, *c.Result)
			}
			for _, name := range knowledgeSnapshotFiles {
				var have *fileDigest
				if text, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
					have = digestOf(string(text))
				}
				if !reflect.DeepEqual(have, c.Files[name]) {
					t.Errorf("%s: %+v, want %+v", name, have, c.Files[name])
				}
			}
		})
	}
}

func TestKnowledgeSnapshotRefresh(t *testing.T) {
	root := t.TempDir()
	built := map[string]string{"tables.json": `{"tables":[]}`, "namespaces.json": "{}", "insights.json": "{}",
		"syntax.json": "{}", "INDEX.md": "# pack", "mode.json": `{"mode":"agentic"}`}
	var builds, checks atomic.Int32
	revisions := []string{"r1"}
	revision := func() (string, error) {
		n := int(checks.Add(1)) - 1
		return revisions[min(n, len(revisions)-1)], nil
	}
	build := func() (map[string]string, error) {
		builds.Add(1)
		return built, nil
	}

	r := refreshKnowledgeSnapshot(root, "src-a", "agentic", false, revision, build)
	if !r.OK || builds.Load() != 1 || len(r.Files) != 4 || r.Files[0].Bytes != len(built["tables.json"]) {
		t.Fatalf("first build: %+v, builds %d", r, builds.Load())
	}
	snap := readKnowledgeSnapshot(root)
	if snap == nil || snap.Revision != "r1" || snap.Files["INDEX.md"] != "# pack" {
		t.Fatalf("snapshot %+v", snap)
	}
	raw, _ := os.ReadFile(filepath.Join(root, knowledgeSnapshotName))
	if !strings.HasPrefix(string(raw), `{"format":1,"source":"src-a","revision":"r1","files":{"tables.json":`) ||
		!strings.Contains(string(raw), `"builtAt":`) {
		t.Fatalf("snapshot file %s", raw)
	}
	if k := readKnowledgePack(root); k.Mode != "agentic" || k.Tables != `{"tables":[]}` {
		t.Fatalf("pack %+v", k)
	}

	checks.Store(0)
	if r := refreshKnowledgeSnapshot(root, "src-a", "agentic", false, revision, build); !r.OK || checks.Load() != 0 || builds.Load() != 1 {
		t.Fatalf("warm read checked the revision or rebuilt: %+v", r)
	}
	if r := refreshKnowledgeSnapshot(root, "src-a", "agentic", true, revision, build); !r.OK || checks.Load() != 1 || builds.Load() != 1 {
		t.Fatalf("an unchanged revision rebuilt: %+v", r)
	}

	checks.Store(0)
	revisions = []string{"r2", "r3", "r4", "r4"}
	if r := refreshKnowledgeSnapshot(root, "src-a", "agentic", true, revision, build); !r.OK || builds.Load() != 3 {
		t.Fatalf("a revision that moved during the build: %+v, builds %d", r, builds.Load())
	}
	if snap := readKnowledgeSnapshot(root); snap.Revision != "r4" {
		t.Fatalf("revision %q, want r4", snap.Revision)
	}

	checks.Store(0)
	revisions = []string{"r5", "r6", "r7", "r8"}
	r = refreshKnowledgeSnapshot(root, "src-a", "agentic", true, revision, build)
	if r.OK || r.Error != "GraphJin catalog changed during knowledge refresh; retrying on the next sweep" {
		t.Fatalf("a catalog that never settles: %+v", r)
	}
	if snap := readKnowledgeSnapshot(root); snap.Revision != "r4" {
		t.Fatal("a failed refresh replaced the last whole snapshot")
	}

	failed := func() (string, error) { return "", io.ErrUnexpectedEOF }
	r = refreshKnowledgeSnapshot(root, "src-b", "agentic", false, failed, build)
	if r.OK {
		t.Fatal("a new source with no revision gave a pack")
	}
	snap = readKnowledgeSnapshot(root)
	if snap.Source != "src-b" || snap.Revision != "" || snap.Files["tables.json"] != "{}" {
		t.Fatalf("a new source kept the old files: %+v", snap)
	}

	checks.Store(0)
	revisions = []string{"r9"}
	release := make(chan struct{})
	slow := func() (map[string]string, error) {
		<-release
		return build()
	}
	before := builds.Load()
	var wg sync.WaitGroup
	results := make([]knowledgeResult, 4)
	for i := range results {
		wg.Go(func() { results[i] = refreshKnowledgeSnapshot(root, "src-b", "agentic", true, revision, slow) })
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	for _, r := range results {
		if !r.OK {
			t.Fatalf("coalesced refresh: %+v", r)
		}
	}
	if n := builds.Load() - before; n != 1 {
		t.Fatalf("4 callers at once built %d times, want 1", n)
	}
}

func (f *fakeKnowledgeServer) set(key string, reply knowledgeReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[key] = reply
}

func TestTurnContextKnowledgeAndSkillIndex(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	raw, err := os.ReadFile("testdata/knowledgecatalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]knowledgeGroup
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	var roles sync.Map
	fake := &fakeKnowledgeServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := strings.TrimPrefix(r.Header.Get("authorization"), "Bearer "); token != "" {
			var claims struct{ Role string }
			payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
			_ = json.Unmarshal(payload, &claims)
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			kind := "catalog"
			if strings.Contains(string(body), "gj_config") {
				kind = "revision"
			}
			roles.Store(kind+":"+claims.Role, true)
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	fake.use(catalog["agentic"], "full")
	revisionQuery := `query { gj_config(id: "current") { catalog_revision } }`
	fake.set(revisionQuery, knowledgeReply{Status: 200, Body: `{"data":{"gj_config":[{"catalog_revision":"rev-1"}]}}`})
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, auth_mode, is_default) values ('org', 'graphjin', 'main', $1, 'jwt', true)`,
		srv.URL+"/graphql")
	seed(t, db, `insert into skill_embedding (org_id, skill_name, document) values ('org', 'gone-skill', 'gone-skill')`)

	roots, err := orgWorkspace("org")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(roots.Skills, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roots.Skills, "notes", "SKILL.md"), []byte("---\nname: notes\ndescription: Keep notes\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}
	load := func() *turnContext {
		t.Helper()
		tc, err := a.loadTurnContext(ctx, turnOptions{OrgID: "org", ThreadID: threadMine, RunID: runMine, Message: "x", Summarize: noSummary(t)})
		if err != nil {
			t.Fatal(err)
		}
		return tc
	}
	tc := load()
	if !strings.Contains(tc.Prompt, "salesorderheader") {
		t.Fatal("the prompt lacks the agentic pack")
	}
	snap := readKnowledgeSnapshot(tc.Workspace.KnowledgeRoot)
	if snap == nil || snap.Revision != "rev-1" || !strings.Contains(snap.Source, `"jwt","service",2]`) {
		t.Fatalf("snapshot %+v", snap)
	}
	for _, want := range []string{"revision:admin", "catalog:service"} {
		if _, ok := roles.Load(want); !ok {
			t.Errorf("no %s request", want)
		}
	}
	if _, ok := roles.Load("catalog:admin"); ok {
		t.Error("the pack was built with an admin token")
	}

	installed := listInstalledSkills(tc.Workspace.SkillsRoot)
	if len(installed) < 2 {
		t.Fatalf("installed skills: %v", installed)
	}
	indexed := func() map[string][2]string {
		rows, err := db.Query(ctx, `select skill_name, document, coalesce(embedding_model, '') from skill_embedding where org_id = 'org'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string][2]string{}
		for rows.Next() {
			var name, doc, model string
			if err := rows.Scan(&name, &doc, &model); err != nil {
				t.Fatal(err)
			}
			out[name] = [2]string{doc, model}
		}
		return out
	}
	got := indexed()
	if len(got) != len(installed) {
		t.Fatalf("index holds %d skills, want %d: %v", len(got), len(installed), got)
	}
	for _, s := range installed {
		want := s.Name
		if s.Description != "" {
			want += "\n" + s.Description
		}
		if got[s.Name][0] != want {
			t.Fatalf("%s document %q, want %q", s.Name, got[s.Name][0], want)
		}
	}
	changed, kept := installed[0].Name, installed[1].Name
	seed(t, db, `update skill_embedding set document = 'stale', embedding_model = 'm' where org_id = 'org' and skill_name = $1`, changed)
	seed(t, db, `update skill_embedding set embedding_model = 'm' where org_id = 'org' and skill_name = $1`, kept)

	fake.set(revisionQuery, knowledgeReply{Status: 500, Body: "{}"})
	if !strings.Contains(load().Prompt, "salesorderheader") {
		t.Fatal("a warm turn must read the snapshot without GraphJin")
	}
	got = indexed()
	if got[changed][1] != "" || got[changed][0] == "stale" || got[kept][1] != "m" {
		t.Fatalf("a changed skill clears its vector and an unchanged one keeps it: %v", got)
	}

	fake.use(catalog["legacy"], "full")
	seed(t, db, `update data_source set auth_mode = 'none', mcp_url = $1 where org_id = 'org'`, srv.URL+"/mcp")
	tc = load()
	if readKnowledgeSnapshot(tc.Workspace.KnowledgeRoot) != nil {
		t.Fatal("legacy discovery leaves no snapshot")
	}
	if text, _ := os.ReadFile(filepath.Join(tc.Workspace.KnowledgeRoot, "INDEX.md")); string(text) != legacyKnowledgeIndex {
		t.Fatal("legacy discovery writes the legacy index")
	}
	if !strings.Contains(tc.Prompt, "orders") {
		t.Fatal("the prompt lacks the legacy pack")
	}

	seed(t, db, `delete from data_source where org_id = 'org'`)
	tc = load()
	snap = readKnowledgeSnapshot(tc.Workspace.KnowledgeRoot)
	if snap == nil || snap.Source != "none" || snap.Files["tables.json"] != "{}" {
		t.Fatalf("no data source must hide the old pack: %+v", snap)
	}
}
