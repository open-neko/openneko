package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

type externalEventMatch struct {
	Name      string
	Source    *string
	Payload   any
	DedupeKey string
}

type externalEventSubscription struct {
	ID                     string
	OrgID                  string
	WorkflowID             string
	Filter                 any
	MaxConcurrentRuns      int
	IdempotencyKeyTemplate *string
}

type externalEventDecision struct {
	SubscriptionID string  `json:"subscriptionId"`
	Action         string  `json:"action"`
	ObservationID  string  `json:"observationId,omitempty"`
	JobID          *string `json:"jobId,omitempty"`
	Reason         string  `json:"reason,omitempty"`
}

type externalEventDispatchResult struct {
	Matched   int                     `json:"matched"`
	Enqueued  int                     `json:"enqueued"`
	Decisions []externalEventDecision `json:"decisions"`
}

// externalEventFilterMatches is externalEventFilterMatches() in
// packages/llm/src/workflows/external-events.ts.
func externalEventFilterMatches(filter any, event externalEventMatch) bool {
	var rawNames any
	var rawSource any
	switch f := filter.(type) {
	case *jsObj:
		if f != nil {
			rawNames = f.vals["event_names"]
			rawSource = f.vals["source"]
		}
	case map[string]any:
		rawNames = f["event_names"]
		rawSource = f["source"]
	}
	var names []string
	if list, ok := rawNames.([]any); ok {
		for _, item := range list {
			if s, ok := item.(string); ok {
				names = append(names, s)
			}
		}
	} else if list, ok := rawNames.([]string); ok {
		names = list
	}
	if len(names) > 0 && !slices.Contains(names, "*") && !slices.Contains(names, event.Name) {
		return false
	}
	if wantSource, ok := rawSource.(string); ok && len(wantSource) > 0 {
		if event.Source == nil || *event.Source != wantSource {
			return false
		}
	}
	return true
}

func buildExternalEventIdempotencyKey(sub externalEventSubscription, event externalEventMatch) string {
	eventKey := event.DedupeKey
	if eventKey == "" {
		src := ""
		if event.Source != nil {
			src = *event.Source
		}
		payloadText := jsStringify(event.Payload, false)
		sum := sha256.Sum256([]byte(event.Name + "\x00" + src + "\x00" + payloadText))
		eventKey = hex.EncodeToString(sum[:])[:16]
	}
	if sub.IdempotencyKeyTemplate != nil && *sub.IdempotencyKeyTemplate != "" {
		out := strings.Replace(*sub.IdempotencyKeyTemplate, "{subscription_id}", sub.ID, 1)
		out = strings.Replace(out, "{source_record_id}", eventKey, 1)
		out = strings.Replace(out, "{source_version}", "none", 1)
		return out
	}
	return sub.ID + ":" + eventKey
}

func truncateUTF16Units(s string, maxUnits int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= maxUnits {
		return s
	}
	return string(utf16.Decode(units[:maxUnits]))
}

func (a *API) handleExternalEventMatch(ctx context.Context, sub externalEventSubscription, event externalEventMatch) (externalEventDecision, error) {
	var inFlight int
	if err := a.DB.QueryRow(ctx, `
		select count(*)::int from workflow_run
		where triggered_by_subscription_id = $1 and status = 'running'`, sub.ID).Scan(&inFlight); err != nil {
		return externalEventDecision{}, err
	}
	if inFlight >= sub.MaxConcurrentRuns {
		return externalEventDecision{
			SubscriptionID: sub.ID,
			Action:         "dropped",
			Reason:         fmt.Sprintf("subscription %s at max_concurrent_runs (%d/%d)", sub.ID, inFlight, sub.MaxConcurrentRuns),
		}, nil
	}

	var dailyBudget *int
	err := a.DB.QueryRow(ctx, `
		select daily_run_budget from workflow_definition
		where org_id = $1 and id = $2`, sub.OrgID, sub.WorkflowID).Scan(&dailyBudget)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return externalEventDecision{}, err
	}
	if dailyBudget != nil {
		startOfToday := time.Now().UTC().Truncate(24 * time.Hour)
		var ranToday int
		if err := a.DB.QueryRow(ctx, `
			select count(*)::int from workflow_run
			where org_id = $1 and workflow_id = $2 and created_at >= $3`,
			sub.OrgID, sub.WorkflowID, startOfToday).Scan(&ranToday); err != nil {
			return externalEventDecision{}, err
		}
		if ranToday >= *dailyBudget {
			return externalEventDecision{
				SubscriptionID: sub.ID,
				Action:         "dropped",
				Reason:         fmt.Sprintf("consumer workflow %s reached daily_run_budget (%d/%d)", sub.WorkflowID, ranToday, *dailyBudget),
			}, nil
		}
	}

	sourceLabel := "external"
	if event.Source != nil {
		sourceLabel = *event.Source
	}
	title := fmt.Sprintf("%s: %s", sourceLabel, event.Name)
	body := truncateUTF16Units(jsStringify(event.Payload, false), 4000)

	var obsID string
	if err := a.DB.QueryRow(ctx, `
		insert into observation (org_id, source_output_id, consumer_kind, consumer_workflow_id, subscription_id, title, body, mood)
		values ($1, null, 'workflow', $2, $3, $4, $5, null)
		returning id::text`,
		sub.OrgID, sub.WorkflowID, sub.ID, title, body).Scan(&obsID); err != nil {
		return externalEventDecision{}, err
	}

	dedupeKey := buildExternalEventIdempotencyKey(sub, event)
	var jobIDPtr *string
	if a.Jobs != nil {
		var eventSource any
		if event.Source != nil {
			eventSource = *event.Source
		}
		payloadRaw := []byte(jsStringify(obj(
			"subscription_id", sub.ID,
			"observation_id", obsID,
			"event_name", event.Name,
			"event_source", eventSource,
			"payload", event.Payload,
		), false))
		var jid string
		err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
			var err error
			jid, err = a.enqueueWorkflowRunFire(ctx, tx, jobs.WorkflowRunFireArgs{
				OrgID:                     sub.OrgID,
				WorkflowID:                sub.WorkflowID,
				TriggerKind:               "subscription",
				TriggerPayload:            payloadRaw,
				TriggeredBySubscriptionID: sub.ID,
				TriggeredByObservationID:  obsID,
			}, dedupeKey, time.Hour)
			return err
		})
		if err != nil {
			return externalEventDecision{}, err
		}
		if jid != "" {
			jobIDPtr = &jid
		}
	}

	return externalEventDecision{
		SubscriptionID: sub.ID,
		Action:         "enqueued",
		ObservationID:  obsID,
		JobID:          jobIDPtr,
	}, nil
}

// dispatchExternalEvent is dispatchExternalEvent() in
// packages/llm/src/workflows/external-events.ts.
func (a *API) dispatchExternalEvent(ctx context.Context, orgID string, event externalEventMatch) (externalEventDispatchResult, error) {
	if a == nil || a.DB == nil {
		return externalEventDispatchResult{Decisions: []externalEventDecision{}}, nil
	}
	rows, err := a.DB.Query(ctx, `
		select id::text, org_id, workflow_id::text, filter::text, max_concurrent_runs, idempotency_key_template
		from subscription
		where enabled = true and source_kind = 'external_event' and org_id = $1
		order by created_at asc, id asc`, orgID)
	if err != nil {
		return externalEventDispatchResult{}, err
	}
	var subs []externalEventSubscription
	for rows.Next() {
		var sub externalEventSubscription
		var filterText string
		if err := rows.Scan(&sub.ID, &sub.OrgID, &sub.WorkflowID, &filterText, &sub.MaxConcurrentRuns, &sub.IdempotencyKeyTemplate); err != nil {
			rows.Close()
			return externalEventDispatchResult{}, err
		}
		sub.Filter, _ = jsParse([]byte(filterText))
		if externalEventFilterMatches(sub.Filter, event) {
			subs = append(subs, sub)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return externalEventDispatchResult{}, err
	}

	decisions := make([]externalEventDecision, 0, len(subs))
	enqueued := 0
	for _, sub := range subs {
		d, err := a.handleExternalEventMatch(ctx, sub, event)
		if err != nil {
			return externalEventDispatchResult{}, err
		}
		if d.Action == "enqueued" {
			enqueued++
		}
		decisions = append(decisions, d)
	}
	return externalEventDispatchResult{
		Matched:   len(subs),
		Enqueued:  enqueued,
		Decisions: decisions,
	}, nil
}
