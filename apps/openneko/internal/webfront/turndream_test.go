package webfront

import (
	"context"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const (
	dreamThread = "d1000000-0000-4000-8000-000000000001"
	dreamRun    = "d2000000-0000-4000-8000-000000000001"
)

func TestDreamLessonsContextOffersTheClosestActiveLessonsOfTheOrg(t *testing.T) {
	t.Setenv("NEKO_EMBEDDING_URL", "")
	t.Setenv("NEKO_EMBEDDING_MODEL", "")
	db := testpg.Migrated(t)
	seedOrg(t, db)
	seed(t, db, `insert into organization (id, name) values ('other', 'Other')`)
	seed(t, db, `insert into work_thread (id, org_id) values ($1, 'org')`, dreamThread)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ($1, 'org', $2, 'test')`, dreamRun, dreamThread)
	seed(t, db, `insert into dream_lesson (org_id, kind, question_shape, body, state) values
		('org', 'query', 'revenue by product category in a period', E'query {\n  category_revenue\n}', 'active'),
		('org', 'data_fact', 'the date range of sales orders', 'Order dates run from 2022-05-30 to 2025-06-29.', 'active'),
		('org', 'query', 'revenue by product category, old version', 'query { retired }', 'retired'),
		('other', 'query', 'revenue by product category in a period', 'query { other_org }', 'active')`)
	for n := range 6 {
		seed(t, db, `insert into dream_lesson (org_id, kind, question_shape, body) values ('org', 'schema_fact', $1, 'filler')`,
			"headcount of department "+string(rune('a'+n)))
	}
	a := &API{DB: db}
	ctx := context.Background()

	got, err := a.dreamLessonsContext(ctx, "org", dreamRun, "what was the revenue for each product category in Q3 2024")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	if !strings.Contains(lines[0], "] query · revenue by product category in a period") ||
		lines[1] != "  query {" || lines[2] != "    category_revenue" {
		t.Fatalf("the closest lesson is not first, with its body indented:\n%s", got)
	}
	if n := strings.Count(got, "\n- [") + 1; n != dreamLessonsPerTurn {
		t.Fatalf("offered %d lessons:\n%s", n, got)
	}
	if strings.Contains(got, "retired") || strings.Contains(got, "other_org") {
		t.Fatalf("offered a retired lesson or a lesson of another org:\n%s", got)
	}

	var uses, used int
	if err := db.QueryRow(ctx, `select count(*) from dream_lesson_use where run_id = $1 and org_id = 'org' and outcome is null`, dreamRun).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select count(*) from dream_lesson where use_count = 1 and last_used_at is not null`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if uses != dreamLessonsPerTurn || used != dreamLessonsPerTurn {
		t.Fatalf("recorded %d uses and %d used lessons", uses, used)
	}

	if _, err := a.dreamLessonsContext(ctx, "org", dreamRun, "what was the revenue for each product category in Q3 2024"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select count(*) from dream_lesson where use_count > 1`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Fatalf("a second call for the same run counted %d lessons again", used)
	}

	if got, err := a.dreamLessonsContext(ctx, "other", dreamRun, "  "); err != nil || got != "" {
		t.Fatalf("an empty question gave %q %v", got, err)
	}
	if got := lessonsSection(""); got != "" {
		t.Fatalf("a turn with no lessons has a lessons section: %q", got)
	}
	if got := lessonsSection("- [x] query · shape"); !strings.HasPrefix(got, "<lessons>") || !strings.Contains(got, "- [x] query · shape\n</lessons>") {
		t.Fatalf("lessons section: %q", got)
	}
}

func TestTraceStepKeepsTheErrorTextOfAFailedToolResult(t *testing.T) {
	reply, _ := jsParse([]byte(`{"content":[{"type":"text","text":"{\"errors\":[{\"message\":\"Field 'totl' is not a column on the queried table; key sk-planted-1\"}]}"}]}`))
	var contents []string
	unregister := registerTraceSink("run-dream-trace", func(_, content *jsObj) {
		contents = append(contents, jsStringify(content, false))
	}, secretScrubber([]string{"sk-planted-1"}))
	args, _ := jsParse([]byte(`{"query":"query { orders { totl } }"}`))
	_, _ = traceGraphjinToolCall(traceBinding{RunID: "run-dream-trace", OrgID: "org", Kind: "work"}, "execute_graphql", args.(*jsObj),
		func() (any, error) { return reply, nil })
	unregister()
	if len(contents) != 1 || !strings.Contains(contents[0], `"error":"Field 'totl' is not a column on the queried table; key [REDACTED]"`) {
		t.Fatalf("step content: %v", contents)
	}
}
