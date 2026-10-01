package budgettriage

import "regexp"

var policyVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Limits are a host-approved candidate allowance. A proposal is telemetry
// until held-out evidence permits a separately gated admission policy.
type Limits struct {
	MaxModelCalls  int   `json:"max_model_calls"`
	MaxModelTokens int64 `json:"max_model_tokens"`
	MaxCostMicros  int64 `json:"max_cost_micros"`
}

func (l Limits) Valid() bool {
	return l.MaxModelCalls >= 1 && l.MaxModelCalls <= 64 &&
		l.MaxModelTokens >= 1 && l.MaxModelTokens <= 10_000_000 &&
		l.MaxCostMicros >= 1 && l.MaxCostMicros <= 1_000_000_000_000
}

func (l Limits) within(hard Limits) bool {
	return l.Valid() && hard.Valid() && l.MaxModelCalls <= hard.MaxModelCalls &&
		l.MaxModelTokens <= hard.MaxModelTokens && l.MaxCostMicros <= hard.MaxCostMicros
}

func minLimits(candidate, hard Limits) Limits {
	if candidate.MaxModelCalls > hard.MaxModelCalls {
		candidate.MaxModelCalls = hard.MaxModelCalls
	}
	if candidate.MaxModelTokens > hard.MaxModelTokens {
		candidate.MaxModelTokens = hard.MaxModelTokens
	}
	if candidate.MaxCostMicros > hard.MaxCostMicros {
		candidate.MaxCostMicros = hard.MaxCostMicros
	}
	return candidate
}

// Policy is pinned in the operator-owned route manifest. Its caps are ordered
// so a harder class can never receive less room than a simpler class.
type Policy struct {
	Version   string `json:"version"`
	Short     Limits `json:"short"`
	MultiStep Limits `json:"multi_step"`
	Artifact  Limits `json:"artifact"`
}

func (p Policy) Valid() bool {
	return policyVersion.MatchString(p.Version) && p.Short.Valid() && p.MultiStep.Valid() && p.Artifact.Valid() &&
		p.Short.MaxModelCalls <= p.MultiStep.MaxModelCalls && p.MultiStep.MaxModelCalls <= p.Artifact.MaxModelCalls &&
		p.Short.MaxModelTokens <= p.MultiStep.MaxModelTokens && p.MultiStep.MaxModelTokens <= p.Artifact.MaxModelTokens &&
		p.Short.MaxCostMicros <= p.MultiStep.MaxCostMicros && p.MultiStep.MaxCostMicros <= p.Artifact.MaxCostMicros
}

// Proposal is content-free and never changes the active admission limits.
type Proposal struct {
	Version string `json:"version"`
	Profile string `json:"profile"`
	Limits  Limits `json:"limits"`
}

// Extension records one possible next allowance after a successful durable
// operation. It is observational until dynamic admission is explicitly enabled.
type Extension struct {
	Version     string `json:"version"`
	From        string `json:"from"`
	To          string `json:"to"`
	OperationID uint64 `json:"operation_id"`
	Limits      Limits `json:"limits"`
	Reason      string `json:"reason"`
}

func (e Extension) Valid(previous Proposal, hard Limits) bool {
	if e.Version != previous.Version || e.OperationID == 0 || e.Reason != "next_call_exceeds_profile" ||
		!e.Limits.within(hard) || e.Limits == previous.Limits || e.From != previous.Profile ||
		e.Limits.MaxModelCalls < previous.Limits.MaxModelCalls ||
		e.Limits.MaxModelTokens < previous.Limits.MaxModelTokens ||
		e.Limits.MaxCostMicros < previous.Limits.MaxCostMicros {
		return false
	}
	switch e.From {
	case "short":
		return e.To == "multi_step"
	case "multi_step":
		return e.To == "artifact"
	case "artifact":
		return e.To == "fixed" && e.Limits == hard
	default:
		return false
	}
}

func (p Policy) Extend(previous Proposal, hard Limits, operationID uint64) (Extension, bool) {
	if !p.Valid() || !previous.Valid(hard) || previous.Version != p.Version || operationID == 0 {
		return Extension{}, false
	}
	extension := Extension{Version: p.Version, From: previous.Profile, OperationID: operationID,
		Reason: "next_call_exceeds_profile"}
	switch previous.Profile {
	case "short":
		extension.To, extension.Limits = "multi_step", minLimits(p.MultiStep, hard)
	case "multi_step":
		extension.To, extension.Limits = "artifact", minLimits(p.Artifact, hard)
	case "artifact":
		extension.To, extension.Limits = "fixed", hard
	default:
		return Extension{}, false
	}
	return extension, extension.Valid(previous, hard) && extension.Limits != previous.Limits
}

func (p Proposal) Valid(hard Limits) bool {
	if !policyVersion.MatchString(p.Version) || !p.Limits.within(hard) {
		return false
	}
	switch p.Profile {
	case "fixed":
		return p.Limits == hard
	case "short", "multi_step", "artifact":
		return true
	default:
		return false
	}
}

func (p Policy) Propose(observed Observation, hard Limits) (Proposal, bool) {
	if !p.Valid() || !observed.Valid() || !hard.Valid() {
		return Proposal{}, false
	}
	result := Proposal{Version: p.Version, Profile: observed.SuggestedProfile, Limits: hard}
	switch result.Profile {
	case "short":
		result.Limits = minLimits(p.Short, hard)
	case "multi_step":
		result.Limits = minLimits(p.MultiStep, hard)
	case "artifact":
		result.Limits = minLimits(p.Artifact, hard)
	case "fixed":
	default:
		return Proposal{}, false
	}
	return result, result.Valid(hard)
}
