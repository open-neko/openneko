package agent

import "sort"

// TerminalDecision is a host-authored check of a candidate result. EvidenceIDs
// refer to committed successful operations, never model-authored references.
type TerminalDecision struct {
	Accepted    bool  `json:"accepted"`
	EvidenceIDs []int `json:"evidence_ids,omitempty"`
}

func (d TerminalDecision) Valid(operations []SavedOperation) bool {
	if !d.Accepted && len(d.EvidenceIDs) != 0 {
		return false
	}
	if len(d.EvidenceIDs) > 32 || !sort.IntsAreSorted(d.EvidenceIDs) {
		return false
	}
	for i, id := range d.EvidenceIDs {
		if id < 1 || id > len(operations) || i > 0 && id == d.EvidenceIDs[i-1] || operations[id-1].ID != id ||
			!operations[id-1].Finished || operations[id-1].Error != "" || len(operations[id-1].Result) == 0 || toolResultFailed(operations[id-1].Result) {
			return false
		}
	}
	return true
}

func terminalOperations(parent, child []SavedOperation) []SavedOperation {
	if len(child) > len(parent) {
		parent, child = child, parent
	}
	all := append([]SavedOperation(nil), parent...)
	for i := range child {
		if all[i].ID == 0 {
			all[i] = child[i]
		}
	}
	return all
}
