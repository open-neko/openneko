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
// the web app reads: the usage of the run's last outer usage event.
type runTelemetry struct {
	mu    sync.Mutex
	usage *jsObj
}

func (t *runTelemetry) wrap(emit func(context.Context, any) error) func(context.Context, any) error {
	return func(ctx context.Context, event any) error {
		if jsGet(event, "type") == "usage" && jsGet(event, "source") == "outer" {
			u, _ := jsGet(event, "usage").(*jsObj)
			t.mu.Lock()
			t.usage = u
			t.mu.Unlock()
		}
		return emit(ctx, event)
	}
}

func (t *runTelemetry) summary(runID string) *jsObj {
	t.mu.Lock()
	m := t.usage
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
	return jsObjOf("schemaVersion", runSummarySchema, "runId", runID, "usage", usage)
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
