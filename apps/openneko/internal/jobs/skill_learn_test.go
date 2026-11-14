package jobs

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func nodeSkillLearnInsert(t *testing.T, schema string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "db", "src", "jobs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)export async function enqueueSkillLearn.*?`(.*?)`").FindSubmatch(raw)
	if m == nil {
		t.Fatal("enqueueSkillLearn not found in packages/db/src/jobs.ts")
	}
	return strings.ReplaceAll(string(m[1]), "river.", schema+".")
}

func TestNodeSkillLearnJobRunsOnceInGo(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	insert := nodeSkillLearnInsert(t, q.Schema)
	for _, args := range []string{
		`{"orgId":"org-1","skillName":"refunds"}`,
		`{"orgId":"org-1","skillName":"refunds"}`,
		`{"orgId":"org-1","skillName":"returns"}`,
	} {
		if _, err := pool.Exec(ctx, insert, args); err != nil {
			t.Fatal(err)
		}
	}
	var queued int
	if err := pool.QueryRow(ctx, `select count(*) from `+q.Schema+`.river_job where kind = $1`, KindSkillLearn).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("queued %d skill_learn jobs (%v), want 2: a second insert for the same skill adds nothing", queued, err)
	}
	ran := make(chan string, 4)
	q.SkillLearn = func(_ context.Context, orgID, skill string) error { ran <- orgID + "/" + skill; return nil }
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Stop(ctx)
	got := map[string]bool{}
	for len(got) < 2 {
		select {
		case s := <-ran:
			got[s] = true
		case <-time.After(20 * time.Second):
			t.Fatalf("ran %v after 20 s", got)
		}
	}
	if !got["org-1/refunds"] || !got["org-1/returns"] {
		t.Fatalf("ran %v", got)
	}
	if _, err := pool.Exec(ctx, insert, `{"orgId":"org-1","skillName":"refunds"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-ran:
		if s != "org-1/refunds" {
			t.Fatalf("ran %s", s)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a skill whose last job started does not queue again")
	}
}

func TestSkillLearnJobWithoutNamesIsCancelled(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	q.SkillLearn = func(context.Context, string, string) error { called <- struct{}{}; return nil }
	if _, err := pool.Exec(ctx, `insert into `+q.Schema+`.river_job (kind, args, max_attempts) values ($1, '{"orgId":"org-1"}', 3)`, KindSkillLearn); err != nil {
		t.Fatal(err)
	}
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Stop(ctx)
	deadline := time.Now().Add(20 * time.Second)
	for {
		var state string
		_ = pool.QueryRow(ctx, `select state::text from `+q.Schema+`.river_job where kind = $1`, KindSkillLearn).Scan(&state)
		if state == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the job is %q after 20 s, want cancelled", state)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(called) != 0 {
		t.Fatal("a job with no skill name reached the runner")
	}
}

func TestQuarterHourly(t *testing.T) {
	for in, want := range map[string]string{
		"2026-11-14T10:00:00Z": "2026-11-14T10:15:00Z",
		"2026-11-14T10:14:59Z": "2026-11-14T10:15:00Z",
		"2026-11-14T23:50:00Z": "2026-11-15T00:00:00Z",
	} {
		at, _ := time.Parse(time.RFC3339, in)
		if got := (QuarterHourly{}).Next(at).Format(time.RFC3339); got != want {
			t.Errorf("Next(%s) = %s, want %s", in, got, want)
		}
	}
}
