package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

func (a *API) enqueueActionExecute(ctx context.Context, tx pgx.Tx, orgID, id string) error {
	if a.Jobs == nil {
		return errors.New("the job queue is not ready")
	}
	return a.Jobs.ActionExecute(ctx, tx, orgID, id)
}

// ActionExecute is the action_execute job. The worker runs the action's
// adapter; any answer but 200 fails the attempt, and River tries again.
func (a *API) ActionExecute(ctx context.Context, orgID, actionRequestID string) error {
	body, err := json.Marshal(map[string]string{"orgId": orgID, "actionRequestId": actionRequestID})
	if err != nil {
		return err
	}
	status, reply, err := a.workerDo(ctx, http.MethodPost, "/admin/actions/execute", jobs.ActionExecuteTimeout, body)
	if err != nil {
		return fmt.Errorf("action %s: the worker did not answer: %w", actionRequestID, err)
	}
	if status == http.StatusOK {
		return nil
	}
	var failed struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(reply, &failed)
	if failed.Error == "" {
		failed.Error = http.StatusText(status)
	}
	return fmt.Errorf("action %s failed (%d): %s", actionRequestID, status, failed.Error)
}
