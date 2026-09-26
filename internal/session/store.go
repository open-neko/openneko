// Package session persists one bounded run checkpoint per host-scoped run ID.
package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
)

type operation = agent.SavedOperation

type checkpoint struct {
	Version    int           `json:"version"`
	Catalog    string        `json:"catalog,omitempty"`
	Spec       agent.Spec    `json:"spec"`
	Events     []agent.Event `json:"events"`
	Operations []operation   `json:"operations"`
	Result     *agent.Result `json:"result,omitempty"`
}

// Run requires a trusted, consumer-scoped local directory. It rejects concurrent
// execution, conflicting input and unfinished previous attempts. Completed runs
// replay stored events without recontacting models or tools. Interrupted runs
// require explicit Resume after reconciliation.
func Run(ctx context.Context, root string, spec agent.Spec, client ax.AIClient, lookup func(context.Context, string) (json.RawMessage, error), emit func(agent.Event) error) (agent.Result, error) {
	return run(ctx, root, spec, client, agent.Tools{Lookup: lookup}, emit, false)
}

// Resume is an explicit authorized new attempt; unresolved operations never dispatch.
func Resume(ctx context.Context, root string, spec agent.Spec, client ax.AIClient, lookup func(context.Context, string) (json.RawMessage, error), emit func(agent.Event) error) (agent.Result, error) {
	return run(ctx, root, spec, client, agent.Tools{Lookup: lookup}, emit, true)
}

func RunWithTools(ctx context.Context, root string, spec agent.Spec, client ax.AIClient, tools agent.Tools, emit func(agent.Event) error) (agent.Result, error) {
	return run(ctx, root, spec, client, tools, emit, false)
}

func ResumeWithTools(ctx context.Context, root string, spec agent.Spec, client ax.AIClient, tools agent.Tools, emit func(agent.Event) error) (agent.Result, error) {
	return run(ctx, root, spec, client, tools, emit, true)
}

func run(ctx context.Context, root string, spec agent.Spec, client ax.AIClient, tools agent.Tools, emit func(agent.Event) error, resume bool) (agent.Result, error) {
	if root == "" || spec.Version != 1 || spec.OperationLimit() < 1 || spec.OperationLimit() > 32 || strings.TrimSpace(spec.RunID) == "" || strings.TrimSpace(spec.InputID) == "" || strings.TrimSpace(spec.Prompt) == "" || len(spec.Prompt) > 65536 || len(spec.RunID) > 128 || len(spec.InputID) > 128 || emit == nil {
		return agent.Result{}, fmt.Errorf("invalid persistent run")
	}
	catalog, err := tools.CatalogHash()
	if err != nil {
		return agent.Result{}, err
	}
	if !resume {
		if err := os.MkdirAll(root, 0700); err != nil {
			return agent.Result{}, err
		}
	}
	sum := sha256.Sum256([]byte(spec.RunID))
	path := filepath.Join(root, hex.EncodeToString(sum[:]))
	flags := os.O_RDWR
	if !resume {
		flags |= os.O_CREATE
	}
	lock, err := os.OpenFile(path+".lock", flags, 0600)
	if err != nil {
		return agent.Result{}, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return agent.Result{}, fmt.Errorf("run already executing")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state := checkpoint{Version: 1, Catalog: catalog, Spec: spec}
	file, err := os.Open(path + ".json")
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(file, (8<<20)+1))
		file.Close()
	}
	prior := agent.Continuation{Attempt: 1}
	if err == nil {
		state, err = decodeCheckpoint(data, spec)
		if err != nil {
			return agent.Result{}, err
		}
		if state.Result == nil {
			if state.Catalog != "" && state.Catalog != catalog {
				return agent.Result{}, fmt.Errorf("admitted capability catalog changed; new run required")
			}
			if !resume {
				return agent.Result{}, fmt.Errorf("interrupted run requires reconciliation; stored operations retained")
			}
			prior, err = continuation(state)
			if err != nil {
				return agent.Result{}, err
			}
		}
		for _, e := range state.Events {
			if err := emit(e); err != nil {
				return agent.Result{}, err
			}
		}
		if state.Result != nil {
			return *state.Result, nil
		}
	}
	if err != nil && resume && os.IsNotExist(err) {
		return agent.Result{}, fmt.Errorf("cannot resume missing checkpoint")
	}
	if err != nil && !os.IsNotExist(err) {
		return agent.Result{}, err
	}
	save := func() error { return saveCheckpoint(root, path, state) }
	if err := save(); err != nil {
		return agent.Result{}, err
	}
	var persistenceErr error
	record := func(ctx context.Context, name, binding, instruction string, call func() (json.RawMessage, error)) (json.RawMessage, error) {
		if persistenceErr != nil {
			return nil, persistenceErr
		}
		n := len(state.Operations)

		if agent.OperationID(ctx) != uint64(n+1) {
			return nil, fmt.Errorf("tool operation identity mismatch")
		}
		op := operation{ID: n + 1, Binding: binding, Instruction: instruction}
		if name != "lookup" {
			op.Tool = name
		}
		state.Operations = append(state.Operations, op)
		if err := save(); err != nil {
			persistenceErr = err
			return nil, err
		}
		raw, err := call()
		if len(raw) > 262144 || !json.Valid(raw) || strings.TrimSpace(string(raw)) == "null" {
			raw = nil
			err = fmt.Errorf("tool result invalid or exceeds limit")
		}
		state.Operations[n].Finished = true
		state.Operations[n].Result = raw
		if err != nil {
			state.Operations[n].Error = name + "_failed"
			state.Operations[n].Result = nil
		}
		if saveErr := save(); saveErr != nil {
			persistenceErr = saveErr
			return nil, saveErr
		}
		return raw, err
	}
	durable := agent.Tools{Scope: tools.Scope}
	if tools.Lookup != nil {
		durable.Lookup = func(ctx context.Context, instruction string) (json.RawMessage, error) {
			return record(ctx, "lookup", "", instruction, func() (json.RawMessage, error) { return tools.Lookup(ctx, instruction) })
		}
	}
	if tools.Propose != nil {
		durable.Propose = func(ctx context.Context, proposal agent.Proposal) (agent.ProposalReceipt, error) {
			input, err := json.Marshal(proposal)
			if err != nil {
				return agent.ProposalReceipt{}, err
			}
			raw, err := record(ctx, "propose", "", string(input), func() (json.RawMessage, error) {
				receipt, err := tools.Propose(ctx, proposal)
				if err != nil {
					return nil, err
				}
				if err = receipt.Validate(); err != nil {
					return nil, err
				}
				return json.Marshal(receipt)
			})
			if err != nil {
				return agent.ProposalReceipt{}, err
			}
			var receipt agent.ProposalReceipt
			err = json.Unmarshal(raw, &receipt)
			return receipt, err
		}
	}
	for _, capability := range tools.Capabilities {
		binding, err := tools.Binding(capability.Name)
		if err != nil {
			return agent.Result{}, err
		}
		original := capability
		original.Call = func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
			return record(ctx, original.Name, binding, string(input), func() (json.RawMessage, error) { return capability.Call(ctx, input) })
		}
		durable.Capabilities = append(durable.Capabilities, original)
	}
	return agent.RunAttemptWithTools(ctx, spec, client, durable, func(e agent.Event) error {
		if persistenceErr != nil {
			return persistenceErr
		}
		state.Events = append(state.Events, e)
		if e.Result != nil {
			state.Result = e.Result
		}
		if err := save(); err != nil {
			persistenceErr = err
			return err
		}
		return emit(e)
	}, prior)
}

func saveCheckpoint(root, path string, state checkpoint) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return fmt.Errorf("checkpoint limit exceeded")
	}
	tmp, err := os.CreateTemp(root, ".checkpoint-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp.Name(), path+".json"); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// continuation is shared by inspection and execution; eligibility is not authorization.
func continuation(state checkpoint) (agent.Continuation, error) {
	prior := agent.Continuation{}
	prior.Attempt = 2
	if len(state.Events) == 0 {
		prior.Attempt = 1
	}
	ended := map[uint64]bool{}
	started := 0
	for _, event := range state.Events {
		if event.Type == "run.resumed" {
			prior.Attempt++
		}
		if event.Type == "tool.started" {
			started++
		}
		if event.Type == "tool.finished" {
			ended[event.OperationID] = true
		}
		if event.SpanID > prior.SpanID {
			prior.SpanID = event.SpanID
		}
	}
	if prior.Attempt > 3 {
		return agent.Continuation{}, fmt.Errorf("continuation attempt limit exceeded")
	}
	if started != len(state.Operations) || len(ended) != started {
		return agent.Continuation{}, fmt.Errorf("unresolved tool results prevent continuation")
	}
	for _, op := range state.Operations {
		if !op.Finished {
			return agent.Continuation{}, fmt.Errorf("unknown operation prevents continuation")
		}
	}
	prior.Sequence = uint64(len(state.Events))
	prior.Operations = append([]agent.SavedOperation(nil), state.Operations...)
	return prior, nil
}
