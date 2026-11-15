package webfront

import (
	"context"
	"math"
	"os"
	"time"
)

func agentConcurrencyOverride() (int, bool) {
	n, ok := jsNumber(os.Getenv("OPENNEKO_AGENT_CONCURRENCY"))
	if !ok || n < 1 || n != math.Trunc(n) || n > math.MaxInt32 {
		return 0, false
	}
	return int(n), true
}

func (s *agentSlots) setLimit(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limit = n
	s.admitNextLocked()
}

var (
	agentConcurrencyRefresh   = 30 * time.Second
	afterAgentConcurrencyRead = func() {}
)

// ApplyAgentConcurrency sets the number of agent turns that run at once from
// the stored "Concurrent jobs" setting of the install's org.
// OPENNEKO_AGENT_CONCURRENCY overrides it.
func (a *API) ApplyAgentConcurrency(ctx context.Context) (int, error) {
	a.slots.applyMu.Lock()
	defer a.slots.applyMu.Unlock()
	if n, ok := agentConcurrencyOverride(); ok {
		a.slots.setLimit(n)
		return n, nil
	}
	orgID, err := a.existingOrgID(ctx)
	if err != nil {
		return 0, err
	}
	cfg := map[string]any{}
	if orgID != "" {
		if _, cfg, _, err = a.loadAgentRow(ctx, orgID); err != nil {
			return 0, err
		}
	}
	n := positiveCap(cfg["globalCap"], agentDefaultGlobalCap)
	afterAgentConcurrencyRead()
	a.slots.setLimit(n)
	return n, nil
}

// StartAgentConcurrencyRefresh reads the setting again on a timer, so a save
// in one openneko web reaches every other process on the same database.
func (a *API) StartAgentConcurrencyRefresh(ctx context.Context, logf func(string, ...any)) {
	go func() {
		tick := time.NewTicker(agentConcurrencyRefresh)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, err := a.ApplyAgentConcurrency(ctx); err != nil && ctx.Err() == nil {
					logf("openneko web: the Concurrent jobs setting was not read: %v", err)
				}
			}
		}
	}()
}

// AgentTurnLimit is the number of agent turns that run at once now; 0 until
// the first apply.
func (a *API) AgentTurnLimit() int {
	a.slots.mu.Lock()
	defer a.slots.mu.Unlock()
	return a.slots.limit
}
