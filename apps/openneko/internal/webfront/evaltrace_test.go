package webfront

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readTraceLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		out = append(out, line)
	}
	return out
}

func traceOperations(lines []map[string]any) string {
	var ops []string
	for _, l := range lines {
		ev, _ := l["event"].(map[string]any)
		ops = append(ops, ev["operation"].(string)+"/"+ev["source"].(string))
	}
	return strings.Join(ops, ",")
}

func TestOpenEvalTrace(t *testing.T) {
	if tr, err := OpenEvalTrace("  ", nil, nil); tr != nil || err != nil {
		t.Fatalf("empty path: %v %v", tr, err)
	}
	var none *EvalTrace
	unregister := none.register("run-off")
	if traceActive("run-off") {
		t.Fatal("a nil trace registered a sink")
	}
	unregister()
	if err := none.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	if err := os.MkdirAll(filepath.Join(runs, "org"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(runs, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(runs, "trace.jsonl"), filepath.Join(runs, "org", "new", "trace.jsonl"), filepath.Join(link, "trace.jsonl"), runs} {
		if _, err := OpenEvalTrace(p, []string{"", runs}, nil); err == nil || !strings.Contains(err.Error(), EvalTraceEnv) {
			t.Errorf("%s: %v", p, err)
		}
	}

	path := filepath.Join(dir, "runs-beside", "trace.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := OpenEvalTrace(path, []string{runs}, []string{
		"OPENAI_API_KEY=sk-planted-key-0001", "NEKO_DATABASE_URL=postgres://neko:planted-db-pass@db/neko",
		"SHORT_TOKEN=abc", "PLAIN=planted-plain-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info.Mode(), err)
	}

	b := traceBinding{RunID: "run-trace", OrgID: "org", Kind: "work"}
	unregister = tr.register(b.RunID)
	_, _ = traced(traceSpec{binding: b, operation: "graphjin.execute", source: traceBroker,
		request:  jsObjOf("query", "{ orders(where: {note: \"sk-planted-key-0001\"}) { id } }"),
		evidence: func(any) *jsObj { return jsObjOf("toolName", "execute_graphql") },
	}, func() (any, error) {
		return jsObjOf("data", jsObjOf("orders", []any{jsObjOf("id", float64(1), "note", "planted-db-pass and planted-plain-value")})), nil
	})
	_, _ = traced(traceSpec{binding: b, operation: "graphjin.execute", source: traceBroker, request: jsObjOf("query", "{ x }"),
		evidence: func(any) *jsObj { return &jsObj{} }}, func() (any, error) { return nil, errors.New("failed with sk-planted-key-0001") })
	traceMemoryPrefetched(b, []memoryOut{{ID: "m1", Text: "Revenue is net of refunds", Scope: "global", Kind: "business_rule"}})
	traceMemoryPrefetched(b, nil)
	unregister()
	_, _ = traced(traceSpec{binding: b, operation: "graphjin.execute", source: traceBroker, request: "late",
		evidence: func(any) *jsObj { return &jsObj{} }}, func() (any, error) { return nil, nil })

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, planted := range []string{"sk-planted-key-0001", "planted-db-pass"} {
		if strings.Contains(string(raw), planted) {
			t.Errorf("the trace holds %s", planted)
		}
	}
	if !strings.Contains(string(raw), "planted-plain-value") {
		t.Error("the scrubber removed a value that is not a secret")
	}
	lines := readTraceLines(t, path)
	if got := traceOperations(lines); got != "graphjin.execute/trusted-broker,graphjin.execute/trusted-broker,memory.prefetched/trusted-host" {
		t.Fatalf("operations %s", got)
	}
	first := lines[0]["content"].(map[string]any)
	if first["rowCount"] != float64(1) || first["truncated"] != false || !strings.Contains(first["request"].(map[string]any)["query"].(string), "[REDACTED]") {
		t.Errorf("content %v", first)
	}
	if lines[1]["content"].(map[string]any)["error"] != "failed with [REDACTED]" {
		t.Errorf("error content %v", lines[1]["content"])
	}
	prefetched := lines[2]
	if _, ok := prefetched["content"]; ok {
		t.Error("a host event carries step content")
	}
	ev := prefetched["event"].(map[string]any)
	evidence := ev["evidence"].(map[string]any)
	m := evidence["memories"].([]any)[0].(map[string]any)
	if ev["sequence"] != float64(3) || ev["status"] != "ok" || ev["runKind"] != "work" || evidence["returnedCount"] != float64(1) ||
		m["id"] != "m1" || m["contentDigest"] != traceDigest("Revenue is net of refunds") || m["layer"] != "team" ||
		ev["requestDigest"] != traceDigest(jsObjOf("ids", []any{"m1"})) || ev["responseDigest"] != traceDigest(jsObjFromMap(evidence)) {
		t.Errorf("memory.prefetched %v", ev)
	}
}

func jsObjFromMap(m map[string]any) any {
	raw, _ := json.Marshal(m)
	v, _ := jsParse(raw)
	return v
}

func TestSkillTrace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r, err := orgWorkspace("org")
	if err != nil {
		t.Fatal(err)
	}
	body := "---\nname: sales-pulse\n---\nRead the sales.\n"
	if err := os.MkdirAll(filepath.Join(r.Skills, "sales-pulse"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Skills, "sales-pulse", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var got []*jsObj
	b := traceBinding{RunID: "run-skill", OrgID: "org", Kind: "workflow"}
	var s skillTrace
	start := func(id, path string) any {
		return jsObjOf("type", "tool_start", "id", id, "name", "skill_view", "input", jsObjOf("path", path))
	}
	s.observe(b, start("before", "/x/skills/sales-pulse/SKILL.md"))
	s.observe(b, jsObjOf("type", "tool_end", "id", "before"))
	if s.pending != nil {
		t.Fatal("a run with no sink keeps skill state")
	}
	unregister := registerTraceSink(b.RunID, func(event, _ *jsObj) { got = append(got, event) }, nil)
	defer unregister()
	path := filepath.Join(r.Skills, "sales-pulse", "SKILL.md")
	s.observe(b, start("a", path))
	s.observe(b, jsObjOf("type", "tool_end", "id", "a", "error", "denied"))
	s.observe(b, start("b", "/x/skills/missing-skill/SKILL.md"))
	s.observe(b, jsObjOf("type", "tool_end", "id", "b"))
	s.observe(b, start("c", path))
	s.observe(b, jsObjOf("type", "tool_end", "id", "c"))
	s.observe(b, start("d", path))
	s.observe(b, jsObjOf("type", "tool_end", "id", "d"))
	s.observe(b, jsObjOf("type", "tool_end", "id", "unknown"))
	if len(got) != 1 {
		t.Fatalf("events %d", len(got))
	}
	ev := got[0]
	want := `{"id":"sales-pulse","contentDigest":"sha256:` + sha256Hex(body) + `"}`
	if jsStringify(ev.vals["evidence"], false) != want || ev.vals["operation"] != "skill.loaded" || ev.vals["source"] != "trusted-host" ||
		ev.vals["runKind"] != "workflow" || ev.vals["requestDigest"] != traceDigest(jsObjOf("id", "sales-pulse")) {
		t.Errorf("skill.loaded %s", jsStringify(ev, false))
	}
}

func TestEvalTraceRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", filepath.Join(home, "stages"))
	got := EvalTraceRefused(filepath.Join(home, "files"))
	want := []string{filepath.Join(home, ".config", "openneko", "agents"), filepath.Join(home, "stages"), filepath.Join(home, "files")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%v", got)
	}
	if _, err := OpenEvalTrace(filepath.Join(home, ".config", "openneko", "agents", "orgs", "org", "uploads", "t.jsonl"), got, nil); err == nil {
		t.Fatal("a trace inside the uploads folder")
	}
}

func TestEvalTraceChatRun(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	t.Setenv("NEKO_PLANTED_SECRET", "planted-run-secret")
	r, err := orgWorkspace("org")
	if err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(r.Skills, "stock-check", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("Check stock.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newRun := func() string {
		var id string
		if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, actor_user_id, actor_role) values ('org', $1, 'ax', 'admin', 'admin') returning id::text`,
			threadMine).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	run := func(runID string) {
		t.Helper()
		done, err := a.startChatRun(ctx, chatTurnOptions{OrgID: "org", ThreadID: threadMine, RunID: runID, Message: "Revenue", Channel: "web",
			Summarize: noSummary(t), resolve: fixedBackend("http://unused"), keepBot: func(context.Context, botKeepInput) error { return nil },
			runCore: func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
				if a.EvalTrace == nil && traceActive(runID) {
					t.Error("a run with the trace off has a sink")
				}
				_, _ = traced(traceSpec{binding: traceBinding{RunID: runID, OrgID: "org", Kind: "work"}, operation: "graphjin.execute", source: traceBroker,
					request:  jsObjOf("query", "{ stock }"),
					evidence: func(any) *jsObj { return &jsObj{} },
				}, func() (any, error) {
					return jsObjOf("data", jsObjOf("stock", []any{jsObjOf("note", "planted-run-secret")})), nil
				})
				for _, id := range []string{"s1", "s2"} {
					_ = in.Emit(ctx, jsObjOf("type", "tool_start", "id", id, "name", "skill_view", "input", jsObjOf("path", skill)))
					_ = in.Emit(ctx, jsObjOf("type", "tool_end", "id", id))
				}
				return axRunResult{Status: "completed", FinalText: "Fine."}, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the run did not end")
		}
	}

	off := newRun()
	run(off)
	if traceActive(off) {
		t.Fatal("the run left a sink")
	}

	path := filepath.Join(t.TempDir(), "trace.jsonl")
	tr, err := OpenEvalTrace(path, EvalTraceRefused(""), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	a.EvalTrace = tr
	on := newRun()
	run(on)
	if traceActive(on) {
		t.Fatal("the run left its file sink")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "planted-run-secret") || strings.Contains(string(raw), off) {
		t.Fatalf("trace %s", raw)
	}
	lines := readTraceLines(t, path)
	if got := traceOperations(lines); got != "memory.prefetched/trusted-host,graphjin.execute/trusted-broker,skill.loaded/trusted-host" {
		t.Fatalf("operations %s", got)
	}
	for i, l := range lines {
		ev := l["event"].(map[string]any)
		if ev["runId"] != on || ev["sequence"] != float64(i+1) {
			t.Errorf("line %d: %v", i, ev)
		}
	}
	ids := map[string]bool{}
	for _, m := range lines[0]["event"].(map[string]any)["evidence"].(map[string]any)["memories"].([]any) {
		ids[m.(map[string]any)["id"].(string)] = true
	}
	if !ids[memTeam] || ids[memOther] {
		t.Errorf("prefetched %v", ids)
	}
}
