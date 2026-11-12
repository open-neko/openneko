package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/embedding"
)

const runAdmin = "b1000000-0000-4000-8000-000000000002"

func noSummary(t *testing.T) func(context.Context, string, string, string) (string, error) {
	return func(context.Context, string, string, string) (string, error) {
		t.Fatal("the transcript is short; nothing folds")
		return "", nil
	}
}

func TestTurnContextMember(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	seed(t, db, `insert into work_message (org_id, thread_id, run_id, role, content, created_at) values
		('org', $1, $2, 'user', 'Revenue by month?', '2026-11-01T08:00:00Z'),
		('org', $1, $2, 'assistant', 'March was the best month.', '2026-11-01T08:01:00Z')`, threadMine, runMine)
	seed(t, db, `insert into bot (org_id, owner_user_id, name, coat, pattern, thread_id, instructions)
		values ('org', 'member', 'Stock watcher', 'ink', 'solid', $1, 'Watch stock daily.')`, threadMine)
	seed(t, db, `insert into operator_profile (org_id, user_id, role_template) values ('org', 'member', 'CFO')`)

	tc, err := a.loadTurnContext(ctx, turnOptions{OrgID: "org", ThreadID: threadMine, RunID: runMine,
		Message: "Are refunds in revenue?", IncludeUxMetadata: true, Summarize: noSummary(t)})
	if err != nil {
		t.Fatal(err)
	}
	p := tc.Prompt
	for _, want := range []string{
		"[" + memTeam + "] business_rule (global): Revenue is net of refunds",
		"[" + memMine + "] preference (global): Show amounts in INR",
		"[" + memThread + "] thread_note (thread:" + threadMine + "): This thread is about Q3",
		"1. User: Revenue by month?\n\n2. Assistant: March was the best month.",
		"<current_user_message>\nAre refunds in revenue?\n</current_user_message>",
		`In this chat you are "Stock watcher"`,
		"Their role: CFO.",
		"<closing>",
		"Uploads for this thread: " + tc.Workspace.ThreadUploadsRoot,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, absent := range []string{"Use weeks", "AOV excludes tax", "<source_config>"} {
		if strings.Contains(p, absent) {
			t.Errorf("prompt holds %q", absent)
		}
	}
	if tc.Actor != (runActorRow{UserID: "member", Role: "member"}) || tc.Compaction != nil {
		t.Fatalf("actor %+v, compaction %+v", tc.Actor, tc.Compaction)
	}
	if tc.AllowedSkills == nil || slices.Contains(tc.AllowedSkills, "graphjin-config") {
		t.Fatalf("a member run must name its skills, without graphjin-config: %v", tc.AllowedSkills)
	}
	for _, dir := range []string{tc.Workspace.ArtifactRoot, tc.Workspace.BinRoot, tc.Workspace.ThreadUploadsRoot} {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Fatalf("workspace dir %s: %v", dir, err)
		}
	}
	var hits, uses int
	if err := db.QueryRow(ctx, `select count(*) from work_memory_event where memory_id = $1 and action = 'search_hit' and run_id = $2`,
		memTeam, runMine).Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select use_count from work_memory where id = $1`, memTeam).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if hits != 1 || uses != 1 {
		t.Fatalf("the keyword match must count one use: %d events, use_count %d", hits, uses)
	}

	tc, err = a.loadTurnContext(ctx, turnOptions{OrgID: "org", ThreadID: threadMine, RunID: runMine,
		Message: "hello", Channel: "slack", Summarize: noSummary(t)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tc.Prompt, "<bot>") || strings.Contains(tc.Prompt, "<cards>") || strings.Contains(tc.Prompt, "<closing>") {
		t.Fatal("a slack turn has no bot, cards or closing section")
	}
}

func TestTurnContextAdminAndService(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values ($1, 'org', $2, 'test', 'admin', 'admin')`,
		runAdmin, threadMine)
	tc, err := a.loadTurnContext(ctx, turnOptions{OrgID: "org", ThreadID: threadMine, RunID: runAdmin, Message: "x", Summarize: noSummary(t)})
	if err != nil {
		t.Fatal(err)
	}
	if tc.AllowedSkills != nil || !tc.HeldActions.All || !strings.Contains(tc.Prompt, "<source_config>") {
		t.Fatalf("an admin run holds every skill and gets the source config tools: %v", tc.AllowedSkills)
	}
	if !strings.Contains(tc.Prompt, "AOV excludes tax") || strings.Contains(tc.Prompt, "Show amounts in INR") {
		t.Fatal("an admin run reads the team layer only")
	}
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, enabled, config) values ('org', 'graphjin-config', 'graphjin', true, '{"sourceConfigEnabled":false}')`)
	if tc, err = a.loadTurnContext(ctx, turnOptions{OrgID: "org", ThreadID: threadMine, RunID: runAdmin, Message: "x", Summarize: noSummary(t)}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tc.Prompt, "<source_config>") {
		t.Fatal("the org setting turns the source config tools off")
	}

	const unlinked = "b1000000-0000-4000-8000-000000000003"
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_role) values ($1, 'org', $2, 'test', 'member')`, unlinked, threadMine)
	if tc, err = a.loadTurnContext(ctx, turnOptions{OrgID: "org", ThreadID: threadMine, RunID: unlinked, Message: "x", Summarize: noSummary(t)}); err != nil {
		t.Fatal(err)
	}
	if len(tc.AllowedSkills) != 0 || tc.AllowedSkills == nil || tc.HeldActions.All || len(tc.HeldActions.IDs) != 0 || strings.Contains(tc.Prompt, "Revenue is net of refunds") ||
		!strings.Contains(tc.Prompt, "This thread is about Q3") {
		t.Fatalf("an unlinked sender holds nothing but the thread notes: %v", tc.AllowedSkills)
	}

	if _, err := a.loadTurnContext(ctx, turnOptions{OrgID: "org", ThreadID: unknownUUID, RunID: runAdmin}); !errors.Is(err, errThreadGone) {
		t.Fatalf("missing thread: %v", err)
	}
}

func TestTurnContextDropsTamperedMemory(t *testing.T) {
	f, db := memoryFront(t)
	seed(t, db, `update work_memory set integrity_hmac = 'forged' where id = $1`, memMine)
	tc, err := f.API.loadTurnContext(context.Background(), turnOptions{OrgID: "org", ThreadID: threadMine, RunID: runMine,
		Message: "amounts", Summarize: noSummary(t)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tc.Prompt, "Show amounts in INR") || !strings.Contains(tc.Prompt, "Revenue is net of refunds") {
		t.Fatal("a row whose hash does not match must stay out of the prompt")
	}
}

func TestCompactTranscript(t *testing.T) {
	var msgs []transcriptMessage
	for i := range 45 {
		msgs = append(msgs, transcriptMessage{ID: fmt.Sprint(i), Role: pick(i%2 == 0, "user", "assistant"), Content: fmt.Sprint("m", i)})
	}
	var gotPrior, gotTurns string
	summarize := func(_ context.Context, _ string, prior, turns string) (string, error) {
		gotPrior, gotTurns = prior, turns
		return "folded", nil
	}
	summary, kept, c, err := compactTranscript(context.Background(), "org", msgs, &threadCompaction{Summary: "old", ThroughMessageID: "gone"}, "now", summarize)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "folded" || len(kept) != 12 || kept[0].ID != "33" || gotPrior != "" ||
		!strings.HasPrefix(gotTurns, "Operator: m0\n\nAgent: m1") || !strings.HasSuffix(gotTurns, "Operator: m32") {
		t.Fatalf("fold: %q %d %q", summary, len(kept), gotTurns)
	}
	if *c != (threadCompaction{Summary: "folded", ThroughMessageID: "32", UpdatedAt: "now", Version: 1}) {
		t.Fatalf("compaction: %+v", c)
	}
	summary, kept, c, err = compactTranscript(context.Background(), "org", msgs, c, "later", noSummary(t))
	if err != nil || summary != "folded" || len(kept) != 12 || c != nil {
		t.Fatalf("a known watermark keeps the summary: %q %d %v %v", summary, len(kept), c, err)
	}
}

func TestReadKnowledgePack(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if k := readKnowledgePack(root); k != (knowledgePack{Mode: "legacy", Tables: "{}\n", Namespaces: "{}\n", Insights: "{}\n", Syntax: "{}\n"}) {
		t.Fatalf("empty root: %+v", k)
	}
	write("tables.json", `{"tables":[]}`)
	write("mode.json", `{"mode":"agentic"}`)
	if k := readKnowledgePack(root); k.Mode != "agentic" || k.Tables != `{"tables":[]}` {
		t.Fatalf("loose files: %+v", k)
	}
	files := map[string]string{"tables.json": `{"tables":[{"name":"orders"}]}`, "namespaces.json": "{}", "insights.json": "{}",
		"syntax.json": "{}", "INDEX.md": "", "mode.json": `{"mode":"legacy"}`}
	snap, _ := json.Marshal(map[string]any{"format": 1, "source": "s", "revision": "r", "files": files})
	write(".knowledge-snapshot.json", string(snap))
	if k := readKnowledgePack(root); k.Mode != "legacy" || k.Tables != files["tables.json"] {
		t.Fatalf("snapshot: %+v", k)
	}
	files["syntax.json"] = "{broken"
	snap, _ = json.Marshal(map[string]any{"format": 1, "source": "s", "revision": "r", "files": files})
	write(".knowledge-snapshot.json", string(snap))
	if k := readKnowledgePack(root); k.Mode != "agentic" || k.Tables != `{"tables":[]}` {
		t.Fatalf("a broken snapshot falls back to the loose files: %+v", k)
	}
}

func TestListInstalledSkills(t *testing.T) {
	root := t.TempDir()
	for name, md := range map[string]string{
		"b-skill":   "---\nname: b\ndescription:   Reads PDFs \r\n---\nbody",
		"a-skill":   "---\nname: a\n---\n",
		"no-header": "# no frontmatter",
	} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "SKILL.md"), []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := listInstalledSkills(root)
	want := []installedSkill{{"a-skill", ""}, {"b-skill", "Reads PDFs"}}
	if !slices.Equal(got, want) {
		t.Fatalf("skills: %+v", got)
	}
}

func TestTurnMemorySemanticSearch(t *testing.T) {
	f, db := memoryFront(t)
	ctx := context.Background()
	t.Setenv("NEKO_EMBEDDING_URL", "https://embed.example.test/v1")
	t.Setenv("NEKO_EMBEDDING_MODEL", "embeddinggemma")
	t.Setenv("NEKO_EMBEDDING_DIMENSIONS", "4")
	var asked []string
	var fail error
	f.API.Embedder = func(_ context.Context, r embedding.Request, _ string) (embedding.Result, error) {
		asked = append(asked, r.Input...)
		return embedding.Result{Vectors: [][]float64{{1, 0, 0, 0}}}, fail
	}
	seed(t, db, `update work_memory set embedding = '[1,0,0,0]', embedding_model = 'embeddinggemma@4' where id = $1`, memTeam)
	hits := func() map[string]int {
		rows, err := db.Query(ctx, `select memory_id::text, count(*) from work_memory_event where action = 'search_hit' group by 1`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				t.Fatal(err)
			}
			out[id] = n
		}
		return out
	}
	load := func() {
		if _, err := f.API.loadTurnContext(ctx, turnOptions{OrgID: "org", ThreadID: threadMine, RunID: runMine,
			Message: "  INR amounts ", Summarize: noSummary(t)}); err != nil {
			t.Fatal(err)
		}
	}
	load()
	if got := hits(); got[memTeam] != 1 || got[memMine] != 1 || len(got) != 2 || !slices.Equal(asked, []string{"INR amounts"}) {
		t.Fatalf("semantic and unindexed keyword hits: %v, asked %q", got, asked)
	}
	fail = errors.New("embedding down")
	load()
	if got := hits(); got[memTeam] != 1 || got[memMine] != 2 {
		t.Fatalf("with embedding down the search falls back to keywords: %v", got)
	}
}

func TestCompactorProgramMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/llm/src/work/compact-transcript.ts")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, "`"+compactorProgram.Signature+"`") || !strings.Contains(src, "`"+compactorProgram.Instruction+"`") {
		t.Fatal("the compactor program differs from packages/llm/src/work/compact-transcript.ts")
	}
}
