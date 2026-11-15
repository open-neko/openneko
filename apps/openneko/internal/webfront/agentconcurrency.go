package webfront

import (
	"context"
	"math"
	"os"
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

// ApplyAgentConcurrency sets the number of agent turns that run at once from
// the stored "Concurrent jobs" setting of the install's org.
// OPENNEKO_AGENT_CONCURRENCY overrides it.
func (a *API) ApplyAgentConcurrency(ctx context.Context) (int, error) {
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
	a.slots.setLimit(n)
	return n, nil
}
