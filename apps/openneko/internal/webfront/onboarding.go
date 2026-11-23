package webfront

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"

	"github.com/jackc/pgx/v5"
)

type onboardingStage struct {
	Kind    string  `json:"kind"`
	Message *string `json:"message"`
}

type metricsProgress struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

type onboardingStatus struct {
	State           string           `json:"state"`
	ProfileVersion  *int             `json:"profileVersion,omitempty"`
	Seats           []string         `json:"seats,omitempty"`
	Mode            string           `json:"mode,omitempty"`
	JobID           string           `json:"jobId,omitempty"`
	CurrentStage    *onboardingStage `json:"currentStage,omitempty"`
	MetricsProgress *metricsProgress `json:"metricsProgress,omitempty"`
	Message         string           `json:"message,omitempty"`
}

func demoMode() bool {
	return os.Getenv("NEXT_PUBLIC_DEMO") == "true" || os.Getenv("DEMO") == "true"
}

const (
	failureRuntime  = "Setup could not finish because the agent became unavailable. Confirm `openneko status` shows serving, re-test the model in Admin → Settings → Agent, then try again."
	failureTimeout  = "The profile agent reached its setup time limit before finishing. Re-test the model in Admin → Settings → Agent, then try again; if it repeats, inspect `openneko logs` for the recorded agent activity."
	failureProvider = "Setup could not finish because the model provider rejected or interrupted the request. Re-test the model in Admin → Settings → Agent, then try again."
	failureHandoff  = "Setup could not finish because the worker could not load the saved model configuration. Re-save or re-test the model in Admin → Settings → Agent, then try again; if it repeats, inspect `openneko logs` for the configuration handoff error."
	failureData     = "Setup could not finish while reading the configured data source. Re-test the source in Admin → Settings → Data Sources, then try again."
	failureGeneric  = "Setup could not finish. Inspect `openneko logs` for the recorded cause, then try again."
)

var failureClasses = []struct {
	re      *regexp.Regexp
	message string
}{
	{regexp.MustCompile(`(?i)turn exceeded|timed? out|timeout|max(?:imum)? iterations|iteration budget`), failureTimeout},
	{regexp.MustCompile(`(?i)openneko serve|serve socket|agent sandbox|agent runtime|builtin-skills|mcp-bridge|scandir`), failureRuntime},
	{regexp.MustCompile(`(?i)no llm provider configured`), failureHandoff},
	{regexp.MustCompile(`(?i)provider|gemini http|anthropic http|rate.?limit|quota|api key|authentication`), failureProvider},
	{regexp.MustCompile(`(?i)graphjin|execute_graphql|data source|unauthorized.*table`), failureData},
}

// profileBuildFailureMessage uses the stored job error only to pick the
// text. The error can hold paths or provider detail, so it never reaches the
// browser.
func profileBuildFailureMessage(stored *string) string {
	if stored == nil || *stored == "" {
		return failureGeneric
	}
	for _, c := range failureClasses {
		if c.re.MatchString(*stored) {
			return c.message
		}
	}
	return failureGeneric
}

const stageKinds = `('business_profile_build', 'industry_insights_build', 'bootstrap_metrics_build', 'metric_refresh')`

func (a *API) currentStage(ctx context.Context, orgID string) (*onboardingStage, error) {
	var s onboardingStage
	err := a.DB.QueryRow(ctx, `select kind,
		  case when jsonb_typeof(progress->'message') = 'string' then progress->>'message' end
		from processing_job
		where org_id = $1 and status in ('queued', 'running') and kind in `+stageKinds+`
		order by created_at desc limit 1`, orgID).Scan(&s.Kind, &s.Message)
	if err != nil {
		return nil, err
	}
	if s.Message != nil && jsTrim(*s.Message) == "" {
		s.Message = nil
	}
	return &s, nil
}

// metricsProgress counts the metric_refresh jobs since the latest
// bootstrap_metrics_build of the org.
func (a *API) metricsProgress(ctx context.Context, orgID string) (*metricsProgress, error) {
	var p metricsProgress
	err := a.DB.QueryRow(ctx, `
		with since as (
		  select created_at from processing_job
		  where org_id = $1 and kind = 'bootstrap_metrics_build'
		  order by created_at desc limit 1
		)
		select count(*) filter (where j.status in ('queued', 'running', 'succeeded', 'failed', 'cancelled'))::int,
		  count(*) filter (where j.status = 'succeeded')::int,
		  count(*) filter (where j.status = 'failed')::int
		from since join processing_job j on j.org_id = $1 and j.kind = 'metric_refresh' and j.created_at >= since.created_at`,
		orgID).Scan(&p.Total, &p.Completed, &p.Failed)
	if err != nil || p.Total == 0 {
		return nil, err
	}
	return &p, nil
}

func noRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func (a *API) onboardingStatus(w http.ResponseWriter, r *http.Request) {
	if demoMode() {
		zero := 0
		sendReady(w, onboardingStatus{State: "ready", ProfileVersion: &zero, Seats: []string{"CEO", "CFO", "CRO"}, Mode: "shared"})
		return
	}
	ctx := r.Context()
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	var (
		profileVersion     *int
		inflightID         *string
		seats              []string
		lastID, lastStatus *string
		lastError          *string
	)
	err = noRows(a.DB.QueryRow(ctx, `select version from customer_profile where org_id = $1 and is_current limit 1`, orgID).Scan(&profileVersion))
	if err == nil {
		err = noRows(a.DB.QueryRow(ctx, `select id::text from processing_job
			where org_id = $1 and kind = 'business_profile_build' and status in ('queued', 'running')
			order by created_at desc limit 1`, orgID).Scan(&inflightID))
	}
	if err == nil {
		err = noRows(a.DB.QueryRow(ctx, `select active_seats from onboarding_wizard where org_id = $1 limit 1`, orgID).Scan(&seats))
	}
	if err == nil {
		err = noRows(a.DB.QueryRow(ctx, `select id::text, status, error from processing_job
			where org_id = $1 and kind = 'business_profile_build'
			order by created_at desc limit 1`, orgID).Scan(&lastID, &lastStatus, &lastError))
	}
	if err != nil {
		sendJSON(w, http.StatusServiceUnavailable, onboardingStatus{State: "db_error", Message: err.Error()})
		return
	}

	switch {
	case profileVersion != nil:
		var personaUser string
		if a.Gate.Status(ctx).Live() {
			if u, err := a.currentUser(ctx, r, orgID); err == nil && u != nil {
				personaUser = u.ID
			}
		}
		status := onboardingStatus{State: "ready", ProfileVersion: profileVersion, Mode: "shared", Seats: seats}
		if personaUser != "" {
			var role string
			err := a.DB.QueryRow(ctx, `select role_template from operator_profile where org_id = $1 and user_id = $2 limit 1`,
				orgID, personaUser).Scan(&role)
			if noRows(err) != nil {
				a.fail(w, r, err)
				return
			}
			if jsTrim(role) == "" {
				sendJSON(w, http.StatusOK, onboardingStatus{State: "needs_persona"})
				return
			}
			status.Mode, status.Seats = "personal", nil
		}
		if status.Seats == nil {
			status.Seats = []string{}
		}
		status.MetricsProgress, _ = a.metricsProgress(ctx, orgID)
		sendReady(w, status)
	case inflightID != nil:
		status := onboardingStatus{State: "processing", JobID: *inflightID}
		status.CurrentStage, _ = a.currentStage(ctx, orgID)
		status.MetricsProgress, _ = a.metricsProgress(ctx, orgID)
		sendJSON(w, http.StatusOK, status)
	case lastStatus != nil && *lastStatus == "failed":
		sendJSON(w, http.StatusOK, onboardingStatus{State: "failed", JobID: *lastID, Message: profileBuildFailureMessage(lastError)})
	default:
		sendJSON(w, http.StatusOK, onboardingStatus{State: "needs_wizard"})
	}
}

func sendReady(w http.ResponseWriter, s onboardingStatus) {
	sendJSON(w, http.StatusOK, struct {
		State           string           `json:"state"`
		ProfileVersion  int              `json:"profileVersion"`
		Seats           []string         `json:"seats"`
		Mode            string           `json:"mode"`
		MetricsProgress *metricsProgress `json:"metricsProgress,omitempty"`
	}{s.State, *s.ProfileVersion, s.Seats, s.Mode, s.MetricsProgress})
}
