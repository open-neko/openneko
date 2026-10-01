package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/open-neko/harness/internal/budgettriage"
)

// runBudgetTriage is shadow-only: it charges one optional Typesafe request but
// never changes the accepted hard caps or grants a capability. Leave room for
// the ordinary Ax context/executor/responder calls before spending on triage.
func runBudgetTriage(ctx context.Context, r *recorder, task *BudgetTriage, input budgettriage.Input) error {
	price := r.pricing.Prices[task.Route]
	reserve := price.Reservation(512)
	r.mu.Lock()
	reason := ""
	switch {
	case r.modelCalls+4 > r.spec.ModelCallLimit():
		reason = "call_budget"
	case r.spec.MaxModelTokens > 0 && r.chargedTokens()+missingModelUsageCharge+r.nextModelReservation() > r.spec.MaxModelTokens:
		reason = "token_budget"
	default:
		mainReserve := mainRouteReservation(r.pricing, task.Route, r.nextModelReservation())
		if r.costMicros+reserve+mainReserve > r.spec.MaxCostMicros {
			reason = "cost_budget"
		}
	}
	remaining := r.spec.MaxCostMicros - r.costMicros
	r.mu.Unlock()
	if reason != "" {
		r.send(Event{Type: "budget.triage.skipped", Name: reason})
		if r.hasError() {
			return fmt.Errorf("budget triage skip was not journaled")
		}
		return nil
	}
	j := &triageJournal{recorder: r, task: task, reservation: reserve}
	observation, err := budgettriage.Evaluate(ctx, task.Client, task.Model, input, price, remaining, j)
	if err != nil {
		return err
	}
	if observation.Reason == "budget_denied" {
		r.send(Event{Type: "budget.triage.skipped", Name: "cost_budget"})
	} else if err := proposeBudgetProfile(r, task.Policy, observation); err != nil {
		return err
	}
	if r.hasError() {
		return fmt.Errorf("budget triage event was not journaled")
	}
	return nil
}

func proposeBudgetProfile(r *recorder, policy budgettriage.Policy, observation budgettriage.Observation) error {
	proposal, ok := policy.Propose(observation, r.spec.triageHardLimits())
	if !ok {
		return fmt.Errorf("invalid budget triage proposal")
	}
	data, err := json.Marshal(proposal)
	if err != nil {
		return err
	}
	r.send(Event{Type: "budget.profile.proposed", Name: proposal.Profile, Data: data})
	if r.hasError() {
		return fmt.Errorf("budget triage proposal was not journaled")
	}
	r.mu.Lock()
	r.shadowProfile = &proposal
	r.mu.Unlock()
	return nil
}

// Reconsider a shadow allowance only after a successful, durable operation.
// The actual run still uses its original hard caps. A single operation may
// justify at most one step up the ordered policy ladder.
func (r *recorder) maybeExtendBudgetProfile(policy budgettriage.Policy, triageRoute string, operationID uint64) {
	r.shadowMu.Lock()
	defer r.shadowMu.Unlock()
	r.mu.Lock()
	current := r.shadowProfile
	if current == nil || operationID <= r.shadowLastExtensionOp || r.err != nil {
		r.mu.Unlock()
		return
	}
	nextTokens := r.nextModelReservation()
	mainCostReserve := mainRouteReservation(r.pricing, triageRoute, nextTokens)
	due := r.modelCalls+1 > current.Limits.MaxModelCalls ||
		r.chargedTokens()+nextTokens > current.Limits.MaxModelTokens ||
		r.costMicros+mainCostReserve > current.Limits.MaxCostMicros
	r.mu.Unlock()
	if !due {
		return
	}
	extension, ok := policy.Extend(*current, r.spec.triageHardLimits(), operationID)
	if !ok {
		return
	}
	data, _ := json.Marshal(extension)
	r.send(Event{Type: "budget.profile.extended", Name: extension.To, OperationID: operationID, Data: data})
	if r.hasError() {
		return
	}
	updated := budgettriage.Proposal{Version: extension.Version, Profile: extension.To, Limits: extension.Limits}
	r.mu.Lock()
	r.shadowProfile = &updated
	r.shadowLastExtensionOp = operationID
	r.mu.Unlock()
}

// A GraphJin request is known after Ax has durably finished the model call,
// but before the remote reservation is admitted. Walk the pinned shadow ladder
// one tier per event until that specific lookup would fit or the hard cap wins.
func (r *recorder) maybeExtendForRemoteLookup(policy budgettriage.Policy, callID uint64) {
	r.shadowMu.Lock()
	defer r.shadowMu.Unlock()
	for step := 0; step < 3; step++ {
		r.mu.Lock()
		current := r.shadowProfile
		if current == nil || r.err != nil || r.pricing == nil || r.pricing.GraphJinPrice == nil {
			r.mu.Unlock()
			return
		}
		due := r.chargedTokens()+remoteLookupReservation > current.Limits.MaxModelTokens ||
			r.costMicros+r.pricing.GraphJinPrice.Reservation(remoteLookupReservation) > current.Limits.MaxCostMicros
		r.mu.Unlock()
		if !due {
			return
		}
		extension, ok := policy.ExtendForRemote(*current, r.spec.triageHardLimits(), callID)
		if !ok {
			return
		}
		data, _ := json.Marshal(extension)
		r.send(Event{Type: "budget.profile.extended", Name: extension.To, CallID: callID, Data: data})
		if r.hasError() {
			return
		}
		updated := budgettriage.Proposal{Version: extension.Version, Profile: extension.To, Limits: extension.Limits}
		r.mu.Lock()
		r.shadowProfile = &updated
		r.mu.Unlock()
	}
}

// The classifier is a one-time request charged before the proposal. Future
// context, executor and responder calls can use any other approved route.
func mainRouteReservation(pricing *RoutedClient, triageRoute string, tokens int64) int64 {
	reserve := int64(0)
	for route, price := range pricing.Prices {
		if route != triageRoute {
			if charge := price.Reservation(tokens); charge > reserve {
				reserve = charge
			}
		}
	}
	return reserve
}

type triageJournal struct {
	recorder    *recorder
	task        *BudgetTriage
	reservation int64
	callID      uint64
	startedAt   time.Time
}

func (j *triageJournal) Reserve(ctx context.Context, observation budgettriage.Observation) error {
	r := j.recorder
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil || r.err != nil || j.callID != 0 || observation.Version != budgettriage.Version ||
		observation.RequestedModel != j.task.Model || observation.ChargedMicros != j.reservation ||
		r.modelCalls >= r.spec.ModelCallLimit() || r.costMicros+j.reservation > r.spec.MaxCostMicros ||
		r.spec.MaxModelTokens > 0 && r.chargedTokens()+missingModelUsageCharge > r.spec.MaxModelTokens {
		return fmt.Errorf("budget triage reservation denied")
	}
	id := uint64(r.modelCalls + 1)
	e := Event{Version: 1, RunID: r.spec.RunID, InputID: r.spec.InputID, Sequence: r.seq + 1,
		Type: "model.request.started", CallID: id, Name: j.task.Model, Origin: j.task.Route,
		Stage: "budget_triage", CostMicros: &j.reservation}
	if err := r.emit(e); err != nil {
		r.err = err
		r.cancel()
		return err
	}
	r.seq++
	r.modelCalls++
	r.usage.Requests++
	r.costMicros += j.reservation
	j.callID = id
	j.startedAt = time.Now()
	return nil
}

func (j *triageJournal) Settle(ctx context.Context, observation budgettriage.Observation) error {
	r := j.recorder
	if j.callID == 0 || !observation.Valid() ||
		observation.RequestedModel != j.task.Model || observation.ChargedMicros < j.reservation ||
		observation.ChargedMicros > 8_000_000_000_000_000 {
		return fmt.Errorf("invalid budget triage settlement")
	}
	data, err := json.Marshal(observation)
	if err != nil || len(data) > 4096 {
		return fmt.Errorf("invalid budget triage observation")
	}
	finished := Event{Type: "model.request.finished", CallID: j.callID, Name: j.task.Model,
		Origin: j.task.Route, Stage: "budget_triage", DurationMS: time.Since(j.startedAt).Milliseconds(),
		CostMicros: &observation.ChargedMicros, Data: data}
	if observation.Coverage == "complete" {
		finished.Usage = &ModelUsage{Reported: 1, InputTokens: observation.InputTokens,
			OutputTokens: observation.OutputTokens, TotalTokens: observation.InputTokens + observation.OutputTokens}
	}
	if observation.Reason == "classifier_unavailable" {
		finished.Error = "model_request_failed"
	}
	r.send(finished)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.costMicros += observation.ChargedMicros - j.reservation
	if r.costMicros > r.spec.MaxCostMicros {
		r.costOverspent = true
	}
	if finished.Usage != nil {
		r.usage.AddReported(*finished.Usage)
		if finished.Usage.TotalTokens > r.maxReportedCallTokens {
			r.maxReportedCallTokens = finished.Usage.TotalTokens
		}
	}
	if r.spec.MaxModelTokens > 0 && r.chargedTokens() > r.spec.MaxModelTokens {
		r.modelTokenOverspent = true
	}
	return nil
}
