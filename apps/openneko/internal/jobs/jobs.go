// Package jobs runs OpenNeko's background jobs on River, in the river schema.
// Queues move here from the Node worker's pg-boss one at a time.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const (
	DefaultSchema     = "river"
	DefaultBossSchema = "pgboss"
)

// Moved lists the queues that River runs. MOVED_QUEUES in
// packages/db/src/jobs.ts must hold the same names.
var Moved = []string{KindOutputSweep, KindMetricSweep, KindCronSweep, KindEmbeddingIndex, KindEmbeddingSweep, KindDirectorySync, KindActionExecute, KindSkillLearn, KindBotNameSweep,
	KindBusinessProfileBuild, KindIndustryInsightsBuild, KindBootstrapMetricsBuild, KindMetricRefresh, KindWorkflowRunFire, KindWorkflowAPISweep}

type Queue struct {
	Pool          *pgxpool.Pool
	Schema        string
	BossSchema    string
	Logf          func(string, ...any)
	MetricSweep   func(context.Context) error
	CronSweep     func(context.Context) error
	DirectorySync func(context.Context) error
	// ActionExecute asks the Node worker to run an approved action; the
	// action adapters live in the worker until the packs runtime moves.
	ActionExecute    func(ctx context.Context, orgID, actionRequestID string) error
	SkillLearn       func(ctx context.Context, orgID, skillName string) error
	BotNameSweep     func(context.Context) error
	Build            BuildFunc
	WorkflowRunFire  WorkflowRunFireFunc
	WorkflowAPISweep func(context.Context) error
	Embedding        Embedding
}

func (q *Queue) schema() string {
	if q.Schema != "" {
		return q.Schema
	}
	return DefaultSchema
}

func (q *Queue) bossSchema() string {
	if q.BossSchema != "" {
		return q.BossSchema
	}
	return DefaultBossSchema
}

func (q *Queue) logf(format string, args ...any) {
	if q.Logf != nil {
		q.Logf(format, args...)
	}
}

// Migrate brings River's tables up to date and moves the pending pg-boss jobs
// of each moved queue to River. The caller holds the migration lock.
func (q *Queue) Migrate(ctx context.Context) error {
	if _, err := q.Pool.Exec(ctx, `create schema if not exists `+pgx.Identifier{q.schema()}.Sanitize()); err != nil {
		return fmt.Errorf("create the %s schema: %w", q.schema(), err)
	}
	mig, err := rivermigrate.New(riverpgxv5.New(q.Pool), &rivermigrate.Config{Schema: q.schema(), Logger: quietLogger()})
	if err != nil {
		return err
	}
	res, err := mig.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("migrate the job queue: %w", err)
	}
	if len(res.Versions) > 0 {
		q.logf("[migrate] job queue: %d River migration(s) applied", len(res.Versions))
	}
	for _, name := range Moved {
		n, err := q.drain(ctx, name)
		if err != nil {
			return fmt.Errorf("move the %s jobs from pg-boss: %w", name, err)
		}
		if n > 0 {
			q.logf("[migrate] job queue: moved %d pending %s job(s) from pg-boss", n, name)
		}
	}
	return nil
}

func (q *Queue) drain(ctx context.Context, name string) (int64, error) {
	boss := pgx.Identifier{q.bossSchema()}.Sanitize()
	river := pgx.Identifier{q.schema()}.Sanitize()
	var job, schedule *string
	if err := q.Pool.QueryRow(ctx, `select to_regclass($1)::text, to_regclass($2)::text`,
		q.bossSchema()+".job", q.bossSchema()+".schedule").Scan(&job, &schedule); err != nil {
		return 0, err
	}
	if job == nil {
		return 0, nil
	}
	tx, err := q.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `
with moved as (
  delete from `+boss+`.job where name = $1 and state in ('created', 'retry')
  returning name, data, retry_limit, start_after
)
insert into `+river+`.river_job (kind, args, max_attempts, scheduled_at, state, queue)
select name, coalesce(data, '{}'::jsonb), greatest(retry_limit, 0) + 1, start_after,
  (case when start_after > now() then 'scheduled' else 'available' end)::`+river+`.river_job_state, $2
from moved`, name, queueOf(name))
	if err != nil {
		return 0, err
	}
	if schedule != nil {
		if _, err := tx.Exec(ctx, `delete from `+boss+`.schedule where name = $1`, name); err != nil {
			return 0, err
		}
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

func queueOf(kind string) string {
	switch kind {
	case KindEmbeddingIndex:
		return QueueEmbedding
	case KindMetricRefresh:
		return QueueMetricRefresh
	case KindWorkflowRunFire:
		return QueueWorkflowRun
	}
	return river.QueueDefault
}

// Runner is a started River client.
type Runner struct {
	client *river.Client[pgx.Tx]
}

// Start runs the moved queues' workers and their schedules. Only the elected
// leader among running clients inserts the scheduled jobs.
func (q *Queue) Start(ctx context.Context) (*Runner, error) {
	config := q.config()
	config.PeriodicJobs = periodicJobs()
	client, err := river.NewClient(riverpgxv5.New(q.Pool), config)
	if err != nil {
		return nil, err
	}
	if err := client.Start(ctx); err != nil {
		return nil, err
	}
	return &Runner{client: client}, nil
}

func (q *Queue) config() *river.Config {
	workers := river.NewWorkers()
	river.AddWorker(workers, &outputSweep{pool: q.Pool, logf: q.logf})
	river.AddWorker(workers, &metricSweep{run: q.MetricSweep})
	river.AddWorker(workers, &cronSweep{run: q.CronSweep})
	river.AddWorker(workers, &embeddingSweep{q: q})
	river.AddWorker(workers, &embeddingIndex{q: q})
	river.AddWorker(workers, &directorySync{run: q.DirectorySync})
	river.AddWorker(workers, &actionExecute{run: q.ActionExecute})
	river.AddWorker(workers, &skillLearn{run: q.SkillLearn})
	river.AddWorker(workers, &botNameSweep{run: q.BotNameSweep})
	addBuildWorkers(workers, q.Build)
	river.AddWorker(workers, &workflowRunFire{run: q.WorkflowRunFire})
	river.AddWorker(workers, &workflowAPISweep{run: q.WorkflowAPISweep})
	return &river.Config{
		Schema: q.schema(),
		Logger: quietLogger(),
		Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}, QueueEmbedding: {MaxWorkers: 1},
			QueueMetricRefresh: {MaxWorkers: MetricRefreshWorkers()}, QueueWorkflowRun: {MaxWorkers: 1}},
		Workers: workers,
	}
}

// Stop waits up to the context's deadline for running jobs, then cancels them.
func (r *Runner) Stop(ctx context.Context) {
	if err := r.client.Stop(ctx); err != nil {
		_ = r.client.StopAndCancel(context.WithoutCancel(ctx))
	}
}

type cronJob struct {
	schedule   river.PeriodicSchedule
	period     time.Duration
	args       river.JobArgs
	runOnStart bool
}

var cronJobs = []cronJob{
	{schedule: Hourly{}, period: time.Hour, args: OutputSweepArgs{}},
	{schedule: Hourly{}, period: time.Hour, args: MetricSweepArgs{}},
	{schedule: EveryMinute{}, period: time.Minute, args: CronSweepArgs{}},
	{schedule: EveryMinute{}, period: time.Minute, args: EmbeddingSweepArgs{}, runOnStart: true},
	{schedule: SixHourly{}, period: 6 * time.Hour, args: DirectorySyncArgs{}},
	{schedule: QuarterHourly{}, period: 15 * time.Minute, args: BotNameSweepArgs{}, runOnStart: true},
	{schedule: EveryMinute{}, period: time.Minute, args: WorkflowAPISweepArgs{}, runOnStart: true},
}

func periodicJobs() []*river.PeriodicJob {
	out := make([]*river.PeriodicJob, len(cronJobs))
	for i, c := range cronJobs {
		out[i] = river.NewPeriodicJob(c.schedule, func() (river.JobArgs, *river.InsertOpts) {
			return c.args, nil
		}, &river.PeriodicJobOpts{ID: c.args.Kind(), RunOnStart: c.runOnStart})
	}
	return out
}

// Hourly fires at the top of each UTC hour, as the cron "0 * * * *" did.
type Hourly struct{}

func (Hourly) Next(t time.Time) time.Time {
	return t.UTC().Truncate(time.Hour).Add(time.Hour)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}
