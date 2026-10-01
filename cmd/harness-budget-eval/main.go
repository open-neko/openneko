// harness-budget-eval scores trusted, stopped shadow runs from independently
// labelled task outcomes. It never dispatches a model or changes a budget.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/open-neko/harness/internal/budgeteval"
	"github.com/open-neko/harness/internal/session"
)

var caseID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var checkpointDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Case struct {
	ID               string           `json:"id"`
	Root             string           `json:"root"`
	RunID            string           `json:"run_id"`
	CheckpointSHA256 string           `json:"checkpoint_sha256"`
	Split            string           `json:"split"`  // calibration, held_out
	Source           string           `json:"source"` // synthetic, live
	Label            budgeteval.Label `json:"label"`
}

type Manifest struct {
	Version int    `json:"version"`
	Cases   []Case `json:"cases"`
}

type CaseReport struct {
	ID     string            `json:"id"`
	Split  string            `json:"split"`
	Source string            `json:"source"`
	Budget budgeteval.Report `json:"budget"`
}

type Summary struct {
	Cases                    int            `json:"cases"`
	HeldOut                  int            `json:"held_out"`
	VerifiedSuccess          int            `json:"verified_success"`
	VerifiedFailure          int            `json:"verified_failure"`
	Unverified               int            `json:"unverified"`
	ClassFalseLowByTaskClass map[string]int `json:"class_false_low_by_task_class"`
	PrematureBudgetFailures  int            `json:"premature_budget_failures"`
	IncompleteUsage          int            `json:"incomplete_usage"`
	ShadowCostMicros         int64          `json:"shadow_cost_micros"`
	FixedCostMicros          int64          `json:"fixed_without_classifier_cost_micros"`
	ClassifierCostMicros     int64          `json:"classifier_cost_micros"`
	ClassifierLatencyMS      int64          `json:"classifier_latency_ms"`
	FixedCostPerSuccess      *float64       `json:"fixed_cost_per_verified_success_micros,omitempty"`
	CanaryReady              bool           `json:"canary_ready"`
	Limitation               string         `json:"limitation"`
}

type Output struct {
	Version     int          `json:"version"`
	Cases       []CaseReport `json:"cases"`
	Summary     Summary      `json:"summary"`
	HeldOut     Summary      `json:"held_out"`
	Calibration Summary      `json:"calibration"`
}

func newSummary() Summary {
	return Summary{ClassFalseLowByTaskClass: map[string]int{},
		Limitation: "Shadow replay detects admission risk; it cannot estimate changed-model behavior or justify a canary without held-out live outcomes and a controlled comparison."}
}

func (s *Summary) add(item Case, report budgeteval.Report) {
	s.Cases++
	if item.Split == "held_out" {
		s.HeldOut++
	}
	switch item.Label.Outcome {
	case "verified_success":
		s.VerifiedSuccess++
	case "verified_failure":
		s.VerifiedFailure++
	default:
		s.Unverified++
	}
	if report.ClassFalseLow {
		s.ClassFalseLowByTaskClass[item.Label.TaskClass]++
	}
	if report.PrematureBudgetFailure {
		s.PrematureBudgetFailures++
	}
	if report.UsageCoverage != "complete" {
		s.IncompleteUsage++
	}
	s.ShadowCostMicros += report.ActualCostMicros
	s.FixedCostMicros += report.ActualCostMicros - report.ClassifierCostMicros
	s.ClassifierCostMicros += report.ClassifierCostMicros
	s.ClassifierLatencyMS += report.ClassifierLatencyMS
}

func (s *Summary) finish() {
	if s.VerifiedSuccess > 0 {
		perSuccess := float64(s.FixedCostMicros) / float64(s.VerifiedSuccess)
		s.FixedCostPerSuccess = &perSuccess
	}
}

func evaluate(input []byte) (Output, error) {
	return evaluateWithTrace(input, session.ReadBudgetTrace)
}

func evaluateWithTrace(input []byte, readTrace func(string, string) (session.BudgetTrace, error)) (Output, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Output{}, fmt.Errorf("invalid budget evaluation manifest: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || manifest.Version != 1 || len(manifest.Cases) < 1 || len(manifest.Cases) > 1000 {
		return Output{}, fmt.Errorf("invalid budget evaluation manifest")
	}
	output := Output{Version: 1, Summary: newSummary(), HeldOut: newSummary(), Calibration: newSummary()}
	seenID, seenRun := map[string]bool{}, map[string]bool{}
	for _, item := range manifest.Cases {
		if !caseID.MatchString(item.ID) || item.Root == "" || item.RunID == "" || !checkpointDigest.MatchString(item.CheckpointSHA256) || seenID[item.ID] || seenRun[item.RunID] ||
			(item.Split != "calibration" && item.Split != "held_out") ||
			(item.Source != "synthetic" && item.Source != "live") || !item.Label.Valid() {
			return Output{}, fmt.Errorf("invalid or repeated budget evaluation case")
		}
		seenID[item.ID], seenRun[item.RunID] = true, true
		trace, err := readTrace(item.Root, item.RunID)
		if err != nil {
			return Output{}, fmt.Errorf("case %s: %w", item.ID, err)
		}
		if trace.CheckpointSHA256 != item.CheckpointSHA256 {
			return Output{}, fmt.Errorf("case %s: checkpoint changed after outcome labelling", item.ID)
		}
		report, err := budgeteval.Evaluate(trace, item.Label)
		if err != nil {
			return Output{}, fmt.Errorf("case %s: %w", item.ID, err)
		}
		output.Cases = append(output.Cases, CaseReport{ID: item.ID, Split: item.Split, Source: item.Source, Budget: report})
		output.Summary.add(item, report)
		if item.Split == "held_out" {
			output.HeldOut.add(item, report)
		} else {
			output.Calibration.add(item, report)
		}
	}
	output.Summary.finish()
	output.HeldOut.finish()
	output.Calibration.finish()
	return output, nil
}

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: harness-budget-eval < manifest.json")
		os.Exit(2)
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
	if err != nil || len(input) > 1<<20 {
		fmt.Fprintln(os.Stderr, "invalid budget evaluation manifest")
		os.Exit(1)
	}
	output, err := evaluate(input)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
