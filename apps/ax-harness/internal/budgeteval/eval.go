// Package budgeteval scores a fixed-budget run against its journaled shadow
// recommendation. It does not change admission or infer task quality from text.
package budgeteval

import (
	"encoding/json"
	"fmt"

	"github.com/open-neko/openneko/apps/ax-harness/internal/budgettriage"
	"github.com/open-neko/openneko/apps/ax-harness/internal/session"
)

const missingModelTokens int64 = 4096
const graphJinReservation int64 = 12 * missingModelTokens

// Label must come from an outcome verifier independent of the classifier and
// agent's final wording. An unverified run can be inspected but never counted
// as a successful task or used to authorize a canary.
type Label struct {
	TaskClass string `json:"task_class"` // short, investigation, artifact
	Outcome   string `json:"outcome"`    // verified_success, verified_failure, unverified
	WallMS    int64  `json:"wall_ms"`
}

func (l Label) Valid() bool {
	if l.TaskClass != "short" && l.TaskClass != "investigation" && l.TaskClass != "artifact" {
		return false
	}
	return (l.Outcome == "verified_success" || l.Outcome == "verified_failure" || l.Outcome == "unverified") && l.WallMS >= 0
}

type Block struct {
	Sequence uint64 `json:"sequence"`
	Kind     string `json:"kind"` // model_calls, model_tokens, remote_tokens, cost, observed_overspend
}

type Report struct {
	RunID                  string `json:"run_id"`
	Label                  Label  `json:"label"`
	RunStatus              string `json:"run_status"`
	InitialProfile         string `json:"initial_profile"`
	FinalProfile           string `json:"final_profile"`
	ClassifierCostMicros   int64  `json:"classifier_cost_micros"`
	ClassifierLatencyMS    int64  `json:"classifier_latency_ms"`
	ActualModelCalls       int    `json:"actual_model_calls"`
	ActualChargedTokens    int64  `json:"actual_charged_tokens"`
	ActualCostMicros       int64  `json:"actual_cost_micros"`
	UsageCoverage          string `json:"usage_coverage"`
	ClassFalseLow          bool   `json:"class_false_low"`
	PrematureBudgetFailure bool   `json:"premature_budget_failure"`
	FirstBlock             *Block `json:"first_block,omitempty"`
}

func chargedTokens(total int64, requests, reported int, remote int64) int64 {
	missing := requests - reported
	if missing < 0 {
		missing = 0
	}
	return total + int64(missing)*missingModelTokens + remote
}

func firstBlock(report *Report, sequence uint64, kind string) {
	if report.FirstBlock == nil {
		report.FirstBlock = &Block{Sequence: sequence, Kind: kind}
	}
}

// Evaluate replays budget metadata in event order. It reports the first call
// that the proposed profile would have blocked, while continuing to measure
// the actual fixed-budget run. This is a safety counterfactual, not a claim
// that changing a budget would preserve model behavior or reduce real cost.
func Evaluate(trace session.BudgetTrace, label Label) (Report, error) {
	if !label.Valid() || trace.RunID == "" || !trace.Hard.Valid() || trace.Status == "" {
		return Report{}, fmt.Errorf("invalid budget evaluation input")
	}
	if trace.Mode != "" && trace.Mode != "fixed" {
		return Report{}, fmt.Errorf("shadow counterfactual requires a fixed-budget run")
	}
	if label.Outcome == "verified_success" && trace.Status != "completed" {
		return Report{}, fmt.Errorf("verified success requires a completed run")
	}
	report := Report{RunID: trace.RunID, Label: label, RunStatus: trace.Status, UsageCoverage: "complete"}
	var profile *budgettriage.Proposal
	var modelTotal, remoteTokens, cost int64
	modelCalls, modelFinished, reported := 0, 0, 0
	remoteStarted, remoteFinished := 0, 0
	maxReportedCallTokens := missingModelTokens
	modelCost := map[uint64]int64{}
	remoteCost := map[uint64]int64{}
	lastSequence := uint64(0)
	for _, e := range trace.Events {
		if e.Sequence <= lastSequence || e.Type == "" {
			return Report{}, fmt.Errorf("unordered budget trace")
		}
		lastSequence = e.Sequence
		switch e.Type {
		case "budget.triage.skipped":
			if profile != nil {
				return Report{}, fmt.Errorf("triage skipped after a shadow proposal")
			}
			profile = &budgettriage.Proposal{Profile: "fixed", Limits: trace.Hard}
			report.InitialProfile, report.FinalProfile = "fixed", "fixed"
		case "budget.profile.proposed":
			var proposal budgettriage.Proposal
			if profile != nil || json.Unmarshal(e.Data, &proposal) != nil || !proposal.Valid(trace.Hard) {
				return Report{}, fmt.Errorf("invalid shadow proposal")
			}
			profile = &proposal
			report.InitialProfile = proposal.Profile
			report.FinalProfile = proposal.Profile
		case "budget.profile.extended":
			var extension budgettriage.Extension
			if profile == nil || json.Unmarshal(e.Data, &extension) != nil || !extension.Valid(*profile, trace.Hard) ||
				e.OperationID != extension.OperationID || e.CallID != extension.CallID {
				return Report{}, fmt.Errorf("invalid shadow extension")
			}
			profile = &budgettriage.Proposal{Version: extension.Version, Profile: extension.To, Limits: extension.Limits}
			report.FinalProfile = extension.To
		case "model.request.started":
			reservation := maxReportedCallTokens
			if profile != nil {
				if modelCalls+1 > profile.Limits.MaxModelCalls {
					firstBlock(&report, e.Sequence, "model_calls")
				}
				if reservation > profile.Limits.MaxModelTokens {
					reservation = profile.Limits.MaxModelTokens
				}
				if chargedTokens(modelTotal, modelCalls, reported, remoteTokens)+reservation > profile.Limits.MaxModelTokens {
					firstBlock(&report, e.Sequence, "model_tokens")
				}
				if e.CostMicros == nil {
					return Report{}, fmt.Errorf("missing priced model reservation")
				}
				if cost+*e.CostMicros > profile.Limits.MaxCostMicros {
					firstBlock(&report, e.Sequence, "cost")
				}
			}
			modelCalls++
			if e.CostMicros != nil {
				modelCost[e.CallID] = *e.CostMicros
				cost += *e.CostMicros
				if e.Stage == "budget_triage" {
					report.ClassifierCostMicros = *e.CostMicros
				}
			}
		case "model.request.finished":
			modelFinished++
			if e.Usage == nil {
				report.UsageCoverage = "partial"
			} else {
				reported++
				modelTotal += e.Usage.TotalTokens
				if e.Usage.TotalTokens > maxReportedCallTokens {
					maxReportedCallTokens = e.Usage.TotalTokens
				}
			}
			if e.CostMicros != nil {
				cost += *e.CostMicros - modelCost[e.CallID]
				if e.Stage == "budget_triage" {
					report.ClassifierCostMicros = *e.CostMicros
					report.ClassifierLatencyMS = e.DurationMS
				}
			}
			if profile != nil && chargedTokens(modelTotal, modelCalls, reported, remoteTokens) > profile.Limits.MaxModelTokens {
				firstBlock(&report, e.Sequence, "observed_overspend")
			}
		case "tool.started":
			if e.Name != "lookup" {
				continue
			}
			remoteStarted++
			if profile != nil {
				if chargedTokens(modelTotal, modelCalls, reported, remoteTokens)+graphJinReservation > profile.Limits.MaxModelTokens {
					firstBlock(&report, e.Sequence, "remote_tokens")
				}
				if e.CostMicros == nil {
					return Report{}, fmt.Errorf("missing priced remote reservation")
				}
				if cost+*e.CostMicros > profile.Limits.MaxCostMicros {
					firstBlock(&report, e.Sequence, "cost")
				}
			}
			remoteTokens += graphJinReservation
			if e.CostMicros != nil {
				remoteCost[e.OperationID] = *e.CostMicros
				cost += *e.CostMicros
			}
		case "tool.finished":
			if e.Name != "lookup" {
				continue
			}
			remoteFinished++
			if e.RemoteUsage == nil {
				report.UsageCoverage = "partial"
			} else {
				remoteTokens += e.RemoteUsage.ChargedTokens - graphJinReservation
				if !e.RemoteUsage.Reported {
					report.UsageCoverage = "partial"
				}
			}
			if e.CostMicros != nil {
				cost += *e.CostMicros - remoteCost[e.OperationID]
			}
			if profile != nil && chargedTokens(modelTotal, modelCalls, reported, remoteTokens) > profile.Limits.MaxModelTokens {
				firstBlock(&report, e.Sequence, "observed_overspend")
			}
		}
	}
	if report.InitialProfile == "" {
		return Report{}, fmt.Errorf("budget trace has no shadow proposal")
	}
	if modelFinished != modelCalls || remoteFinished != remoteStarted {
		report.UsageCoverage = "partial"
	}
	report.ActualModelCalls = modelCalls
	report.ActualChargedTokens = chargedTokens(modelTotal, modelCalls, reported, remoteTokens)
	report.ActualCostMicros = cost
	report.ClassFalseLow = label.Outcome != "unverified" && profileRank(report.InitialProfile) < profileRank(label.TaskClass)
	report.PrematureBudgetFailure = label.Outcome == "verified_success" && report.FirstBlock != nil
	return report, nil
}

func profileRank(profile string) int {
	switch profile {
	case "short":
		return 1
	case "multi_step", "investigation":
		return 2
	case "artifact":
		return 3
	default: // fixed is an abstention, not a low prediction.
		return 4
	}
}
