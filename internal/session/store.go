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

type operation struct {
	ID          int             `json:"id"`
	Instruction string          `json:"instruction"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	Finished    bool            `json:"finished"`
}
type checkpoint struct {
	Version    int           `json:"version"`
	Spec       agent.Spec    `json:"spec"`
	Events     []agent.Event `json:"events"`
	Operations []operation   `json:"operations"`
	Result     *agent.Result `json:"result,omitempty"`
}

// Run requires a trusted, consumer-scoped local directory. It rejects concurrent
// execution, conflicting input and unfinished previous attempts. Completed runs
// replay stored events without recontacting models or tools. Crash continuation is
// deliberately disabled; completed read evidence remains in the checkpoint.
func Run(ctx context.Context, root string, spec agent.Spec, client ax.AIClient, lookup func(context.Context, string) (json.RawMessage, error), emit func(agent.Event) error) (agent.Result, error) {
	if root == "" || spec.Version != 1 || strings.TrimSpace(spec.RunID) == "" || strings.TrimSpace(spec.InputID) == "" || strings.TrimSpace(spec.Prompt) == "" || len(spec.Prompt) > 65536 || len(spec.RunID) > 128 || len(spec.InputID) > 128 || emit == nil {
		return agent.Result{}, fmt.Errorf("invalid persistent run")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return agent.Result{}, err
	}
	sum := sha256.Sum256([]byte(spec.RunID))
	path := filepath.Join(root, hex.EncodeToString(sum[:]))
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return agent.Result{}, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return agent.Result{}, fmt.Errorf("run already executing")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state := checkpoint{Version: 1, Spec: spec}
	file, err := os.Open(path + ".json")
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(file, (8<<20)+1))
		file.Close()
	}
	if err == nil {
		state, err = decodeCheckpoint(data, spec)
		if err != nil {
			return agent.Result{}, err
		}
		if state.Result == nil {
			return agent.Result{}, fmt.Errorf("interrupted run requires reconciliation; stored operations retained")
		}
		for _, e := range state.Events {
			if err := emit(e); err != nil {
				return agent.Result{}, err
			}
		}
		return *state.Result, nil
	}
	if !os.IsNotExist(err) {
		return agent.Result{}, err
	}
	save := func() error {
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
	if err := save(); err != nil {
		return agent.Result{}, err
	}
	var durableLookup func(context.Context, string) (json.RawMessage, error)
	var persistenceErr error
	if lookup != nil {
		durableLookup = func(ctx context.Context, instruction string) (json.RawMessage, error) {
			if persistenceErr != nil {
				return nil, persistenceErr
			}
			n := len(state.Operations)
			state.Operations = append(state.Operations, operation{ID: n + 1, Instruction: instruction})
			if err := save(); err != nil {
				persistenceErr = err
				return nil, err
			}
			raw, err := lookup(ctx, instruction)
			if len(raw) > 262144 || !json.Valid(raw) || strings.TrimSpace(string(raw)) == "null" {
				raw = nil
				err = fmt.Errorf("lookup result invalid or exceeds limit")
			}
			state.Operations[n].Finished = true
			state.Operations[n].Result = raw
			if err != nil {
				state.Operations[n].Error = "lookup_failed"
				state.Operations[n].Result = nil
			}
			if saveErr := save(); saveErr != nil {
				persistenceErr = saveErr
				return nil, saveErr
			}
			return raw, err
		}
	}
	return agent.Run(ctx, spec, client, durableLookup, func(e agent.Event) error {
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
	})
}
