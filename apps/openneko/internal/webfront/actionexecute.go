package webfront

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (a *API) enqueueActionExecute(ctx context.Context, tx pgx.Tx, orgID, id string) error {
	if a.Jobs == nil {
		return errors.New("the job queue is not ready")
	}
	return a.Jobs.ActionExecute(ctx, tx, orgID, id)
}

// ActionExecute is the action_execute job. All action adapters run in Go.
func (a *API) ActionExecute(ctx context.Context, orgID, actionRequestID string) error {
	run, err := a.goActionAdapter(ctx, orgID, actionRequestID)
	if err != nil {
		return err
	}
	return a.executeActionRequest(ctx, orgID, actionRequestID, run)
}
