package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// The pg-boss send options of the queue: retry limit 2 and retry delay 30 s.
// enqueueSkillLearn in packages/db/src/jobs.ts writes the same max_attempts
// and adds no job while one for the same skill waits; a running job does not
// block the next, so uses made during a pass get a pass of their own.
const (
	KindSkillLearn       = "skill_learn"
	SkillLearnAttempts   = 3
	SkillLearnRetryDelay = 30 * time.Second
	SkillLearnTimeout    = 15 * time.Minute
)

type SkillLearnArgs struct {
	OrgID     string `json:"orgId"`
	SkillName string `json:"skillName"`
}

func (SkillLearnArgs) Kind() string { return KindSkillLearn }

func (SkillLearnArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: SkillLearnAttempts}
}

type skillLearn struct {
	river.WorkerDefaults[SkillLearnArgs]
	run func(ctx context.Context, orgID, skillName string) error
}

func (w *skillLearn) Work(ctx context.Context, job *river.Job[SkillLearnArgs]) error {
	if w.run == nil {
		return errors.New("skill learning has no runner")
	}
	if job.Args.OrgID == "" || job.Args.SkillName == "" {
		return river.JobCancel(errors.New("the job names no org or skill"))
	}
	return w.run(ctx, job.Args.OrgID, job.Args.SkillName)
}

func (w *skillLearn) Timeout(*river.Job[SkillLearnArgs]) time.Duration { return SkillLearnTimeout }

func (w *skillLearn) NextRetry(*river.Job[SkillLearnArgs]) time.Time {
	return time.Now().Add(SkillLearnRetryDelay)
}

const KindBotNameSweep = "bot_name_sweep"

type BotNameSweepArgs struct{}

func (BotNameSweepArgs) Kind() string { return KindBotNameSweep }

func (BotNameSweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
		rivertype.JobStateRunning, rivertype.JobStateScheduled,
	}}}
}

type botNameSweep struct {
	river.WorkerDefaults[BotNameSweepArgs]
	run func(context.Context) error
}

func (w *botNameSweep) Work(ctx context.Context, _ *river.Job[BotNameSweepArgs]) error {
	if w.run == nil {
		return errors.New("the bot name sweep has no runner")
	}
	return w.run(ctx)
}

// QuarterHourly fires at 00, 15, 30 and 45 past each UTC hour, as the cron
// "*/15 * * * *" did.
type QuarterHourly struct{}

func (QuarterHourly) Next(t time.Time) time.Time {
	return t.UTC().Truncate(15 * time.Minute).Add(15 * time.Minute)
}
