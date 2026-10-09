// harness-budget-compare compares independently verified, paired fixed and
// canary runs. It reads validated, stopped checkpoints and never changes limits.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/budgeteval"
	"github.com/open-neko/harness/internal/session"
)

var pairID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type runCase struct {
	Root             string               `json:"root"`
	RunID            string               `json:"run_id"`
	CheckpointSHA256 string               `json:"checkpoint_sha256"`
	Outcome          string               `json:"outcome"`
	WallMS           int64                `json:"wall_ms"`
	GraphJin         *graphJinEnvironment `json:"graphjin_environment,omitempty"`
}

// The evaluator compares reviewer-attested, non-secret deployment facts. It
// does not inspect the GraphJin process or prove the dataset digest itself.
type graphJinEnvironment struct {
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	Reasoning          string `json:"reasoning"`
	EvalFingerprint    string `json:"eval_fingerprint"`
	DataSnapshotSHA256 string `json:"data_snapshot_sha256"`
}

func (g *graphJinEnvironment) valid() bool {
	return g != nil && strings.TrimSpace(g.Provider) != "" && strings.TrimSpace(g.Model) != "" &&
		strings.TrimSpace(g.Reasoning) != "" &&
		digest.MatchString(g.EvalFingerprint) && digest.MatchString(g.DataSnapshotSHA256)
}

type pair struct {
	ID        string  `json:"id"`
	Split     string  `json:"split"`
	Source    string  `json:"source"`
	TaskClass string  `json:"task_class"`
	Fixed     runCase `json:"fixed"`
	Canary    runCase `json:"canary"`
}

type manifest struct {
	Version int    `json:"version"`
	Pairs   []pair `json:"pairs"`
}

type runReport struct {
	RunID                 string                `json:"run_id"`
	Outcome               string                `json:"outcome"`
	Status                string                `json:"status"`
	WallMS                int64                 `json:"wall_ms"`
	ModelCalls            int                   `json:"model_calls"`
	ModelRequests         []modelRequestProfile `json:"model_requests"`
	ModelInputTokens      int64                 `json:"model_input_tokens"`
	ModelOutputTokens     int64                 `json:"model_output_tokens"`
	CacheReadTokens       int64                 `json:"cache_read_tokens"`
	CacheWriteTokens      int64                 `json:"cache_write_tokens"`
	ReasoningTokens       int64                 `json:"reasoning_tokens"`
	GraphJinCalls         int                   `json:"graphjin_calls"`
	GraphJinPromptTokens  int64                 `json:"graphjin_prompt_tokens"`
	GraphJinOutputTokens  int64                 `json:"graphjin_output_tokens"`
	TriageCalls           int                   `json:"triage_calls"`
	TriageCostMicros      int64                 `json:"triage_cost_micros"`
	TriageDurationMS      int64                 `json:"triage_duration_ms"`
	ToolResultBytes       int64                 `json:"tool_result_bytes"`
	ReferencedResultBytes int64                 `json:"referenced_result_bytes"`
	ObservationReadCalls  int                   `json:"observation_read_calls"`
	ObservationReadBytes  int64                 `json:"observation_read_bytes"`
	ParentSchemaBytes     int                   `json:"parent_schema_bytes"`
	ParentDescriptorBytes int                   `json:"parent_descriptor_bytes"`
	ChildSchemaBytes      int                   `json:"child_schema_bytes"`
	ChildDescriptorBytes  int                   `json:"child_descriptor_bytes"`
	InvalidToolInputs     int                   `json:"invalid_tool_inputs"`
	ChargedMicros         int64                 `json:"charged_micros"`
	CostCoverage          string                `json:"cost_coverage"`
	UsageCoverage         string                `json:"usage_coverage"`
}

// modelRequestProfile preserves the order of reported provider usage without
// exporting model input, response text, or tool content. A missing receipt is
// explicit; zero tokens must not be mistaken for a measured empty request.
type modelRequestProfile struct {
	CallID           uint64 `json:"call_id"`
	Stage            string `json:"stage,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	UsageReported    bool   `json:"usage_reported"`
	InputTokens      int64  `json:"input_tokens,omitempty"`
	OutputTokens     int64  `json:"output_tokens,omitempty"`
	CacheReadTokens  int64  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64  `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64  `json:"reasoning_tokens,omitempty"`
	DurationMS       int64  `json:"duration_ms,omitempty"`
}

type pairReport struct {
	ID                    string               `json:"id"`
	Split                 string               `json:"split"`
	Source                string               `json:"source"`
	TaskClass             string               `json:"task_class"`
	GraphJin              *graphJinEnvironment `json:"graphjin_environment,omitempty"`
	Fixed                 runReport            `json:"fixed"`
	Canary                runReport            `json:"canary"`
	FixedShadowFirstBlock string               `json:"fixed_shadow_first_block,omitempty"`
	CanaryRegression      bool                 `json:"canary_regression"`
}

type summary struct {
	Pairs                  int            `json:"pairs"`
	FixedSuccess           int            `json:"fixed_success"`
	CanarySuccess          int            `json:"canary_success"`
	CanaryRegressions      int            `json:"canary_regressions"`
	RegressionsByTaskClass map[string]int `json:"regressions_by_task_class"`
	FixedCostMicros        int64          `json:"fixed_cost_micros"`
	CanaryCostMicros       int64          `json:"canary_cost_micros"`
	FixedWallMS            int64          `json:"fixed_wall_ms"`
	CanaryWallMS           int64          `json:"canary_wall_ms"`
	FixedCacheReadTokens   int64          `json:"fixed_cache_read_tokens"`
	CanaryCacheReadTokens  int64          `json:"canary_cache_read_tokens"`
	FixedCacheWriteTokens  int64          `json:"fixed_cache_write_tokens"`
	CanaryCacheWriteTokens int64          `json:"canary_cache_write_tokens"`
	FixedTriageCostMicros  int64          `json:"fixed_triage_cost_micros"`
	CanaryTriageCostMicros int64          `json:"canary_triage_cost_micros"`
	FixedCostPerSuccess    *float64       `json:"fixed_cost_per_verified_success_micros,omitempty"`
	CanaryCostPerSuccess   *float64       `json:"canary_cost_per_verified_success_micros,omitempty"`
	IncompleteUsagePairs   int            `json:"incomplete_usage_pairs"`
	IncompleteCostPairs    int            `json:"incomplete_cost_pairs"`
}

type output struct {
	Version          int          `json:"version"`
	Pairs            []pairReport `json:"pairs"`
	Summary          summary      `json:"summary"`
	HeldOut          summary      `json:"held_out"`
	SyntheticHeldOut summary      `json:"synthetic_held_out"`
	Calibration      summary      `json:"calibration"`
	Limitation       string       `json:"limitation"`
}

func newSummary() summary { return summary{RegressionsByTaskClass: map[string]int{}} }

func (s *summary) add(p pairReport) {
	s.Pairs++
	if p.Fixed.Outcome == "verified_success" {
		s.FixedSuccess++
	}
	if p.Canary.Outcome == "verified_success" {
		s.CanarySuccess++
	}
	if p.CanaryRegression {
		s.CanaryRegressions++
		s.RegressionsByTaskClass[p.TaskClass]++
	}
	s.FixedCostMicros += p.Fixed.ChargedMicros
	s.CanaryCostMicros += p.Canary.ChargedMicros
	s.FixedWallMS += p.Fixed.WallMS
	s.CanaryWallMS += p.Canary.WallMS
	s.FixedCacheReadTokens += p.Fixed.CacheReadTokens
	s.CanaryCacheReadTokens += p.Canary.CacheReadTokens
	s.FixedCacheWriteTokens += p.Fixed.CacheWriteTokens
	s.CanaryCacheWriteTokens += p.Canary.CacheWriteTokens
	s.FixedTriageCostMicros += p.Fixed.TriageCostMicros
	s.CanaryTriageCostMicros += p.Canary.TriageCostMicros
	if p.Fixed.UsageCoverage != "complete" || p.Canary.UsageCoverage != "complete" {
		s.IncompleteUsagePairs++
	}
	if p.Fixed.CostCoverage != "complete" || p.Canary.CostCoverage != "complete" {
		s.IncompleteCostPairs++
	}
}

func (s *summary) finish() {
	if s.FixedSuccess > 0 && s.IncompleteCostPairs == 0 {
		v := float64(s.FixedCostMicros) / float64(s.FixedSuccess)
		s.FixedCostPerSuccess = &v
	}
	if s.CanarySuccess > 0 && s.IncompleteCostPairs == 0 {
		v := float64(s.CanaryCostMicros) / float64(s.CanarySuccess)
		s.CanaryCostPerSuccess = &v
	}
}

func validRun(c runCase) bool {
	return c.Root != "" && c.RunID != "" && digest.MatchString(c.CheckpointSHA256) && c.WallMS >= 0 &&
		(c.Outcome == "verified_success" || c.Outcome == "verified_failure" || c.Outcome == "unverified")
}

func measuredRun(c runCase, expectedMode string, readTrace func(string, string) (session.BudgetTrace, error)) (runReport, session.BudgetTrace, error) {
	trace, err := readTrace(c.Root, c.RunID)
	if err != nil {
		return runReport{}, session.BudgetTrace{}, err
	}
	if trace.RunID != c.RunID || trace.CheckpointSHA256 != c.CheckpointSHA256 || trace.Mode != expectedMode || trace.Status == "interrupted" ||
		(c.Outcome == "verified_success" && trace.Status != "completed") {
		return runReport{}, session.BudgetTrace{}, fmt.Errorf("run %s: checkpoint identity, mode or outcome changed", c.RunID)
	}
	r := runReport{RunID: c.RunID, Outcome: c.Outcome, Status: trace.Status, WallMS: c.WallMS, CostCoverage: "complete", UsageCoverage: "complete",
		ModelRequests: []modelRequestProfile{}}
	modelCosts, toolCosts := map[uint64]int64{}, map[uint64]int64{}
	modelStarted, modelFinished, lookupStarted, lookupFinished := 0, 0, 0, 0
	for _, e := range trace.Events {
		switch e.Type {
		case "tool.catalog.configured":
			if e.ToolCatalog == nil {
				return runReport{}, session.BudgetTrace{}, fmt.Errorf("run %s: missing catalog profile", c.RunID)
			}
			if e.Name == "parent" {
				r.ParentSchemaBytes += e.ToolCatalog.SchemaBytes
				r.ParentDescriptorBytes += e.ToolCatalog.DescriptorBytes
			} else if e.Name == "child.team.researcher" {
				r.ChildSchemaBytes += e.ToolCatalog.SchemaBytes
				r.ChildDescriptorBytes += e.ToolCatalog.DescriptorBytes
			}
		case "tool.input.rejected":
			r.InvalidToolInputs++
		case "model.request.started":
			r.ModelCalls++
			modelStarted++
			if e.Stage == "budget_triage" {
				r.TriageCalls++
			}
			if e.CostMicros != nil {
				modelCosts[e.CallID] = *e.CostMicros
				r.ChargedMicros += *e.CostMicros
				if e.Stage == "budget_triage" {
					r.TriageCostMicros += *e.CostMicros
				}
			} else {
				r.CostCoverage = "partial"
			}
		case "model.request.finished":
			modelFinished++
			profile := modelRequestProfile{CallID: e.CallID, Stage: e.Stage, Provider: e.Origin,
				Model: e.Name, DurationMS: e.DurationMS}
			if e.Usage == nil {
				r.UsageCoverage = "partial"
			} else {
				profile.UsageReported = true
				profile.InputTokens = e.Usage.InputTokens
				profile.OutputTokens = e.Usage.OutputTokens
				profile.CacheReadTokens = e.Usage.CacheReadTokens
				profile.CacheWriteTokens = e.Usage.CacheWriteTokens
				profile.ReasoningTokens = e.Usage.ReasoningTokens
				r.ModelInputTokens += e.Usage.InputTokens
				r.ModelOutputTokens += e.Usage.OutputTokens
				r.CacheReadTokens += e.Usage.CacheReadTokens
				r.CacheWriteTokens += e.Usage.CacheWriteTokens
				r.ReasoningTokens += e.Usage.ReasoningTokens
			}
			r.ModelRequests = append(r.ModelRequests, profile)
			if e.Stage == "budget_triage" {
				r.TriageDurationMS += e.DurationMS
			}
			if e.CostMicros != nil {
				r.ChargedMicros += *e.CostMicros - modelCosts[e.CallID]
				if e.Stage == "budget_triage" {
					r.TriageCostMicros += *e.CostMicros - modelCosts[e.CallID]
				}
			} else {
				r.CostCoverage = "partial"
			}
		case "tool.started":
			if e.Name == "lookup" {
				r.GraphJinCalls++
				lookupStarted++
				if e.CostMicros != nil {
					toolCosts[e.OperationID] = *e.CostMicros
					r.ChargedMicros += *e.CostMicros
				} else {
					r.CostCoverage = "partial"
				}
			}
		case "tool.finished":
			r.ToolResultBytes += int64(e.ResultBytes)
			if e.ResultBytes > agent.InlineSavedResultBytes {
				r.ReferencedResultBytes += int64(e.ResultBytes)
			}
			if e.Name != "lookup" {
				continue
			}
			lookupFinished++
			if e.RemoteUsage == nil || !e.RemoteUsage.Reported {
				r.UsageCoverage = "partial"
			} else {
				r.GraphJinPromptTokens += e.RemoteUsage.PromptTokens
				r.GraphJinOutputTokens += e.RemoteUsage.CompletionTokens
			}
			if e.CostMicros != nil {
				r.ChargedMicros += *e.CostMicros - toolCosts[e.OperationID]
			} else {
				r.CostCoverage = "partial"
			}
		case "observation.retrieved":
			if e.ObservationRead == nil {
				return runReport{}, session.BudgetTrace{}, fmt.Errorf("run %s: missing observation profile", c.RunID)
			}
			r.ObservationReadCalls++
			r.ObservationReadBytes += int64(e.ObservationRead.InstructionBytes + e.ObservationRead.ResultBytes)
		}
	}
	if modelFinished != modelStarted || lookupFinished != lookupStarted {
		r.UsageCoverage = "partial"
		r.CostCoverage = "partial"
	}
	sort.Slice(r.ModelRequests, func(i, j int) bool { return r.ModelRequests[i].CallID < r.ModelRequests[j].CallID })
	return r, trace, nil
}

func compareWithTrace(input []byte, readTrace func(string, string) (session.BudgetTrace, error)) (output, error) {
	var m manifest
	d := json.NewDecoder(bytes.NewReader(input))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.Version != 1 || len(m.Pairs) == 0 || len(m.Pairs) > 1000 {
		return output{}, fmt.Errorf("invalid budget comparison manifest")
	}
	out := output{Version: 1, Summary: newSummary(), HeldOut: newSummary(), SyntheticHeldOut: newSummary(), Calibration: newSummary(),
		Limitation: "Paired outcomes require independent verification and comparable tasks, models, data and conditions; this report does not authorize a canary."}
	seenID, seenRun := map[string]bool{}, map[string]bool{}
	for _, p := range m.Pairs {
		if !pairID.MatchString(p.ID) || seenID[p.ID] || (p.Split != "held_out" && p.Split != "calibration") ||
			(p.Source != "synthetic" && p.Source != "live") || !validRun(p.Fixed) || !validRun(p.Canary) ||
			!(budgeteval.Label{TaskClass: p.TaskClass, Outcome: p.Fixed.Outcome, WallMS: p.Fixed.WallMS}).Valid() ||
			seenRun[p.Fixed.RunID] || seenRun[p.Canary.RunID] || p.Fixed.RunID == p.Canary.RunID {
			return output{}, fmt.Errorf("invalid or repeated budget comparison pair")
		}
		seenID[p.ID], seenRun[p.Fixed.RunID], seenRun[p.Canary.RunID] = true, true, true
		fixed, fixedTrace, err := measuredRun(p.Fixed, "fixed", readTrace)
		if err != nil {
			return output{}, fmt.Errorf("pair %s fixed: %w", p.ID, err)
		}
		canary, canaryTrace, err := measuredRun(p.Canary, "canary", readTrace)
		if err != nil {
			return output{}, fmt.Errorf("pair %s canary: %w", p.ID, err)
		}
		if fixedTrace.TaskFingerprint == "" || fixedTrace.TaskFingerprint != canaryTrace.TaskFingerprint ||
			fixedTrace.CatalogFingerprint == "" || fixedTrace.CatalogFingerprint != canaryTrace.CatalogFingerprint ||
			fixedTrace.RoutingDigest != canaryTrace.RoutingDigest || fixedTrace.Hard != canaryTrace.Hard ||
			fixedTrace.MaxOperations != canaryTrace.MaxOperations {
			return output{}, fmt.Errorf("pair %s: accepted task, admitted catalog, approved route or hard admission limits differ", p.ID)
		}
		if p.Source == "live" && (fixed.GraphJinCalls > 0 || canary.GraphJinCalls > 0) {
			if !p.Fixed.GraphJin.valid() || !p.Canary.GraphJin.valid() || *p.Fixed.GraphJin != *p.Canary.GraphJin {
				return output{}, fmt.Errorf("pair %s: live GraphJin profile or data snapshot attestation missing or different", p.ID)
			}
		}
		shadow, err := budgeteval.Evaluate(fixedTrace, budgeteval.Label{TaskClass: p.TaskClass, Outcome: p.Fixed.Outcome, WallMS: p.Fixed.WallMS})
		if err != nil {
			return output{}, fmt.Errorf("pair %s fixed shadow: %w", p.ID, err)
		}
		report := pairReport{ID: p.ID, Split: p.Split, Source: p.Source, TaskClass: p.TaskClass, Fixed: fixed, Canary: canary,
			CanaryRegression: p.Fixed.Outcome == "verified_success" && p.Canary.Outcome != "verified_success"}
		if p.Source == "live" && (fixed.GraphJinCalls > 0 || canary.GraphJinCalls > 0) {
			profile := *p.Fixed.GraphJin
			report.GraphJin = &profile
		}
		if shadow.FirstBlock != nil {
			report.FixedShadowFirstBlock = shadow.FirstBlock.Kind
		}
		out.Pairs = append(out.Pairs, report)
		out.Summary.add(report)
		if p.Split == "held_out" && p.Source == "live" {
			out.HeldOut.add(report)
		} else if p.Split == "held_out" {
			out.SyntheticHeldOut.add(report)
		} else {
			out.Calibration.add(report)
		}
	}
	out.Summary.finish()
	out.HeldOut.finish()
	out.SyntheticHeldOut.finish()
	out.Calibration.finish()
	return out, nil
}

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: harness-budget-compare < manifest.json")
		os.Exit(2)
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
	if err != nil || len(input) > 1<<20 {
		fmt.Fprintln(os.Stderr, "invalid budget comparison manifest")
		os.Exit(1)
	}
	out, err := compareWithTrace(input, session.ReadBudgetTrace)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
