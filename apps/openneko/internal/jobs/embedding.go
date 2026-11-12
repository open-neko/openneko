package jobs

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const (
	KindEmbeddingIndex = "embedding_index"
	KindEmbeddingSweep = "embedding_sweep"
	// QueueEmbedding runs one index job at a time in each client.
	QueueEmbedding = "embedding"
)

var pendingStates = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
	rivertype.JobStateRunning, rivertype.JobStateScheduled,
}

// Embedding is the embedding service as the index jobs see it. Model is
// empty when embedding is off.
type Embedding struct {
	Model func() string
	Embed func(ctx context.Context, text string) ([]float64, error)
}

func (e Embedding) model() string {
	if e.Model == nil {
		return ""
	}
	return e.Model()
}

type EmbeddingIndexArgs struct {
	Target string `json:"kind"`
	OrgID  string `json:"orgId"`
	ID     string `json:"id"`
}

func (EmbeddingIndexArgs) Kind() string { return KindEmbeddingIndex }

func (EmbeddingIndexArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 9, Queue: QueueEmbedding,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: pendingStates}}
}

type EmbeddingSweepArgs struct{}

func (EmbeddingSweepArgs) Kind() string { return KindEmbeddingSweep }

func (EmbeddingSweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 4, UniqueOpts: river.UniqueOpts{ByState: pendingStates}}
}

type embeddingSweep struct {
	river.WorkerDefaults[EmbeddingSweepArgs]
	q *Queue
}

func (w *embeddingSweep) Work(ctx context.Context, _ *river.Job[EmbeddingSweepArgs]) error {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	_, err = w.q.DispatchEmbeddingJobs(ctx, func(ctx context.Context, tx pgx.Tx, args []river.InsertManyParams) error {
		_, err := client.InsertManyTx(ctx, tx, args)
		return err
	})
	return err
}

func (w *embeddingSweep) NextRetry(*river.Job[EmbeddingSweepArgs]) time.Time {
	return time.Now().Add(15 * time.Second)
}

type embeddingIndex struct {
	river.WorkerDefaults[EmbeddingIndexArgs]
	q *Queue
}

func (w *embeddingIndex) Work(ctx context.Context, job *river.Job[EmbeddingIndexArgs]) error {
	return w.q.IndexEmbedding(ctx, job.Args)
}

func (w *embeddingIndex) Timeout(*river.Job[EmbeddingIndexArgs]) time.Duration {
	return 180 * time.Second
}

// NextRetry is pg-boss's retryBackoff with a 30-second delay.
func (w *embeddingIndex) NextRetry(job *river.Job[EmbeddingIndexArgs]) time.Time {
	return time.Now().Add(embeddingBackoff(job.Attempt, rand.Float64()))
}

func embeddingBackoff(attempt int, jitter float64) time.Duration {
	half := 30 * math.Pow(2, float64(min(16, max(attempt, 1)))) / 2
	return time.Duration((half + half*jitter) * float64(time.Second))
}

// InsertJobs inserts River jobs in the caller's transaction.
type InsertJobs func(ctx context.Context, tx pgx.Tx, jobs []river.InsertManyParams) error

// DispatchEmbeddingJobs queues an index job for each memory and skill that
// has no vector from the current model: at most 32 per call, at most 128
// pending, in turn across organisations. It returns the number queued.
func (q *Queue) DispatchEmbeddingJobs(ctx context.Context, insert InsertJobs) (int, error) {
	model := q.Embedding.model()
	if model == "" {
		return 0, nil
	}
	tx, err := q.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var acquired bool
	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtext('embedding-dispatch'))`).Scan(&acquired); err != nil {
		return 0, err
	}
	if !acquired {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `
with pending as (
  select args->>'kind' || ':' || (args->>'id') as key from `+pgx.Identifier{q.schema()}.Sanitize()+`.river_job
  where kind = $1 and state = any($3::text[]::`+pgx.Identifier{q.schema()}.Sanitize()+`.river_job_state[])
), candidates as (
  select 'memory' as kind, id, org_id, updated_at from work_memory
  where (embedding is null or embedding_model is distinct from $2) and archived_at is null and not suppressed and btrim(text) <> ''
    and (expires_at is null or expires_at > now())
  union all
  select 'skill', id, org_id, updated_at from skill_embedding
  where embedding is null or embedding_model is distinct from $2
), ranked as (
  select *, row_number() over (partition by org_id order by updated_at, id) as turn
  from candidates c where not exists (select 1 from pending p where p.key = c.kind || ':' || c.id::text)
) select kind, id::text, org_id from ranked order by turn, updated_at, id
limit greatest(0, least(32, 128 - (select count(*) from pending)))`, KindEmbeddingIndex, model, stateNames(pendingStates))
	if err != nil {
		return 0, err
	}
	params, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (river.InsertManyParams, error) {
		var a EmbeddingIndexArgs
		err := row.Scan(&a.Target, &a.ID, &a.OrgID)
		return river.InsertManyParams{Args: a}, err
	})
	if err != nil {
		return 0, err
	}
	if len(params) > 0 {
		if err := insert(ctx, tx, params); err != nil {
			return 0, err
		}
	}
	return len(params), tx.Commit(ctx)
}

func stateNames(states []rivertype.JobState) []string {
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = string(s)
	}
	return out
}

var errEmbeddingBusy = errors.New("Embedding processing is busy")

// IndexEmbedding stores the vector of one memory or skill. An edit, archive
// or delete during the call leaves the row without the obsolete vector.
func (q *Queue) IndexEmbedding(ctx context.Context, a EmbeddingIndexArgs) error {
	if (a.Target != "memory" && a.Target != "skill") || a.OrgID == "" || a.ID == "" {
		return errors.New("Invalid embedding job")
	}
	model := q.Embedding.model()
	if model == "" {
		return nil
	}
	table, column, limit := "skill_embedding", "document", 4000
	eligible := `(embedding is null or embedding_model is distinct from $3)`
	if a.Target == "memory" {
		table, column, limit = "work_memory", "text", 32000
		eligible += ` and archived_at is null and not suppressed and (expires_at is null or expires_at > now())`
	}
	tx, err := q.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var acquired bool
	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtext('embedding-processing'))`).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return errEmbeddingBusy
	}
	var content string
	err = tx.QueryRow(ctx, `select `+column+` from `+table+` where org_id = $1 and id = $2 and `+eligible,
		a.OrgID, a.ID, model).Scan(&content)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if q.Embedding.Embed == nil {
		return errors.New("the embedding index job has no embedder")
	}
	vector, err := q.Embedding.Embed(ctx, utf16Prefix(content, limit))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update `+table+` set embedding = $4::vector, embedding_model = $3
where org_id = $1 and id = $2 and `+eligible+` and `+column+` = $5`,
		a.OrgID, a.ID, model, vectorLiteral(vector), content); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// utf16Prefix is String.prototype.slice(0, n): n UTF-16 code units.
func utf16Prefix(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

func vectorLiteral(v []float64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	}
	b.WriteByte(']')
	return b.String()
}
