package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Tools are host-installed capabilities. Propose only creates an approval request;
// it cannot execute an effect or accept model-selected approval state.
type Tools struct {
	Lookup  func(context.Context, string) (json.RawMessage, error)
	Propose func(context.Context, Proposal) (ProposalReceipt, error)
}

type Proposal struct {
	Action    string          `json:"action"`
	Arguments json.RawMessage `json:"arguments"`
	Summary   string          `json:"summary"`
}

type ProposalReceipt struct {
	ID     string `json:"id,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func ParseProposal(raw []byte) (Proposal, error) {
	var p Proposal
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 65536 || !json.Valid(raw) || decoder.Decode(&p) != nil ||
		strings.TrimSpace(p.Action) == "" || len(p.Action) > 128 ||
		strings.TrimSpace(p.Summary) == "" || len(p.Summary) > 1000 ||
		len(bytes.TrimSpace(p.Arguments)) == 0 || bytes.TrimSpace(p.Arguments)[0] != '{' {
		return p, fmt.Errorf("invalid bounded action proposal")
	}
	return p, nil
}

func (r ProposalReceipt) Validate() error {
	if len(r.ID) > 128 || len(r.Reason) > 2000 ||
		(r.Status != "pending_approval" && r.Status != "denied") ||
		(r.Status == "pending_approval" && strings.TrimSpace(r.ID) == "") ||
		(r.Status == "denied" && strings.TrimSpace(r.Reason) == "") {
		return fmt.Errorf("invalid proposal receipt; effects are not executable")
	}
	return nil
}

func (o SavedOperation) Name() string {
	if o.Tool == "" {
		return "lookup"
	} // Existing read-only checkpoints.
	return o.Tool
}

func (o SavedOperation) ValidInput() bool {
	switch o.Name() {
	case "lookup":
		return strings.TrimSpace(o.Instruction) != "" && len(o.Instruction) <= 8000
	case "propose":
		_, err := ParseProposal([]byte(o.Instruction))
		return err == nil
	default:
		return false
	}
}

func ParseProposalReceipt(raw []byte) (ProposalReceipt, error) {
	var receipt ProposalReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 16384 || !json.Valid(raw) || decoder.Decode(&receipt) != nil {
		return receipt, fmt.Errorf("invalid proposal receipt")
	}
	return receipt, receipt.Validate()
}
