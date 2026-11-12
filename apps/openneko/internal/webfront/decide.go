package webfront

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"
)

const unsafeRetry = "This action cannot be retried safely. Check its change set before proposing another action."

type decisionError struct {
	status  int
	message string
}

func (e *decisionError) Error() string { return e.message }

func transitionError(from, to string) error {
	return &decisionError{http.StatusConflict, "invalid action_request transition: " + from + " → " + to}
}

// actionRequestDecision answers PATCH action-requests/{id} as the Node route
// and approve/reject/retryActionRequest in packages/llm did.
func (a *API) actionRequestDecision(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, _ := readBody(r)
	decision, _ := field(body, "decision")
	if decision != "approve" && decision != "reject" && decision != "retry" {
		sendError(w, http.StatusBadRequest, "decision must be 'approve', 'reject', or 'retry'")
		return
	}
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := r.PathValue("actionRequestId")
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	req, err := scanActionRequest(a.DB.QueryRow(ctx, `select `+actionRequestColumns+` from action_request where org_id = $1 and id = $2`, act.OrgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if ok, err := a.visibleActionRequest(ctx, act, req); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			sendError(w, http.StatusNotFound, "not found")
		}
		return
	}
	var reason *string
	if v, _ := field(body, "reason"); v != nil {
		if s, ok := v.(string); ok {
			reason = &s
		}
	}
	var status, kind string
	var event auditEvent
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var current string
		var policyID *string
		if err := tx.QueryRow(ctx, `select status, kind, policy_id::text from action_request where org_id = $1 and id = $2 for update`,
			act.OrgID, id).Scan(&current, &kind, &policyID); err != nil {
			return err
		}
		switch decision {
		case "approve", "reject":
			if err := mayDecide(ctx, tx, act, id, policyID); err != nil {
				return err
			}
			status = map[string]string{"approve": "approved", "reject": "rejected"}[decision.(string)]
			if current != "draft" && current != "pending_approval" {
				return transitionError(current, status)
			}
			if _, err := tx.Exec(ctx, `update action_request set status = $3, approved_by_user_id = $4, approved_at = now(),
				rejection_reason = case when $3 = 'rejected' then $5 else rejection_reason end, updated_at = now()
				where org_id = $1 and id = $2`, act.OrgID, id, status, nullable(act.UserID), reason); err != nil {
				return err
			}
			if status == "approved" {
				event = auditEvent{Event: "approved", Payload: map[string]any{"approvedBy": nullable(act.UserID), "kind": kind, "soloOperator": false}}
			} else {
				var why any
				if reason != nil {
					why = *reason
				}
				event = auditEvent{Event: "rejected", Payload: map[string]any{"rejectedBy": nullable(act.UserID), "reason": why, "kind": kind}}
			}
		case "retry":
			status = "approved"
			if current != "failed" {
				return transitionError(current, status)
			}
			if err := mayDecide(ctx, tx, act, id, policyID); err != nil {
				return err
			}
			if err := resetFailedChangeset(ctx, tx, act.OrgID, id); err != nil {
				return err
			}
			event = auditEvent{Event: "retried", Payload: map[string]any{"retriedBy": nullable(act.UserID), "kind": kind}}
		}
		if status == "approved" {
			return a.enqueueActionExecute(ctx, tx, act.OrgID, id)
		}
		return nil
	})
	var refused *decisionError
	if errors.As(err, &refused) {
		sendError(w, refused.status, refused.message)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	event.OrgID, event.EntityKind, event.EntityID = act.OrgID, "action_request", id
	a.recordAudit(ctx, event)
	sendJSON(w, http.StatusOK, map[string]any{"actionRequest": map[string]string{"id": id, "status": status}})
}

// mayDecide is assertMayDecide() in action-store.ts. Each decision passes no
// approver role, so only a policy's approver group can refuse a member.
func mayDecide(ctx context.Context, tx pgx.Tx, act actor, id string, policyID *string) error {
	if act.Admin || policyID == nil {
		return nil
	}
	var group *string
	err := tx.QueryRow(ctx, `select approver_group_id::text from action_policy where org_id = $1 and id = $2`, act.OrgID, *policyID).Scan(&group)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && group == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	member := false
	if act.UserID != "" {
		err = tx.QueryRow(ctx, `
			select exists (select 1 from user_group g
			  join app_user u on u.id = $2 and u.org_id = g.org_id and u.disabled_at is null
			  where g.org_id = $1 and g.id = $3::uuid and (g.slug = 'everyone'
			    or exists (select 1 from user_group_membership m where m.group_id = g.id and m.user_id = u.id)))`,
			act.OrgID, act.UserID, *group).Scan(&member)
		if err != nil {
			return err
		}
	}
	if !member {
		return &decisionError{http.StatusForbidden, "action_request " + id + ": approval needs a member of the policy's approver group"}
	}
	return nil
}

// resetFailedChangeset is the safe-retry check of retryActionRequest(): one
// failed change set, never sent in bulk, whose rows all failed before any
// row started.
func resetFailedChangeset(ctx context.Context, tx pgx.Tx, orgID, id string) error {
	rows, err := tx.Query(ctx, `select id::text, status, bulk_uuid is not null from action_changeset
		where org_id = $1 and action_request_id = $2 for update`, orgID, id)
	if err != nil {
		return err
	}
	type changeset struct {
		id, status string
		bulk       bool
	}
	sets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (changeset, error) {
		var c changeset
		return c, row.Scan(&c.id, &c.status, &c.bulk)
	})
	if err != nil {
		return err
	}
	if len(sets) != 1 || sets[0].status != "failed" || sets[0].bulk {
		return &decisionError{http.StatusConflict, unsafeRetry}
	}
	rows, err = tx.Query(ctx, `select status = 'failed' and started_at is null and external_ref is null
		from action_changeset_row where changeset_id = $1 for update`, sets[0].id)
	if err != nil {
		return err
	}
	safe, err := pgx.CollectRows(rows, pgx.RowTo[bool])
	if err != nil {
		return err
	}
	if len(safe) == 0 || slices.Contains(safe, false) {
		return &decisionError{http.StatusConflict, unsafeRetry}
	}
	if _, err := tx.Exec(ctx, `update action_changeset_row set status = 'draft', error = null, finished_at = null, updated_at = now()
		where changeset_id = $1`, sets[0].id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update action_changeset set status = 'approved', executed_at = null, reconciled_at = null, updated_at = now()
		where id = $1`, sets[0].id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `update action_request set status = 'approved', failure_reason = null, updated_at = now()
		where org_id = $1 and id = $2`, orgID, id)
	return err
}

// ensureInsights answers POST insights/ensure as the Node route did.
func (a *API) ensureInsights(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, _ := readBody(r)
	force, _ := field(body, "force")
	forced := force == true
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var off bool
	if err := a.DB.QueryRow(ctx, `select `+researchOff, orgID).Scan(&off); err != nil {
		a.fail(w, r, err)
		return
	}
	if off {
		sendJSON(w, http.StatusOK, map[string]string{"state": "disabled"})
		return
	}
	if err := a.ensureQueue(ctx, queueIndustryInsights); err != nil {
		a.fail(w, r, err)
		return
	}
	var state, jobID string
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "insights:"+orgID); err != nil {
			return err
		}
		var hasInsights bool
		var live *string
		if err := tx.QueryRow(ctx, `
			select coalesce((select industry_insights is not null and industry_insights <> '' from customer_profile
			    where org_id = $1 and is_current limit 1), false),
			  (select id::text from processing_job where org_id = $1 and kind = 'industry_insights_build'
			    and status in ('queued', 'running') limit 1)`, orgID).Scan(&hasInsights, &live); err != nil {
			return err
		}
		if !forced && hasInsights {
			state = "ready"
			return nil
		}
		if live != nil {
			state, jobID = "live", *live
			return nil
		}
		if forced && hasInsights {
			if _, err := tx.Exec(ctx, `update customer_profile set industry_insights = null where org_id = $1 and is_current`, orgID); err != nil {
				return err
			}
		}
		trigger := "processing_next_ensure"
		if forced {
			trigger = "insights_regenerate"
		}
		if err := tx.QueryRow(ctx, `insert into processing_job (org_id, kind, status, trigger)
			values ($1, 'industry_insights_build', 'queued', $2) returning id::text`, orgID, trigger).Scan(&jobID); err != nil {
			return err
		}
		state = "queued"
		_, err := a.enqueue(ctx, tx, queueIndustryInsights, map[string]string{"processingJobId": jobID, "orgId": orgID})
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		State string `json:"state"`
		JobID string `json:"jobId,omitempty"`
	}{state, jobID})
}
