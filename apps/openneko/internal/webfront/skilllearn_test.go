package webfront

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func learnUsages(n int, at time.Time) []skillUsageSnapshot {
	out := make([]skillUsageSnapshot, n)
	for i := range out {
		out[i] = skillUsageSnapshot{RunID: "run-" + strconv.Itoa(i+1), ContentHash: "same-hash", CreatedAt: at}
	}
	return out
}

func learnSkill(t *testing.T, name, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func proposing(p *skillLearnProposal) func(string, []skillUsageSnapshot) (*skillLearnProposal, error) {
	return func(string, []skillUsageSnapshot) (*skillLearnProposal, error) { return p, nil }
}

func TestRunSkillLearnDecisions(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	old := now.Add(-3 * time.Hour)
	good := &skillLearnProposal{Lesson: "parent items", Rationale: "child rows double-count",
		LearnedMarkdown: "  Prefer parent items when product structures would double-count.  ", EvidenceRunIDs: []string{"run-1"}}
	base := func(root string, mut func(*skillLearnInput)) skillLearnInput {
		in := skillLearnInput{OrgRoot: root, SkillName: "refunds", OrgEnabled: true, SkillOn: true, Usages: learnUsages(5, old), Now: now, Propose: proposing(good)}
		if mut != nil {
			mut(&in)
		}
		return in
	}
	root := learnSkill(t, "refunds", "# Refunds\n")
	for name, c := range map[string]struct {
		in                       skillLearnInput
		decision, reason, detail string
	}{
		"org off":   {base(root, func(in *skillLearnInput) { in.OrgEnabled = false }), "skipped", "flags_off", ""},
		"skill off": {base(root, func(in *skillLearnInput) { in.SkillOn = false }), "skipped", "flags_off", ""},
		"four uses": {base(root, func(in *skillLearnInput) { in.Usages = learnUsages(4, old) }), "skipped", "below_repeat_threshold", "settled 4 < 5"},
		"unsettled": {base(root, func(in *skillLearnInput) {
			in.Usages = append(learnUsages(4, old), skillUsageSnapshot{RunID: "run-new", ContentHash: "same-hash", CreatedAt: now.Add(-time.Hour)})
		}), "skipped", "below_repeat_threshold", "settled 4 < 5"},
		"split cohort": {base(root, func(in *skillLearnInput) {
			in.Usages = append(learnUsages(3, old), learnUsages(3, old)...)
			for i := range 3 {
				in.Usages[i].ContentHash = "other"
			}
		}), "skipped", "below_repeat_threshold", "no content_hash cohort reached the repeat threshold"},
		"missing skill": {base(t.TempDir(), nil), "skipped", "missing_skill", "skill directory is missing"},
		"no proposal":   {base(root, func(in *skillLearnInput) { in.Propose = proposing(nil) }), "skipped", "no_proposal", "proposer returned no lesson"},
		"secret": {base(root, func(in *skillLearnInput) {
			in.Propose = proposing(&skillLearnProposal{Lesson: "leak", Rationale: "bad", LearnedMarkdown: "Store GEMINI api_key=sk-abc1234567890xyz in the skill."})
		}), "rejected", "candidate looks like a secret", ""},
		"frontmatter": {base(root, func(in *skillLearnInput) {
			in.Propose = proposing(&skillLearnProposal{Lesson: "hijack", Rationale: "bad", LearnedMarkdown: "---\nname: evil\n---\n"})
		}), "rejected", "candidate tries to set skill frontmatter", ""},
	} {
		res, err := runSkillLearn(c.in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Decision != c.decision || res.Reason != c.reason || (c.detail != "" && res.Trace.SkipReason != c.detail) {
			t.Errorf("%s: %+v", name, res)
		}
		if _, err := os.Stat(learnedOverlayPath(root, "refunds")); err == nil {
			t.Fatalf("%s wrote an overlay", name)
		}
	}

	res, err := runSkillLearn(base(root, nil))
	if err != nil || res.Decision != "applied" || res.Overlay == nil {
		t.Fatalf("apply: %+v %v", res, err)
	}
	baseHash, _ := fingerprintSkillTree(filepath.Join(root, "skills", "refunds"))
	raw, _ := os.ReadFile(learnedOverlayPath(root, "refunds"))
	want := "---\nskill_name: refunds\nbase_hash: " + baseHash + "\nstatus: applied\n---\n\nPrefer parent items when product structures would double-count.\n"
	if string(raw) != want || res.Trace.BaseHash != baseHash || res.Trace.ContentHash != "same-hash" ||
		res.Diff != "--- before\n\n+++ after\nPrefer parent items when product structures would double-count.\n" {
		t.Fatalf("overlay %q, result %+v", raw, res)
	}
	if skill, _ := os.ReadFile(filepath.Join(root, "skills", "refunds", "SKILL.md")); string(skill) != "# Refunds\n" {
		t.Errorf("SKILL.md changed: %q", skill)
	}
	if hash, _ := fingerprintEffectiveSkill(root, "refunds", filepath.Join(root, "skills", "refunds")); hash == baseHash {
		t.Error("the turn's skill hash does not see the overlay")
	}

	if err := os.WriteFile(filepath.Join(root, "skills", "refunds", "SKILL.md"), []byte("# Refunds v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, _ := runSkillLearn(base(root, nil)); res.Decision != "stale" || res.Reason != "existing overlay base_hash does not match current base" {
		t.Errorf("stale overlay: %+v", res)
	}
}

func TestParseSkillLearnProposal(t *testing.T) {
	got := parseSkillLearnProposal(`here
{"lesson":"parent items","rationale":"child rows double-count","learnedMarkdown":"Prefer parent items.","evidenceRunIds":["run-1","nope",3]} done`, []string{"run-1", "run-2"})
	if got == nil || got.Lesson != "parent items" || got.LearnedMarkdown != "Prefer parent items." || strings.Join(got.EvidenceRunIDs, ",") != "run-1" {
		t.Fatalf("parsed %+v", got)
	}
	for _, raw := range []string{
		`{"lesson":"x","rationale":"y","learnedMarkdown":"z","evidenceRunIds":["other"]}`,
		`{"lesson":"x","rationale":"y","learnedMarkdown":"z","evidenceRunIds":[]}`,
		`{"lesson":" ","rationale":"y","learnedMarkdown":"z","evidenceRunIds":["run-1"]}`,
		`["run-1"]`,
		`no json`,
	} {
		if got := parseSkillLearnProposal(raw, []string{"run-1"}); got != nil {
			t.Errorf("%s parsed to %+v", raw, got)
		}
	}
}

func TestLearnedTextProblems(t *testing.T) {
	for text, want := range map[string]string{
		"api_key=sk-abc1234567890xyz":         "candidate looks like a secret",
		"Send it with Bearer abc.def":         "candidate looks like a secret",
		"email ops@example.com":               "candidate contains an email address",
		"---\nname: hijack\n---":              "candidate tries to set skill frontmatter",
		"Use the parent.\r  name : x":         "candidate tries to set skill frontmatter",
		"allowed-tools: bash":                 "candidate edits tool permissions",
		"Boundary: Never refund":              "candidate edits a safety boundary",
		"First line   Boundary : none":        "candidate edits a safety boundary",
		"Never return an item to its origin.": "",
	} {
		if got := strings.Join(learnedTextProblems(text), "; "); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
	if got := additiveLearnedBodyProblem(strings.Repeat("é", 8001)); got != "learned body is too long" {
		t.Errorf("long body: %q", got)
	}
	if got := additiveLearnedBodyProblem(" \n"); got != "learned body is empty" {
		t.Errorf("empty body: %q", got)
	}
}

func TestSkillLearnJob(t *testing.T) {
	f, db, g := hostFront(t)
	t.Setenv("HOME", t.TempDir())
	ctx := t.Context()
	seedPrimary(t, db, "openai", "gpt-5", `{}`, `{"apiKey":"k"}`)
	refunds, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "packs", "magento", "skills", "magento-investigate-refunds"))
	if err != nil {
		t.Fatal(err)
	}
	root := orgAgentRoot("org")
	skillDir := filepath.Join(root, "skills", "magento-investigate-refunds")
	if err := os.CopyFS(skillDir, os.DirFS(refunds)); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into work_thread (id, org_id) values ('c3000000-0000-4000-8000-000000000001', 'org')`)
	var runIDs []string
	for i := range 5 {
		var id string
		if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend) values ('org', 'c3000000-0000-4000-8000-000000000001', 'ax') returning id::text`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, id)
		seed(t, db, `insert into skill_usage (org_id, run_id, skill_name, content_hash, origin, source, first_event_id, created_at)
			values ('org', $1, 'magento-investigate-refunds', 'h1', 'pack', 'read', $2, now() - interval '3 hours')`, id, i+1)
	}
	evidence, _ := json.Marshal(runIDs[:3])
	g.reply = map[string]any{"skillLearningProposal": `{"lesson":"parent items","rationale":"five runs double-counted child rows","learnedMarkdown":"Prefer parent items when product structures would double-count.","evidenceRunIds":` + string(evidence) + `}`}

	if err := f.API.SkillLearn(ctx, "org", "magento-investigate-refunds"); err != nil {
		t.Fatal(err)
	}
	if len(g.calls) != 0 {
		t.Fatalf("the model was asked with org learning off: %d calls", len(g.calls))
	}
	seed(t, db, `insert into skill_learn_org (org_id, enabled) values ('org', true)`)
	if err := f.API.SkillLearn(ctx, "org", "magento-investigate-refunds"); err != nil {
		t.Fatal(err)
	}
	if len(g.calls) != 1 {
		t.Fatalf("gateway calls: %d", len(g.calls))
	}
	prompt, _ := g.calls[0].request.Values["learningPrompt"].(string)
	original, _ := os.ReadFile(filepath.Join(refunds, "SKILL.md"))
	if !strings.Contains(prompt, "skillName: magento-investigate-refunds\nbaseHash: ") || !strings.Contains(prompt, runIDs[4]) ||
		!strings.Contains(prompt, "skill excerpt:\n"+jsSlice(string(original), 4000)) {
		t.Errorf("prompt %q", prompt)
	}

	rows, err := db.Query(ctx, `select decision, reason, coalesce(lesson, ''), run_ids::text, model_trace->>'baseHash', coalesce(diff, '') from skill_learn_event order by created_at`)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	var baseHash string
	for rows.Next() {
		var decision, reason, lesson, ids, diff string
		var traceBase *string
		if err := rows.Scan(&decision, &reason, &lesson, &ids, &traceBase, &diff); err != nil {
			t.Fatal(err)
		}
		events = append(events, decision+"/"+reason+"/"+lesson)
		if decision == "applied" {
			var got []string
			_ = json.Unmarshal([]byte(ids), &got)
			if strings.Join(got, ",") != strings.Join(runIDs[:3], ",") || traceBase == nil || !strings.Contains(diff, "+++ after\nPrefer parent items") {
				t.Errorf("applied event: ids %s base %v diff %q", ids, traceBase, diff)
			}
			baseHash = *traceBase
		}
	}
	rows.Close()
	if strings.Join(events, " | ") != "skipped/flags_off/ | applied/applied additive learned guidance/parent items" {
		t.Fatalf("events %q", events)
	}
	var stateBase, learned string
	var on bool
	if err := db.QueryRow(ctx, `select enabled, current_base_hash, current_learned_hash from skill_learn_state where org_id = 'org' and skill_name = 'magento-investigate-refunds'`).
		Scan(&on, &stateBase, &learned); err != nil || !on || stateBase != baseHash || learned != sha256Hex("Prefer parent items when product structures would double-count.") {
		t.Errorf("state: %v %q %q %v", on, stateBase, learned, err)
	}
	if skill, _ := os.ReadFile(filepath.Join(skillDir, "SKILL.md")); string(skill) != string(original) {
		t.Error("the pack SKILL.md changed")
	}
	if o := readLearnedOverlay(root, "magento-investigate-refunds"); o == nil || o.BaseHash != baseHash || o.Status != "applied" {
		t.Errorf("overlay %+v", o)
	}
}
