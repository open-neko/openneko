package webfront

import (
	"context"
	"log"
	"sync"
)

const runSummarySchema = "openneko.harness-run-summary/v1"

var runSummaryUsageKeys = []string{"inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens",
	"reasoningTokens", "totalTokens", "estimatedCostUsd", "billedCostUsd"}

// runTelemetry builds the part of HarnessRunSummary (packages/telemetry) that
// the web app reads: the usage of the run's last outer usage event, and the
// card repairs and the cards not shown.
type runTelemetry struct {
	mu            sync.Mutex
	usage         *jsObj
	batch         *jsObj
	cardRepairs   int
	cardsNotShown int
}

func (t *runTelemetry) wrap(emit func(context.Context, any) error) func(context.Context, any) error {
	return func(ctx context.Context, event any) error {
		t.mu.Lock()
		switch {
		case jsGet(event, "type") == "usage" && jsGet(event, "source") == "outer":
			t.usage, _ = jsGet(event, "usage").(*jsObj)
		case jsGet(event, "type") == "usage" && jsGet(event, "source") == cardRepairUsageSource:
			t.cardRepairs++
		case jsGet(event, "type") == "notice" && jsGet(event, "code") == "card_not_shown":
			t.cardsNotShown++
		}
		t.mu.Unlock()
		return emit(ctx, event)
	}
}

func (t *runTelemetry) summary(runID string) *jsObj {
	t.mu.Lock()
	m, repairs, notShown := t.usage, t.cardRepairs, t.cardsNotShown
	t.mu.Unlock()
	usage := jsObjOf()
	reasons := []any{}
	coverage := "unavailable"
	if m != nil {
		for _, k := range runSummaryUsageKeys {
			if v, ok := m.vals[k].(float64); ok {
				usage.set(k, v)
				coverage = "partial"
			}
		}
		if s, ok := m.vals["costStatus"].(string); ok && s != "" {
			usage.set("costStatus", s)
		}
		if coverage != "unavailable" {
			if m.vals["coverage"] == "complete" {
				coverage = "complete"
			}
			reasons = append(reasons, jsArray(m.vals["missingReasons"])...)
		}
	}
	usage.set("coverage", coverage)
	usage.set("missingReasons", reasons)
	out := jsObjOf("schemaVersion", runSummarySchema, "runId", runID)
	if t.batch != nil {
		out.set("batch", t.batch)
	}
	out.set("usage", usage)
	if repairs > 0 || notShown > 0 {
		out.set("cards", jsObjOf("repairs", float64(repairs), "notShown", float64(notShown)))
	}
	return out
}

// storeRunTelemetry adds the summary as the run's telemetry event and, for a
// workflow run, to workflow_run.telemetry_summary. A failure here is logged
// and leaves the run's outcome as it is.
func (a *API) storeRunTelemetry(ctx context.Context, store *runEventStore, t *runTelemetry, workflowRunID string) {
	summary := t.summary(store.runID)
	if err := store.emit(ctx, jsObjOf("type", "telemetry", "summary", summary)); err != nil {
		log.Printf("[run.telemetry] event for %s not stored: %v", store.runID, err)
	}
	if workflowRunID == "" {
		return
	}
	if _, err := a.DB.Exec(ctx, `update workflow_run set telemetry_summary = $2::jsonb, updated_at = now() where id = $1`,
		workflowRunID, jsStringify(summary, false)); err != nil {
		log.Printf("[run.telemetry] summary for workflow run %s not stored: %v", workflowRunID, err)
	}
}
