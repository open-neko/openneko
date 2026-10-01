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
