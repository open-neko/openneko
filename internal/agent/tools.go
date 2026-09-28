package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Tools are host-installed capabilities. Propose only creates an approval request;
// it cannot execute an effect or accept model-selected approval state.
type Tools struct {
	Lookup       func(context.Context, string) (json.RawMessage, error)
	Propose      func(context.Context, Proposal) (ProposalReceipt, error)
	OnResume     func(context.Context, []SavedOperation) error
	Capabilities []Capability
	ChildReads   []string        // Exact host-admitted read tools for one owned child agent.
	SkillCatalog []SkillMetadata // Host-staged catalog hints; never capability grants.
	Scope        string          // Trusted run-scoped admission context, never model input.
}

type SkillMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

var skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func ValidSkillName(name string) bool { return skillName.MatchString(name) }

func (t Tools) skills() ([]SkillMetadata, error) {
	if len(t.SkillCatalog) > 64 {
		return nil, fmt.Errorf("too many staged skills")
	}
	list := append([]SkillMetadata(nil), t.SkillCatalog...)
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	for i, skill := range list {
		if !skillName.MatchString(skill.Name) || len(skill.Description) > 500 ||
			(i > 0 && list[i-1].Name == skill.Name) {
			return nil, fmt.Errorf("invalid staged skill catalog")
		}
	}
	return list, nil
}

// Capability is installed by trusted host code for this run. Description is
// host-authored; remote discovery metadata must never become an instruction.
type Capability struct {
	Name, Version, Origin, Effect, Description string
	InputSchema                                json.RawMessage
	Call                                       func(context.Context, json.RawMessage) (json.RawMessage, error)
}

type admittedTool struct {
	Capability
	binding string
	schema  *jsonschema.Resolved
}

var capabilityName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func ValidToolName(name string) bool { return capabilityName.MatchString(name) }

func (t Tools) admitted() ([]admittedTool, error) {
	list := append([]Capability(nil), t.Capabilities...)
	for _, capability := range list {
		if capability.Name == "lookup" || capability.Name == "propose" {
			return nil, fmt.Errorf("reserved capability name")
		}
	}
	if t.Lookup != nil {
		list = append([]Capability{{Name: "lookup", Version: "1", Origin: "native", Effect: "read", Description: "Delegate a focused read-only investigation to the server-side data agent; preserve refusals and evidence.", InputSchema: json.RawMessage(`{"type":"string","minLength":1,"maxLength":8000}`), Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var instruction string
			if err := json.Unmarshal(raw, &instruction); err != nil {
				return nil, err
			}
			return t.Lookup(ctx, instruction)
		}}}, list...)
	}
	if t.Propose != nil {
		list = append(list, Capability{Name: "propose", Version: "1", Origin: "native", Effect: "approval", Description: "Create a governed action proposal requiring human approval; never execute the effect.", InputSchema: json.RawMessage(`{"type":"object","required":["action","arguments","summary"],"properties":{"action":{"type":"string"},"arguments":{"type":"object"},"summary":{"type":"string"}},"additionalProperties":false}`), Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			proposal, err := ParseProposal(raw)
			if err != nil {
				return nil, err
			}
			receipt, err := t.Propose(ctx, proposal)
			if err != nil {
				return nil, err
			}
			if err = receipt.Validate(); err != nil {
				return nil, err
			}
			return json.Marshal(receipt)
		}})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	seen := map[string]bool{}
	admitted := make([]admittedTool, 0, len(list))
	for _, cap := range list {
		if !capabilityName.MatchString(cap.Name) || seen[cap.Name] || cap.Version == "" || len(cap.Version) > 128 || cap.Origin == "" || len(cap.Origin) > 128 || len(cap.Description) > 1000 || cap.Description == "" || cap.Call == nil || len(cap.InputSchema) == 0 || len(cap.InputSchema) > 16384 || (cap.Effect != "read" && cap.Effect != "approval" && cap.Effect != "interaction" && cap.Effect != "pause" && cap.Effect != "durable") {
			return nil, fmt.Errorf("invalid or duplicate capability")
		}
		seen[cap.Name] = true
		var schema jsonschema.Schema
		if err := json.Unmarshal(cap.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("invalid capability schema: %w", err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("unsupported capability schema: %w", err)
		}
		fingerprint, _ := json.Marshal([]any{cap.Name, cap.Version, cap.Origin, cap.Effect, cap.Description, schema})
		sum := sha256.Sum256(fingerprint)
		admitted = append(admitted, admittedTool{Capability: cap, binding: hex.EncodeToString(sum[:]), schema: resolved})
	}
	return admitted, nil
}

// Binding pins a trusted capability definition to a durable operation.
func (t Tools) Binding(name string) (string, error) {
	admitted, err := t.admitted()
	if err != nil {
		return "", err
	}
	for _, capability := range admitted {
		if capability.Name == name {
			return capability.binding, nil
		}
	}
	return "", fmt.Errorf("capability unavailable")
}

func (t Tools) CatalogHash() (string, error) {
	if len(t.Scope) > 16384 {
		return "", fmt.Errorf("catalog scope exceeds limit")
	}
	admitted, err := t.admitted()
	if err != nil {
		return "", err
	}
	bindings := make([]string, 0, len(admitted))
	for _, capability := range admitted {
		bindings = append(bindings, capability.Name+":"+capability.binding)
	}
	sort.Strings(bindings)
	child, err := t.childReads(admitted)
	if err != nil {
		return "", err
	}
	childNames := make([]string, len(child))
	for i, capability := range child {
		childNames[i] = capability.Name
	}
	skills, err := t.skills()
	if err != nil {
		return "", err
	}
	var raw []byte
	if len(skills) == 0 {
		// Preserve the pre-selector catalog identity for existing checkpoints.
		raw, _ = json.Marshal([]any{t.Scope, bindings, childNames})
	} else {
		raw, _ = json.Marshal([]any{t.Scope, bindings, childNames, skills})
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (t Tools) childReads(admitted []admittedTool) ([]admittedTool, error) {
	if len(t.ChildReads) > 8 {
		return nil, fmt.Errorf("too many child capabilities")
	}
	wanted := make(map[string]bool, len(t.ChildReads))
	for _, name := range t.ChildReads {
		if !ValidToolName(name) || wanted[name] {
			return nil, fmt.Errorf("invalid child capability")
		}
		wanted[name] = true
	}
	child := make([]admittedTool, 0, len(wanted))
	for _, capability := range admitted {
		if !wanted[capability.Name] {
			continue
		}
		if capability.Effect != "read" {
			return nil, fmt.Errorf("child capability must be read-only")
		}
		child = append(child, capability)
		delete(wanted, capability.Name)
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("child capability unavailable")
	}
	return child, nil
}

func (c admittedTool) input(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 65536 {
		return "", fmt.Errorf("invalid capability input")
	}
	var decoded any
	if err = json.Unmarshal(raw, &decoded); err != nil || c.schema.Validate(decoded) != nil {
		return "", fmt.Errorf("capability input violates schema")
	}
	if c.Name == "lookup" {
		instruction, ok := decoded.(string)
		if !ok || strings.TrimSpace(instruction) == "" {
			return "", fmt.Errorf("invalid lookup instruction")
		}
		return instruction, nil // Legacy checkpoint representation.
	}
	if c.Name == "propose" {
		proposal, err := ParseProposal(raw)
		if err != nil {
			return "", err
		}
		raw, _ = json.Marshal(proposal)
	}
	return string(raw), nil
}

func (c admittedTool) invoke(ctx context.Context, instruction string) (json.RawMessage, error) {
	if c.Name == "lookup" {
		return c.Call(ctx, json.RawMessage(strconv.Quote(instruction)))
	}
	return c.Call(ctx, json.RawMessage(instruction))
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
		return o.Binding == "" && strings.TrimSpace(o.Instruction) != "" && len(o.Instruction) <= 8000
	case "propose":
		_, err := ParseProposal([]byte(o.Instruction))
		return o.Binding == "" && err == nil
	default:
		_, err := hex.DecodeString(o.Binding)
		return capabilityName.MatchString(o.Name()) && len(o.Binding) == 64 && err == nil && len(o.Instruction) <= 65536 && json.Valid([]byte(o.Instruction))
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
