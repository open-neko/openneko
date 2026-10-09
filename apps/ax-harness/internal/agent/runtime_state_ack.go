package agent

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	ax "github.com/ax-llm/ax/packages/go"
)

// stateAcknowledgements correlates Ax's queued control ID with the operation
// that produced a committed host snapshot. A successful Steer only queues the
// update; Ax emits applied when a matching model boundary consumes it.
type stateAcknowledgements struct {
	steerMu sync.Mutex
	active  atomic.Uint64
	mu      sync.Mutex
	queued  map[string]stateQueueEntry
}

type stateQueueEntry struct {
	operationID uint64
	target      string
}

func newStateAcknowledgements(control *ax.AxRunControl, events *recorder) *stateAcknowledgements {
	a := &stateAcknowledgements{queued: make(map[string]stateQueueEntry)}
	control.OnEvent(func(event map[string]ax.Value) {
		kind, _ := event["type"].(string)
		id := fmt.Sprint(event["update_id"])
		switch kind {
		case "queued":
			if op := a.active.Load(); op != 0 {
				target, _ := event["path"].(string)
				a.mu.Lock()
				a.queued[id] = stateQueueEntry{operationID: op, target: target}
				a.mu.Unlock()
			}
		case "applied":
			a.mu.Lock()
			entry, ok := a.queued[id]
			if ok {
				delete(a.queued, id)
			}
			a.mu.Unlock()
			if !ok {
				return
			}
			path, _ := event["path"].(string)
			if path != entry.target {
				events.failRuntimeState(entry.operationID)
				return
			}
			timing, _ := event["timing"].(string)
			if timing != "next-response" && timing != "native" {
				events.failRuntimeState(entry.operationID)
				return
			}
			events.send(Event{Type: "runtime.state.applied", OperationID: entry.operationID, Origin: timing})
		}
	})
	return a
}

func (a *stateAcknowledgements) steer(control *ax.AxRunControl, operationID uint64, update RuntimeStateUpdate) error {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	a.active.Store(operationID)
	defer a.active.Store(0)
	return control.Steer(string(update.State), update.Target)
}

func (a *stateAcknowledgements) unapplied() []uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	seen := make(map[uint64]bool, len(a.queued))
	var ids []uint64
	for _, entry := range a.queued {
		if !seen[entry.operationID] {
			ids = append(ids, entry.operationID)
			seen[entry.operationID] = true
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// A tool-less finalizer cannot consume an Ax responder update. It may only
// supersede pending updates from the exact committed receipts admitted as
// finalizer evidence. Other pending updates remain a terminal failure.
func (a *stateAcknowledgements) supersedeForFinalizer(evidenceIDs []int, events *recorder) {
	allowed := make(map[uint64]bool, len(evidenceIDs))
	for _, id := range evidenceIDs {
		allowed[uint64(id)] = true
	}
	a.mu.Lock()
	ids := make([]uint64, 0, len(a.queued))
	for key, entry := range a.queued {
		if allowed[entry.operationID] {
			ids = append(ids, entry.operationID)
			delete(a.queued, key)
		}
	}
	a.mu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		events.send(Event{Type: "runtime.state.superseded", OperationID: id, Origin: "terminal_finalizer"})
	}
}
