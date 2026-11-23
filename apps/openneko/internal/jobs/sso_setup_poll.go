package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// KindSSOSetupPoll checks pending SSO portal setups for each organisation in
// awaiting_portal or polling state.
const KindSSOSetupPoll = "sso_setup_poll"

type SSOSetupPollArgs struct{}

func (SSOSetupPollArgs) Kind() string { return KindSSOSetupPoll }

func (SSOSetupPollArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
		rivertype.JobStateRunning, rivertype.JobStateScheduled,
	}}}
}

type ssoSetupPoll struct {
	river.WorkerDefaults[SSOSetupPollArgs]
	run func(context.Context) error
}

func (w *ssoSetupPoll) Work(ctx context.Context, _ *river.Job[SSOSetupPollArgs]) error {
	if w.run == nil {
		return errors.New("the SSO setup poll has no runner")
	}
	return w.run(ctx)
}

func (w *ssoSetupPoll) NextRetry(*river.Job[SSOSetupPollArgs]) time.Time {
	return time.Now().Add(15 * time.Second)
}
