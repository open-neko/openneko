// Package budgettriage evaluates an optional shadow budget recommendation.
// It does not grant tools, choose model routes, or change a run's hard ceiling.
package budgettriage

import (
	"context"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	ax "github.com/ax-llm/ax/packages/go"
)

const Version = "budget-triage-v1"
const reservationTokens int64 = 512

var familyName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var classes = []string{"short_answer", "multi_step", "artifact_pipeline", "uncertain"}

// Input contains a host-approved bounded summary and trusted run metadata.
// It is sent to the classifier but never copied into Observation telemetry.
type Input struct {
	Summary           string   `json:"summary"`
	ArtifactRequested bool     `json:"artifact_requested"`
	ToolFamilies      []string `json:"tool_families"`
	InputBytes        int      `json:"input_bytes"`
}

func (in Input) Valid() bool {
	if strings.TrimSpace(in.Summary) == "" || len(in.Summary) > 2048 || !utf8.ValidString(in.Summary) || in.InputBytes < 0 || in.InputBytes > 131072 || len(in.ToolFamilies) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, family := range in.ToolFamilies {
		if !familyName.MatchString(family) || seen[family] {
			return false
		}
		seen[family] = true
	}
	return true
}

type SystemOneClient interface {
	SystemOne(context.Context, ax.TypesafeRequest, map[string]ax.Value) (*ax.TypesafeResponse, error)
}

// Price is supplied by the host's pinned model route profile. Keeping this
// narrow avoids coupling the classifier to the agent runtime.
type Price interface {
	Valid() bool
	Reservation(int64) int64
	Observed(int64, int64, int64) int64
}

// Journal is the host-owned durable accounting boundary. Reserve must commit
// before transport; Settle must commit the observed result or unavailable
// coverage before the surrounding run may continue.
type Journal interface {
	Reserve(context.Context, Observation) error
	Settle(context.Context, Observation) error
}

// Observation is safe to emit as content-free telemetry. ChargedMicros is an
// upper-bound estimate; a missing usage report retains the pre-call reserve.
type Observation struct {
	Version          string             `json:"version"`
	RequestedModel   string             `json:"requested_model"`
	ActualModel      string             `json:"actual_model,omitempty"`
	Choice           string             `json:"choice,omitempty"`
	Probabilities    map[string]float64 `json:"probabilities,omitempty"`
	SuggestedProfile string             `json:"suggested_profile"`
	Reason           string             `json:"reason"`
	InputTokens      int64              `json:"input_tokens,omitempty"`
	OutputTokens     int64              `json:"output_tokens,omitempty"`
	Coverage         string             `json:"coverage"`
	ChargedMicros    int64              `json:"charged_micros"`
	LatencyMS        int64              `json:"latency_ms"`
}

// Valid checks the durable, content-free observation shape on checkpoint
// recovery. A malformed classifier response must not become trusted budget
// evidence merely because it was serialized by a prior process.
func (o Observation) Valid() bool {
	if o.Version != Version || o.RequestedModel == "" || len(o.RequestedModel) > 128 ||
		o.ChargedMicros < 1 || o.ChargedMicros > 8_000_000_000_000_000 ||
		o.LatencyMS < 0 || o.LatencyMS > 120_000 ||
		o.InputTokens < 0 || o.OutputTokens < 0 || o.InputTokens > 1_000_000_000_000 || o.OutputTokens > 1_000_000_000_000 {
		return false
	}
	if o.Coverage != "complete" && o.Coverage != "unavailable" ||
		o.Coverage == "unavailable" && (o.InputTokens != 0 || o.OutputTokens != 0) {
		return false
	}
	switch o.Reason {
	case "classified", "uncertain", "low_confidence", "invalid_result", "invalid_usage", "classifier_unavailable":
	default:
		return false
	}
	switch o.SuggestedProfile {
	case "fixed", "short", "multi_step", "artifact":
	default:
		return false
	}
	if o.Choice == "" {
		return len(o.Probabilities) == 0 && o.SuggestedProfile == "fixed"
	}
	if len(o.Probabilities) != len(classes) {
		return false
	}
	sum, chosen := 0.0, false
	for _, class := range classes {
		p, ok := o.Probabilities[class]
		if !ok || math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return false
		}
		sum += p
		chosen = chosen || class == o.Choice
	}
	profile, reason := chooseProfile(o.Probabilities)
	return chosen && sum >= .98 && sum <= 1.02 && o.SuggestedProfile == profile && o.Reason == reason
}

// Evaluate is shadow-only. A successful recommendation never changes the hard
// run limit. The caller's Journal must persist both accounting transitions;
// an unavailable decision keeps fixed limits.
func Evaluate(ctx context.Context, client SystemOneClient, model string, in Input, price Price, remainingMicros int64, journal Journal) (Observation, error) {
	if client == nil || journal == nil || price == nil || model == "" || len(model) > 128 || !in.Valid() || !price.Valid() || remainingMicros < 0 {
		return Observation{}, errors.New("invalid budget triage configuration")
	}
	result := Observation{Version: Version, RequestedModel: model, SuggestedProfile: "fixed", Reason: "classifier_unavailable", Coverage: "unavailable"}
	reserve := price.Reservation(reservationTokens)
	if reserve > remainingMicros {
		result.Reason = "budget_denied"
		return result, nil
	}
	result.ChargedMicros = reserve
	if err := journal.Reserve(ctx, result); err != nil {
		return Observation{}, err
	}
	settle := func() (Observation, error) {
		if err := journal.Settle(ctx, result); err != nil {
			return Observation{}, err
		}
		return result, nil
	}
	families := append([]string(nil), in.ToolFamilies...)
	sort.Strings(families)
	request := ax.TypesafeRequest{
		Model: model,
		State: ax.Object("task_summary", in.Summary, "artifact_requested", in.ArtifactRequested, "tool_families", families, "input_bytes", in.InputBytes),
		Questions: map[string]ax.TypesafeQuestion{"workload": {
			Type:         "choice",
			Instructions: "Classify the likely work needed to reach a verified outcome. A short prompt may still require a long data or artifact pipeline. This choice sizes a budget only; it does not choose tools, permissions or model routes.",
			Criteria: ax.Object(
				"short_answer", "A direct answer with no substantial investigation or artifact work",
				"multi_step", "Several reads, checks, or tool steps are likely before a verified answer",
				"artifact_pipeline", "Create or verify a file, report, batch data pipeline, or other durable artifact",
				"uncertain", "The summary lacks enough information to size work reliably"),
		}},
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	response, err := client.SystemOne(callCtx, request, nil)
	result.LatencyMS = time.Since(start).Milliseconds()
	if err != nil || response == nil {
		return settle()
	}
	result.ActualModel = response.Model
	if response.Usage.InputTokens > 0 || response.Usage.OutputTokens > 0 {
		if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 || response.Usage.InputTokens > 1_000_000_000_000 || response.Usage.OutputTokens > 1_000_000_000_000 {
			result.Reason = "invalid_usage"
			return settle()
		}
		result.InputTokens = response.Usage.InputTokens
		result.OutputTokens = response.Usage.OutputTokens
		result.Coverage = "complete"
		observed := price.Observed(response.Usage.InputTokens, response.Usage.OutputTokens, response.Usage.InputTokens+response.Usage.OutputTokens)
		if observed > result.ChargedMicros {
			result.ChargedMicros = observed
		}
	}
	answer, ok := response.Answers["workload"]
	if !ok || response.Model != model || answer.Type != "choice" || !validDistribution(answer) {
		result.Reason = "invalid_result"
		return settle()
	}
	result.Choice = answer.Choice
	result.Probabilities = make(map[string]float64, len(classes))
	for _, class := range classes {
		result.Probabilities[class] = answer.Probabilities[class]
	}
	result.SuggestedProfile, result.Reason = chooseProfile(result.Probabilities)
	return settle()
}

func validDistribution(answer ax.TypesafeAnswer) bool {
	if len(answer.Probabilities) != len(classes) || math.IsNaN(answer.Confidence) || answer.Confidence < 0 || answer.Confidence > 1 {
		return false
	}
	sum, chosen := 0.0, false
	for _, class := range classes {
		p, ok := answer.Probabilities[class]
		if !ok || math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return false
		}
		sum += p
		chosen = chosen || answer.Choice == class
	}
	return chosen && sum >= 0.98 && sum <= 1.02
}

// Conservative shadow thresholds prevent a split short/complex prediction
// from becoming a short budget. Thresholds require held-out calibration.
func chooseProfile(p map[string]float64) (string, string) {
	if p["uncertain"] >= .20 {
		return "fixed", "uncertain"
	}
	if p["artifact_pipeline"] >= .50 {
		return "artifact", "classified"
	}
	if p["multi_step"] >= .60 && p["artifact_pipeline"] < .15 {
		return "multi_step", "classified"
	}
	if p["short_answer"] >= .85 && p["multi_step"]+p["artifact_pipeline"] < .10 {
		return "short", "classified"
	}
	return "fixed", "low_confidence"
}
