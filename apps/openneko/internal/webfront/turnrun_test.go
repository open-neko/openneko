package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunStoreHelpersMatchNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/runstore.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Detect []struct {
			Event json.RawMessage `json:"event"`
			Skill *string         `json:"skill"`
		} `json:"detect"`
		SkillFiles map[string]string `json:"skillFiles"`
		BaseHash   string            `json:"baseHash"`
		Overlays   []struct {
			Overlay *string `json:"overlay"`
			Hash    string  `json:"hash"`
		} `json:"overlays"`
		Usage []struct {
			Usage      json.RawMessage `json:"usage"`
			Cap        float64         `json:"cap"`
			CostMicros float64         `json:"costMicros"`
			Priced     string          `json:"priced"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for i, c := range fx.Detect {
		event, err := jsParse(c.Event)
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if c.Skill != nil {
			want = *c.Skill
		}
		if got := detectSkillUse(event); got != want {
			t.Errorf("detect %d: %q, Node %q", i, got, want)
		}
	}
	root := t.TempDir()
	base := filepath.Join(root, "skills", "report")
	for path, text := range fx.SkillFiles {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(base, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, path), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range fx.Overlays {
		dir := filepath.Join(root, "skill-overlays", "report")
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if c.Overlay != nil {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "LEARNED.md"), []byte(strings.ReplaceAll(*c.Overlay, "BASE", fx.BaseHash)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := fingerprintEffectiveSkill(root, "report", base)
		if err != nil || got != c.Hash {
			t.Errorf("overlay %d: %s %v, Node %s", i, got, err, c.Hash)
		}
	}
	for i, c := range fx.Usage {
		usage, err := jsParse(c.Usage)
		if err != nil {
			t.Fatal(err)
		}
		micros, priced := turnUsagePrice(usage, c.Cap)
		if micros != c.CostMicros || priced != c.Priced {
			t.Errorf("usage %d: %v %s, Node %v %s", i, micros, priced, c.CostMicros, c.Priced)
		}
	}
}

func TestAgentSlotDefaultLimit(t *testing.T) {
	for _, c := range []struct {
		concurrency, owner string
		want               int
	}{
		{"", "", 2}, {"4", "", 4}, {"1.5", "", 2}, {"0", "", 2}, {"x", "worker", 3}, {"", "web-worker", 3}, {"6", "worker", 6},
	} {
		t.Setenv("OPENNEKO_AGENT_CONCURRENCY", c.concurrency)
		t.Setenv("OPENNEKO_SANDBOX_OWNER", c.owner)
		if got := defaultAgentSlotLimit(); got != c.want {
			t.Errorf("%q %q: %d, want %d", c.concurrency, c.owner, got, c.want)
		}
	}
}

func TestAgentSlotsQueue(t *testing.T) {
	old := agentSlotHeartbeat
	agentSlotHeartbeat = 10 * time.Millisecond
	t.Cleanup(func() { agentSlotHeartbeat = old })
	s := &agentSlots{limit: 1}
	ctx := context.Background()
	never := func() error { t.Error("the first turn waited"); return nil }
	first, err := s.acquire(ctx, never, func() {})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []string
	waits := map[string]int{}
	beats := map[string]int{}
	wait := func(ctx context.Context, name string) chan func() {
		got := make(chan func(), 1)
		go func() {
			release, err := s.acquire(ctx, func() error {
				mu.Lock()
				waits[name]++
				mu.Unlock()
				return nil
			}, func() {
				mu.Lock()
				beats[name]++
				mu.Unlock()
			})
			if err != nil {
				got <- nil
				return
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			got <- release
		}()
		return got
	}
	queued := func(n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			s.mu.Lock()
			l := len(s.waiting)
			s.mu.Unlock()
			if l == n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d waiting, want %d", l, n)
			}
			time.Sleep(time.Millisecond)
		}
	}
	second := wait(ctx, "second")
	queued(1)
	cctx, cancel := context.WithCancel(ctx)
	gone := wait(cctx, "gone")
	queued(2)
	third := wait(ctx, "third")
	queued(3)
	time.Sleep(50 * time.Millisecond)
	cancel()
	if <-gone != nil {
		t.Fatal("a cancelled turn got a slot")
	}
	queued(2)
	first()
	first()
	release := <-second
	if release == nil {
		t.Fatal("second got no slot")
	}
	select {
	case <-third:
		t.Fatal("third ran beside second")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	if r := <-third; r == nil {
		t.Fatal("third got no slot")
	} else {
		r()
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "second,third" || waits["second"] != 1 || waits["third"] != 1 || waits["gone"] != 1 || beats["second"] == 0 || beats["third"] == 0 {
		t.Fatalf("order %v waits %v beats %v", order, waits, beats)
	}
	if s.active != 0 || len(s.waiting) != 0 {
		t.Fatalf("active %d waiting %d", s.active, len(s.waiting))
	}

	if _, err := s.acquire(cctx, never, func() {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled turn: %v", err)
	}
	failWait := errors.New("status not stored")
	hold, _ := s.acquire(ctx, never, func() {})
	if _, err := s.acquire(ctx, func() error { return failWait }, func() {}); !errors.Is(err, failWait) || len(s.waiting) != 0 {
		t.Fatalf("a failed wait status: %v %d", err, len(s.waiting))
	}
	hold()
}

func TestRunEventStore(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	old := runEventFlushIdle
	t.Cleanup(func() { runEventFlushIdle = old })
	runEventFlushIdle = time.Hour
	stored := func(runID string) []string {
		t.Helper()
		rows, err := db.Query(ctx, `select kind || ' ' || payload::text from work_run_event where run_id = $1 order by id`, runID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	cancelRun := a.runs.register(runAdmin, "org", threadMine, func(error) {})
	var live []string
	var ids []int64
	unsubscribe, ok := a.subscribeRun(runAdmin, func(event any, id int64) {
		live = append(live, jsStringify(event, false))
		ids = append(ids, id)
	})
	if !ok {
		t.Fatal("subscribe")
	}
	if _, ok := a.subscribeRun("b1000000-0000-4000-8000-0000000000ff", func(any, int64) {}); ok {
		t.Fatal("a run not in this process took a subscriber")
	}
	a.subscribeRun(runAdmin, func(any, int64) { panic("a bad subscriber") })
	s := a.newRunEventStore("org", threadMine, runAdmin)
	for _, e := range []any{
		jsObjOf("type", "message", "role", "assistant", "content", "Rev"),
		jsObjOf("type", "message", "role", "assistant", "content", "enue rose."),
		jsObjOf("type", "status", "message", "Working"),
		jsObjOf("type", "message", "role", "assistant", "content", ""),
		jsObjOf("type", "message", "role", "user", "content", "Hi"),
		jsObjOf("type", "message", "role", "assistant", "content", " Next"),
		jsObjOf("type", "message", "role", "system", "content", "sys"),
		jsObjOf("type", "message", "role", "assistant", "content", "Tail"),
	} {
		if err := s.emit(cancelled, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.finalize(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`message {"role": "assistant", "type": "message", "content": "Revenue rose."}`,
		`status {"type": "status", "message": "Working"}`,
		`message {"role": "user", "type": "message", "content": "Hi"}`,
		`message {"role": "assistant", "type": "message", "content": " Next"}`,
		`message {"role": "system", "type": "message", "content": "sys"}`,
		`message {"role": "assistant", "type": "message", "content": "Tail"}`,
	}
	if got := stored(runAdmin); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("stored:\n%s", strings.Join(got, "\n"))
	}
	if len(live) != 6 || live[0] != `{"type":"message","role":"assistant","content":"Revenue rose."}` || live[1] != `{"type":"status","message":"Working"}` {
		t.Fatalf("live %v", live)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("ids out of order %v", ids)
		}
	}
	unsubscribe()
	runEventFlushIdle = 20 * time.Millisecond
	idle := a.newRunEventStore("org", threadMine, runAdmin)
	if err := idle.emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Idle")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(stored(runAdmin)) != 7 {
		if time.Now().After(deadline) {
			t.Fatal("the idle text was not stored")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := idle.finalize(ctx); err != nil || len(stored(runAdmin)) != 7 || len(live) != 6 {
		t.Fatalf("%v %d %d", err, len(stored(runAdmin)), len(live))
	}
	a.runs.unregister(runAdmin, cancelRun)

	bad := a.newRunEventStore("org", threadMine, "not-a-uuid")
	if err := bad.emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "x")); err != nil {
		t.Fatal(err)
	}
	if err := bad.emit(ctx, jsObjOf("type", "status", "message", "y")); err == nil {
		t.Fatal("a store error was not returned")
	}
	if err := bad.finalize(ctx); err == nil || !strings.Contains(err.Error(), "uuid") {
		t.Fatalf("finalize %v", err)
	}
}

func TestRunEventStoreSpendAndSkillRows(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	seed(t, db, `update spend_limit set run_cap_micros = 3000000, org_hourly_micros = 100000000, warn_percent = 50 where org_id = 'org' and workflow_id is null`)
	seed(t, db, `insert into workflow_definition (id, org_id, name) values ('c0000000-0000-4000-8000-000000000001', 'org', 'Stock check')`)
	seed(t, db, `insert into spend_reservation (org_id, work_run_id, workflow_id, source, reserved_micros) values ('org', $1, 'c0000000-0000-4000-8000-000000000001', 'cron', 3000000)`, runAdmin)
	seed(t, db, `insert into spend_limit (org_id, workflow_id, workflow_hourly_micros) values ('org', 'c0000000-0000-4000-8000-000000000001', 1000000)`)
	s := a.newRunEventStore("org", threadMine, runAdmin)
	emit := func(e any) {
		t.Helper()
		if err := s.emit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	emit(jsObjOf("type", "usage", "source", "outer", "provider", "openai", "model", "gpt-5",
		"usage", jsObjOf("totalTokens", 150.2, "costStatus", "estimated", "estimatedCostUsd", 0.6, "costSource", "catalog", "pricingCatalogVersion", "v1")))
	emit(jsObjOf("type", "usage", "source", "inner", "usage", jsObjOf("estimatedCostUsd", 9.0)))
	emit(jsObjOf("type", "usage", "source", "outer", "model", "mystery", "usage", jsObjOf("costStatus", "unknown")))

	root := orgAgentRoot("org")
	skill := filepath.Join(root, "skills", "monthly-report")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: monthly-report\n---\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := git(root, "init", "--initial-branch=main"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(root, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(root, "commit", "-m", "skills"); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(must(git(root, "rev-parse", "HEAD")))
	hash, err := fingerprintSkillTree(skill)
	if err != nil {
		t.Fatal(err)
	}
	read := jsObjOf("type", "tool_start", "id", "t1", "name", "read_file", "input", jsObjOf("path", skill+"/SKILL.md"))
	emit(read)
	emit(jsObjOf("type", "tool_start", "id", "t2", "name", "read_file", "input", jsObjOf("path", skill+"/SKILL.md")))
	emit(jsObjOf("type", "tool_start", "id", "t3", "name", "read_file", "input", jsObjOf("path", "skills/missing/SKILL.md")))
	if err := s.finalize(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(ctx, `select coalesce(provider, '-'), coalesce(model, '-'), cost_micros, coalesce(tokens, -1), priced, coalesce(cost_source, '-'),
		coalesce(pricing_version, '-'), task, source, workflow_id::text, reservation_id is not null from spend_ledger where work_run_id = $1 order by cost_micros`, runAdmin)
	if err != nil {
		t.Fatal(err)
	}
	var ledger []string
	for rows.Next() {
		var provider, model, priced, costSource, version, task, source, workflow string
		var micros, tokens int64
		var reserved bool
		if err := rows.Scan(&provider, &model, &micros, &tokens, &priced, &costSource, &version, &task, &source, &workflow, &reserved); err != nil {
			t.Fatal(err)
		}
		ledger = append(ledger, fmt.Sprintf("%s %s %d %d %s %s %s %s %s %v", provider, model, micros, tokens, priced, costSource, version, task, source, reserved))
	}
	if got := strings.Join(ledger, "|"); got != "openai gpt-5 600000 151 estimated catalog v1 turn cron true|- mystery 3000000 -1 fallback - - turn cron true" {
		t.Fatalf("ledger %s", got)
	}
	alerts := rowText(t, db, `select string_agg(kind || ' ' || subject || ' ' || (details->>'message'), '|' order by kind, subject) from behavior_alert where org_id = 'org'`)
	for _, want := range []string{
		"spend.price_unknown model:unknown/mystery OpenNeko has no price for unknown/mystery. Each turn on it is charged the $3.00 per-run cap until a price is added.",
		"spend.budget_warning workflow:c0000000-0000-4000-8000-000000000001 Spend is at ",
		"hourly budget for Stock check",
	} {
		if !strings.Contains(alerts, want) {
			t.Errorf("alerts %s\nwant %s", alerts, want)
		}
	}
	usage := rowText(t, db, `select skill_name || ' ' || content_hash || ' ' || origin || ' ' || coalesce(config_commit_sha, '-') || ' ' || source || ' ' || attempt::text ||
		' ' || (first_event_id = (select min(id) from work_run_event where run_id = $1 and kind = 'tool_start'))::text from skill_usage where run_id = $1`, runAdmin)
	if usage != "monthly-report "+hash+" custom "+head+" read 1 true" {
		t.Fatalf("skill usage %q", usage)
	}
	kinds := rowText(t, db, `select string_agg(kind, ',' order by id) from work_run_event where run_id = $1`, runAdmin)
	if kinds != "usage,usage,usage,tool_start,skill_used,tool_start,tool_start" {
		t.Fatalf("kinds %s", kinds)
	}
	used := rowText(t, db, `select payload::text from work_run_event where run_id = $1 and kind = 'skill_used'`, runAdmin)
	if !strings.Contains(used, `"origin": "custom"`) || !strings.Contains(used, `"configCommitSha": "`+head+`"`) || strings.Contains(used, "packId") {
		t.Fatalf("skill_used %s", used)
	}

	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash) values ('c1000000-0000-4000-8000-000000000001', 'org', 'reports', '1.2.0', 'installed', 'h')`)
	seed(t, db, `insert into pack_artifact (org_id, pack_install_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash) values ('org', 'c1000000-0000-4000-8000-000000000001', 'skill', 'monthly-report', 'monthly-report', 'h', 'h')`)
	var other string
	if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, actor_user_id, actor_role) values ('org', $1, 'ax', 'admin', 'admin') returning id::text`, threadMine).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := a.appendRunEvent(ctx, "org", threadMine, other, read); err != nil {
		t.Fatal(err)
	}
	if got := rowText(t, db, `select origin || ' ' || pack_id || ' ' || pack_version from skill_usage where run_id = $1`, other); got != "pack reports 1.2.0" {
		t.Fatalf("pack skill %q", got)
	}
	if got := rowText(t, db, `select concat_ws(' ', payload->>'origin', payload->>'packId', payload->>'packVersion', payload->>'configCommitSha')
		from work_run_event where run_id = $1 and kind = 'skill_used'`, other); got != "pack reports 1.2.0 "+head {
		t.Fatalf("pack skill_used %s", got)
	}
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}

func TestRunSpendGuard(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	log := &eventLog{}
	if runCtx, emit, err := a.newRunSpendGuard(ctx, runAdmin, log.emit); err != nil || runCtx != ctx {
		t.Fatalf("no reservation: %v", err)
	} else if err := emit(ctx, jsObjOf("type", "usage", "source", "outer", "usage", jsObjOf("costStatus", "unknown"))); err != nil || log.count("error") != 0 {
		t.Fatalf("no reservation: %v %v", err, log.all())
	}
	seed(t, db, `insert into spend_reservation (org_id, work_run_id, source, reserved_micros) values ('org', $1, 'chat', 1000000)`, runAdmin)
	seed(t, db, `update work_run set status = 'running' where id = $1`, runAdmin)
	log = &eventLog{}
	runCtx, emit, err := a.newRunSpendGuard(ctx, runAdmin, log.emit)
	if err != nil {
		t.Fatal(err)
	}
	usage := func(usd float64) any {
		return jsObjOf("type", "usage", "source", "outer", "usage", jsObjOf("costStatus", "estimated", "estimatedCostUsd", usd))
	}
	snapshot := func(usage any) any {
		return jsObjOf("type", "tool_start", "id", "t", "name", "x", "usageSnapshot", usage)
	}
	for _, e := range []any{
		usage(0.5),
		snapshot(jsObjOf("costStatus", "estimated", "estimatedCostUsd", 0.5)),
		snapshot(jsObjOf("costStatus", "unknown", "estimatedCostUsd", 5.0)),
		jsObjOf("type", "usage", "source", "inner", "usage", jsObjOf("estimatedCostUsd", 5.0)),
	} {
		if err := emit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if runCtx.Err() != nil || log.count(`"type":"error"`) != 0 {
		t.Fatalf("stopped under the cap: %v", log.all())
	}
	if err := emit(ctx, snapshot(jsObjOf("costStatus", "billed", "billedCostUsd", 0.6))); err != nil {
		t.Fatal(err)
	}
	msg := "The run exceeded its $1.00 spend cap ($1.10 spent)."
	stop := spendStop(runCtx)
	if stop == nil || stop.Error() != msg || rowText(t, db, `select status || ':' || error from work_run where id = $1`, runAdmin) != "failed:"+msg {
		t.Fatalf("stop %v", stop)
	}
	if err := emit(ctx, usage(3)); err != nil {
		t.Fatal(err)
	}
	all := log.all()
	if log.count(`{"type":"error","message":"`+msg+`"}`) != 1 || len(all) != 7 || !strings.Contains(all[5], `"type":"error"`) {
		t.Fatalf("events %v", all)
	}
}

func TestStartChatRun(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	old := runEventFlushIdle
	t.Cleanup(func() { runEventFlushIdle = old })
	runEventFlushIdle = time.Hour
	a.slots.limit = 1
	newRun := func() string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, actor_user_id, actor_role) values ('org', $1, 'ax', 'admin', 'admin') returning id::text`,
			threadMine).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	options := func(runID string, core fakeCore) chatTurnOptions {
		return chatTurnOptions{OrgID: "org", ThreadID: threadMine, RunID: runID, Message: "Question", Channel: "web", IncludeUxMetadata: true,
			Summarize: noSummary(t), resolve: fixedBackend("http://unused"), runCore: core, keepBot: func(context.Context, botKeepInput) error { return nil }}
	}
	status := func(runID string) string {
		return rowText(t, db, `select status || ':' || coalesce(error, '-') from work_run where id = $1`, runID)
	}
	kinds := func(runID string) string {
		return rowText(t, db, `select string_agg(kind, ',' order by id) from work_run_event where run_id = $1`, runID)
	}

	holding, release := make(chan struct{}), make(chan struct{})
	first := newRun()
	firstDone, err := a.startChatRun(ctx, options(first, func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Stock "))
		close(holding)
		<-release
		_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "is fine."))
		_ = in.Emit(ctx, jsObjOf("type", "usage", "source", "outer", "usage",
			jsObjOf("totalTokens", 25.0, "estimatedCostUsd", 0.01, "costStatus", "estimated", "coverage", "complete")))
		return axRunResult{Status: "completed", FinalText: "Stock is fine."}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	<-holding
	var mu sync.Mutex
	var seen []string
	if _, ok := a.subscribeRun(first, func(event any, _ int64) {
		mu.Lock()
		seen = append(seen, fmt.Sprint(jsGet(event, "type")))
		mu.Unlock()
	}); !ok {
		t.Fatal("the running turn is not registered")
	}

	second := newRun()
	waiting := make(chan struct{})
	secondDone, err := a.startChatRun(ctx, options(second, func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		t.Error("the aborted turn ran its core")
		return axRunResult{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for !strings.Contains(kinds(second), "status,status,status,status") {
			time.Sleep(5 * time.Millisecond)
		}
		close(waiting)
	}()
	select {
	case <-waiting:
	case <-time.After(10 * time.Second):
		t.Fatalf("no wait status: %s", kinds(second))
	}
	if !a.abortRun(second) {
		t.Fatal("abort")
	}
	<-secondDone
	if status(second) != "cancelled:-" || a.runs.get(second) != nil {
		t.Fatalf("aborted run %s", status(second))
	}
	if got := rowText(t, db, `select payload->>'message' from work_run_event where run_id = $1 and kind = 'status' order by id desc limit 1`, second); got != "Waiting for another question to finish…" {
		t.Fatalf("wait status %q", got)
	}

	close(release)
	<-firstDone
	if status(first) != "completed:-" || a.runs.get(first) != nil || a.abortRun(first) {
		t.Fatalf("first %s", status(first))
	}
	if got := rowText(t, db, `select string_agg(payload->>'content', '|' order by id) from work_run_event where run_id = $1 and kind = 'message'`, first); got != "Stock is fine." {
		t.Fatalf("coalesced text %q", got)
	}
	mu.Lock()
	if strings.Join(seen, ",") != "message,usage,done,telemetry" {
		t.Errorf("live %v", seen)
	}
	mu.Unlock()
	if got := rowText(t, db, `select payload->'summary'->>'runId' || ':' || (payload->'summary'->'usage')::text from work_run_event where run_id = $1 and kind = 'telemetry'`, first); got != first+`:{"coverage": "complete", "costStatus": "estimated", "totalTokens": 25, "missingReasons": [], "estimatedCostUsd": 0.01}` {
		t.Errorf("telemetry %s", got)
	}

	third := newRun()
	started := make(chan struct{})
	thirdDone, err := a.startChatRun(ctx, options(third, func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		close(started)
		<-ctx.Done()
		return axRunResult{Status: "cancelled"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if n := a.StopTurns(stopCtx); n != 1 {
		t.Fatalf("stopped %d", n)
	}
	select {
	case <-thirdDone:
	default:
		t.Fatal("StopTurns returned before the turn ended")
	}
	if status(third) != "failed:"+errTurnShutdown.Error() || !strings.HasSuffix(kinds(third), "error,done,telemetry") {
		t.Fatalf("shutdown %s %s", status(third), kinds(third))
	}

	fourth := newRun()
	fourthDone, err := a.startChatRun(ctx, options(fourth, func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		return axRunResult{}, errors.New("core broke")
	}))
	if err != nil {
		t.Fatal(err)
	}
	<-fourthDone
	if status(fourth) != "failed:core broke" || !strings.HasSuffix(kinds(fourth), "error,done,telemetry") ||
		rowText(t, db, `select payload->'summary'->'usage'->>'coverage' from work_run_event where run_id = $1 and kind = 'telemetry'`, fourth) != "unavailable" {
		t.Fatalf("thrown %s %s", status(fourth), kinds(fourth))
	}

	seed(t, db, `update work_run set status = 'running' where id = $1`, fourth)
	s := a.newRunEventStore("org", threadMine, fourth)
	a.failThrownRun(ctx, options(fourth, nil), s, errors.New("late"))
	if err := s.finalize(ctx); err != nil {
		t.Fatal(err)
	}
	if status(fourth) != "failed:late" || !strings.HasSuffix(kinds(fourth), "error,done,telemetry,error,done") {
		t.Fatalf("cleanup %s %s", status(fourth), kinds(fourth))
	}
}
