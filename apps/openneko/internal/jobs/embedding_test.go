package jobs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type fakeEmbedding struct {
	mu    sync.Mutex
	model string
	calls []string
	next  []func(text string) ([]float64, error)
}

func (f *fakeEmbedding) Embedding() Embedding {
	return Embedding{
		Model: func() string { f.mu.Lock(); defer f.mu.Unlock(); return f.model },
		Embed: func(_ context.Context, text string) ([]float64, error) {
			f.mu.Lock()
			f.calls = append(f.calls, text)
			var step func(string) ([]float64, error)
			if len(f.next) > 0 {
				step, f.next = f.next[0], f.next[1:]
			}
			f.mu.Unlock()
			if step != nil {
				return step(text)
			}
			return []float64{1, 0, 0}, nil
		},
	}
}

func (f *fakeEmbedding) setModel(m string) { f.mu.Lock(); f.model = m; f.mu.Unlock() }
func (f *fakeEmbedding) count() int        { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func embeddingSetup(t *testing.T) (*pgxpool.Pool, *Queue, *river.Client[pgx.Tx]) {
	t.Helper()
	ctx := context.Background()
	pool := testpg.Migrated(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Schema: q.Schema, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into organization (id, name) values ('org-a', 'a'), ('org-b', 'b')`); err != nil {
		t.Fatal(err)
	}
	return pool, q, client
}

func clientInsert(client *river.Client[pgx.Tx]) InsertJobs {
	return func(ctx context.Context, tx pgx.Tx, p []river.InsertManyParams) error {
		_, err := client.InsertManyTx(ctx, tx, p)
		return err
	}
}

func addMemory(t *testing.T, pool *pgxpool.Pool, org, text string, updated time.Time) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `insert into work_memory (org_id, kind, scope, text, updated_at)
values ($1, 'fact', 'global', $2, $3) returning id::text`, org, text, updated).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func indexJobs(t *testing.T, pool *pgxpool.Pool, q *Queue) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select args->>'kind' || ' ' || (args->>'orgId') || ' ' || (args->>'id') || ' ' || queue || ' ' || max_attempts
from `+q.Schema+`.river_job where kind = $1 order by id`, KindEmbeddingIndex)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func memoryVector(t *testing.T, pool *pgxpool.Pool, id string) (bool, string) {
	t.Helper()
	var indexed bool
	var model *string
	if err := pool.QueryRow(context.Background(), `select embedding is not null, embedding_model from work_memory where id = $1`, id).Scan(&indexed, &model); err != nil {
		t.Fatal(err)
	}
	if model == nil {
		return indexed, ""
	}
	return indexed, *model
}

func TestDispatchAndIndexEmbedding(t *testing.T) {
	ctx := context.Background()
	pool, q, client := embeddingSetup(t)
	fake := &fakeEmbedding{}
	q.Embedding = fake.Embedding()
	insert := clientInsert(client)
	id := addMemory(t, pool, "org-a", "original", time.Now())
	job := EmbeddingIndexArgs{Target: "memory", OrgID: "org-a", ID: id}

	if n, err := q.DispatchEmbeddingJobs(ctx, insert); err != nil || n != 0 {
		t.Fatalf("with embedding off: dispatched %d, %v", n, err)
	}
	if err := q.IndexEmbedding(ctx, job); err != nil || fake.count() != 0 {
		t.Fatalf("with embedding off: %v, %d calls", err, fake.count())
	}

	fake.setModel("model-a")
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := q.DispatchEmbeddingJobs(ctx, insert); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := q.DispatchEmbeddingJobs(ctx, insert); err != nil {
		t.Fatal(err)
	}
	if got, want := indexJobs(t, pool, q), []string{"memory org-a " + id + " embedding 9"}; !slices.Equal(got, want) {
		t.Fatalf("index jobs = %q, want %q", got, want)
	}

	fake.next = append(fake.next, func(string) ([]float64, error) { return nil, errors.New("offline") })
	if err := q.IndexEmbedding(ctx, job); err == nil || err.Error() != "offline" {
		t.Fatalf("a failed call gave %v", err)
	}
	if indexed, _ := memoryVector(t, pool, id); indexed {
		t.Fatal("a failed call stored a vector")
	}

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, `select pg_advisory_xact_lock(hashtext('embedding-processing'))`); err != nil {
		t.Fatal(err)
	}
	if err := q.IndexEmbedding(ctx, job); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("with the lock held: %v", err)
	}
	_ = blocker.Rollback(ctx)

	fake.next = append(fake.next, func(string) ([]float64, error) {
		if _, err := pool.Exec(ctx, `update work_memory set text = 'edited' where id = $1`, id); err != nil {
			return nil, err
		}
		return []float64{1, 0, 0}, nil
	})
	if err := q.IndexEmbedding(ctx, job); err != nil {
		t.Fatal(err)
	}
	if indexed, _ := memoryVector(t, pool, id); indexed {
		t.Fatal("an edit during the call got the obsolete vector")
	}

	calls := fake.count()
	if err := q.IndexEmbedding(ctx, EmbeddingIndexArgs{Target: "memory", OrgID: "wrong-org", ID: id}); err != nil || fake.count() != calls {
		t.Fatalf("another org's job: %v, %d new calls", err, fake.count()-calls)
	}
	if err := q.IndexEmbedding(ctx, job); err != nil {
		t.Fatal(err)
	}
	if last := fake.calls[len(fake.calls)-1]; last != "edited" {
		t.Fatalf("embedded %q, want the edited text", last)
	}
	if indexed, model := memoryVector(t, pool, id); !indexed || model != "model-a" {
		t.Fatalf("after the job: indexed %v, model %q", indexed, model)
	}
	calls = fake.count()
	if err := q.IndexEmbedding(ctx, job); err != nil || fake.count() != calls {
		t.Fatalf("an indexed row was embedded again: %v", err)
	}
	fake.setModel("model-b")
	if err := q.IndexEmbedding(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, model := memoryVector(t, pool, id); model != "model-b" {
		t.Fatalf("a model change left model %q", model)
	}
	if _, err := pool.Exec(ctx, `update work_memory set embedding = null, archived_at = now() where id = $1`, id); err != nil {
		t.Fatal(err)
	}
	calls = fake.count()
	if err := q.IndexEmbedding(ctx, job); err != nil || fake.count() != calls {
		t.Fatalf("an archived memory was embedded: %v", err)
	}

	if _, err := pool.Exec(ctx, `update `+q.Schema+`.river_job set state = 'completed', finalized_at = now() where kind = $1`, KindEmbeddingIndex); err != nil {
		t.Fatal(err)
	}
	if n, err := q.DispatchEmbeddingJobs(ctx, insert); err != nil || n != 0 {
		t.Fatalf("an archived memory was queued: %d, %v", n, err)
	}
	if _, err := pool.Exec(ctx, `update work_memory set archived_at = null where id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if n, err := q.DispatchEmbeddingJobs(ctx, insert); err != nil || n != 1 {
		t.Fatalf("after the job completed: dispatched %d, %v", n, err)
	}

	if err := q.IndexEmbedding(ctx, EmbeddingIndexArgs{Target: "concept", OrgID: "org-a", ID: id}); err == nil || err.Error() != "Invalid embedding job" {
		t.Fatalf("an unknown kind gave %v", err)
	}
}

func TestIndexSkillEmbedding(t *testing.T) {
	ctx := context.Background()
	pool, q, _ := embeddingSetup(t)
	fake := &fakeEmbedding{model: "model-a"}
	q.Embedding = fake.Embedding()
	var id string
	if err := pool.QueryRow(ctx, `insert into skill_embedding (org_id, skill_name, document) values ('org-a', 's', $1) returning id::text`,
		strings.Repeat("é", 4100)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := q.IndexEmbedding(ctx, EmbeddingIndexArgs{Target: "skill", OrgID: "org-a", ID: id}); err != nil {
		t.Fatal(err)
	}
	if got := fake.calls[0]; got != strings.Repeat("é", 4000) {
		t.Fatalf("embedded %d characters, want the first 4000", len([]rune(got)))
	}
	var model string
	if err := pool.QueryRow(ctx, `select embedding_model from skill_embedding where id = $1 and embedding is not null`, id).Scan(&model); err != nil || model != "model-a" {
		t.Fatalf("skill vector: %q, %v", model, err)
	}
}

func TestDispatchEmbeddingLimits(t *testing.T) {
	ctx := context.Background()
	pool, q, client := embeddingSetup(t)
	q.Embedding = (&fakeEmbedding{model: "model-a"}).Embedding()
	start := time.Now().Add(-time.Hour)
	for i := range 20 {
		addMemory(t, pool, "org-a", "a", start.Add(time.Duration(i)*time.Second))
	}
	for i := range 120 {
		addMemory(t, pool, "org-b", "b", start.Add(time.Duration(100+i)*time.Second))
	}
	addMemory(t, pool, "org-a", "   ", start)
	if _, err := pool.Exec(ctx, `insert into work_memory (org_id, kind, scope, text, expires_at) values ('org-a', 'fact', 'global', 'old', now() - interval '1 minute')`); err != nil {
		t.Fatal(err)
	}
	insert := clientInsert(client)
	n, err := q.DispatchEmbeddingJobs(ctx, insert)
	if err != nil || n != 32 {
		t.Fatalf("first dispatch: %d, %v", n, err)
	}
	jobs := indexJobs(t, pool, q)
	for i, j := range jobs[:32] {
		if want := []string{"org-a", "org-b"}[i%2]; !strings.Contains(j, " "+want+" ") {
			t.Fatalf("job %d is %q, want %s in turn", i, j, want)
		}
	}
	total := 32
	for {
		n, err := q.DispatchEmbeddingJobs(ctx, insert)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		total += n
	}
	if total != 128 {
		t.Fatalf("pending jobs = %d, want the cap of 128", total)
	}
}

func TestEmbeddingBackoff(t *testing.T) {
	for _, c := range []struct {
		attempt  int
		lo, high time.Duration
	}{
		{1, 30 * time.Second, 60 * time.Second},
		{2, 60 * time.Second, 120 * time.Second},
		{8, 3840 * time.Second, 7680 * time.Second},
	} {
		if got := embeddingBackoff(c.attempt, 0); got != c.lo {
			t.Errorf("attempt %d, no jitter: %v, want %v", c.attempt, got, c.lo)
		}
		if got := embeddingBackoff(c.attempt, 1); got != c.high {
			t.Errorf("attempt %d, full jitter: %v, want %v", c.attempt, got, c.high)
		}
	}
}

func TestUTF16Prefix(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"abcdef", 3, "abc"},
		{"ab", 5, "ab"},
		{"a😀b", 3, "a😀"},
		{"a😀b", 2, "a"},
	} {
		if got := utf16Prefix(c.in, c.n); got != c.want {
			t.Errorf("utf16Prefix(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestRunnerRunsEmbeddingJobs(t *testing.T) {
	ctx := context.Background()
	pool, q, _ := embeddingSetup(t)
	var running, peak atomic.Int32
	fake := &fakeEmbedding{model: "model-a"}
	e := fake.Embedding()
	q.Embedding = Embedding{Model: e.Model, Embed: func(ctx context.Context, text string) ([]float64, error) {
		if n := running.Add(1); n > peak.Load() {
			peak.Store(n)
		}
		defer running.Add(-1)
		time.Sleep(50 * time.Millisecond)
		return e.Embed(ctx, text)
	}}
	var ids []string
	for i := range 4 {
		ids = append(ids, addMemory(t, pool, "org-a", "memory", time.Now().Add(time.Duration(i)*time.Second)))
	}
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Stop(ctx)
	deadline := time.Now().Add(30 * time.Second)
	for {
		var left int
		if err := pool.QueryRow(ctx, `select count(*) from work_memory where embedding is null`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d memories have no vector after 30 s; jobs %q", left, indexJobs(t, pool, q))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if peak.Load() != 1 {
		t.Fatalf("%d embedding calls ran at once, want 1", peak.Load())
	}
	var sweeps int
	if err := pool.QueryRow(ctx, `select count(*) from `+q.Schema+`.river_job where kind = $1 and state = 'completed'`, KindEmbeddingSweep).Scan(&sweeps); err != nil || sweeps < 1 {
		t.Fatalf("completed sweeps at start: %d, %v", sweeps, err)
	}
}

func TestMigrateMovesEmbeddingJobs(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	b := q.BossSchema
	if _, err := pool.Exec(ctx, `
create schema `+b+`;
create table `+b+`.job (id uuid primary key default gen_random_uuid(), name text not null, data jsonb,
  state text not null, retry_limit int not null default 2, start_after timestamptz not null default now());
create table `+b+`.schedule (name text primary key);
insert into `+b+`.job (name, data, state, retry_limit) values
  ('embedding_index', '{"kind":"memory","orgId":"org-a","id":"00000000-0000-4000-8000-000000000001"}', 'created', 8),
  ('embedding_sweep', '{}', 'retry', 3);
insert into `+b+`.schedule values ('embedding_sweep');`); err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `select kind || ' ' || queue || ' ' || max_attempts from `+q.Schema+`.river_job order by kind`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"embedding_index embedding 9", "embedding_sweep default 4"}; !slices.Equal(got, want) {
		t.Fatalf("river jobs = %q, want %q", got, want)
	}
	var left int
	if err := pool.QueryRow(ctx, `select (select count(*) from `+b+`.job) + (select count(*) from `+b+`.schedule)`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("pg-boss rows left: %d, %v", left, err)
	}
}
