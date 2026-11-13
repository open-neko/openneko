package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

// turnUsagePrice is priceUsage() in packages/llm/src/spend/ledger.ts: the
// billed cost, then the estimate; a usage with no price costs the run cap.
func turnUsagePrice(usage any, capMicros float64) (float64, string) {
	if jsGet(usage, "costStatus") != "unknown" {
		if billed, ok := jsGet(usage, "billedCostUsd").(float64); ok && !math.IsInf(billed, 0) && !math.IsNaN(billed) {
			return usdToMicros(billed), "billed"
		}
		if estimated, ok := jsGet(usage, "estimatedCostUsd").(float64); ok && !math.IsInf(estimated, 0) && !math.IsNaN(estimated) {
			if jsGet(usage, "costStatus") == "included" {
				return usdToMicros(estimated), "included"
			}
			return usdToMicros(estimated), "estimated"
		}
	}
	return capMicros, "fallback"
}

// recordTurnSpend is recordUsageSpend() with task "turn" for the usage of
// a run's turn. A failed alert is logged only.
func (a *API) recordTurnSpend(ctx context.Context, orgID, runID string, event any, now time.Time) error {
	var reservationID, source, workflowID *string
	if err := noRows(a.DB.QueryRow(ctx, `select id::text, source, workflow_id::text from spend_reservation where work_run_id::text = $1`, runID).
		Scan(&reservationID, &source, &workflowID)); err != nil {
		return err
	}
	runCap := usdToMicros(5)
	limits, err := spendLimits(ctx, a.DB, orgID, "")
	switch {
	case err == nil:
		runCap = limits[0]
	case !strings.HasPrefix(err.Error(), "No spend limits"):
		return err
	}
	usage := jsGet(event, "usage")
	micros, priced := turnUsagePrice(usage, runCap)
	var tokens any
	if total, ok := jsGet(usage, "totalTokens").(float64); ok && !math.IsInf(total, 0) && !math.IsNaN(total) {
		tokens = int64(math.Ceil(total))
	}
	src := "system"
	if source != nil {
		src = *source
	}
	provider, _ := jsGet(event, "provider").(string)
	model, _ := jsGet(event, "model").(string)
	costSource, _ := jsGet(usage, "costSource").(string)
	pricing, _ := jsGet(usage, "pricingCatalogVersion").(string)
	if _, err := a.DB.Exec(ctx, `insert into spend_ledger (
	     org_id, reservation_id, work_run_id, workflow_id, source, provider, model,
	     cost_micros, tokens, priced, cost_source, pricing_version, created_at, task
	   ) values ($1, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'turn')`,
		orgID, reservationID, runID, workflowID, src, nullable(provider), nullable(model),
		int64(micros), tokens, priced, nullable(costSource), nullable(pricing), now); err != nil {
		return err
	}
	if priced == "fallback" {
		if provider == "" {
			provider = "unknown"
		}
		if model == "" {
			model = "unknown"
		}
		name := provider + "/" + model
		day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
		details, err := json.Marshal(struct {
			Message        string  `json:"message"`
			WindowStart    string  `json:"windowStart"`
			ObservedMicros float64 `json:"observedMicros"`
			ThresholdMicro float64 `json:"thresholdMicros"`
		}{fmt.Sprintf("OpenNeko has no price for %s. Each turn on it is charged the $%s per-run cap until a price is added.", name, jsToFixed2(microsToUSD(micros))),
			jsDate(day), micros, micros})
		if err != nil {
			log.Printf("[spend] alert check failed: %v", err)
		} else {
			a.raiseSpendAlert(ctx, orgID, "spend.price_unknown", "model:"+name, micros, micros, 86_400, jsDate(day), details)
		}
	}
	wf := ""
	if workflowID != nil {
		wf = *workflowID
	}
	if err := a.checkSpendWarnings(ctx, orgID, wf, now); err != nil {
		log.Printf("[spend] alert check failed: %v", err)
	}
	return nil
}
