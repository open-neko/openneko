package webfront

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/secretenv"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

const sandboxEgressRecheck = 30 * time.Second

// OperatorEgressEnv says that the operator enforces the sandbox's egress
// outside it, for a sandbox on another host. Off unless it is "1" or "true".
const OperatorEgressEnv = "OPENNEKO_SANDBOX_EGRESS_BY_OPERATOR"

func OperatorEnforcesEgress() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(OperatorEgressEnv)))
	return v == "1" || v == "true"
}

type sandboxEgressGate struct {
	mu    sync.Mutex
	key   string
	state string
	err   error
	at    time.Time
}

// sandboxEgress is "off" with no sandbox, "operator" when OperatorEgressEnv
// is on, else what the sandbox reports: "closed", "open" or "unknown".
func (a *API) sandboxEgress(ctx context.Context, fresh bool) (string, error) {
	cfg := sandbox.ConfigFromEnv(secretenv.Get)
	if !cfg.Enabled() {
		return "off", nil
	}
	operator := OperatorEnforcesEgress()
	key := cfg.URL + "\x00" + cfg.Key
	if operator {
		key += "\x00operator"
	}
	g := &a.egressGate
	g.mu.Lock()
	defer g.mu.Unlock()
	if !fresh && g.key == key && time.Since(g.at) < sandboxEgressRecheck {
		return g.state, g.err
	}
	state, err := "operator", error(nil)
	if !operator {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		state, err = sandbox.ProbeEgress(probeCtx, cfg)
		cancel()
		if err != nil {
			state = "unknown"
		}
	}
	if g.key != key || g.state != state {
		a.recordEgressState(context.WithoutCancel(ctx), state)
	}
	g.key, g.state, g.err, g.at = key, state, err, time.Now()
	return state, err
}

func egressAllowsTerminal(state string) bool {
	return state == "closed" || state == "operator"
}

func terminalBlockedReason(state string) string {
	switch state {
	case "open":
		return `The terminal is off: the sandbox network has a route out. Run the sandbox on an internal network (INSTALL.md, "Sandbox").`
	case "unknown":
		return `The terminal is off: the sandbox does not report that its network is closed. Rebuild it from recipes/sandbox and run it on an internal network (INSTALL.md, "Sandbox").`
	}
	return ""
}

func (a *API) recordEgressState(ctx context.Context, state string) {
	if a.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := a.DB.Query(ctx, `select id from organization order by created_at, id`)
	if err != nil {
		log.Printf("sandbox egress state %s not audited: %v", state, err)
		return
	}
	orgs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		log.Printf("sandbox egress state %s not audited: %v", state, err)
		return
	}
	for _, org := range orgs {
		previous := "closed"
		err := a.DB.QueryRow(ctx, `select payload->>'next' from audit_chain
			where org_id = $1 and event = 'sandbox:egress_state' order by seq desc limit 1`, org).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("sandbox egress state %s not audited for org %s: %v", state, org, err)
			continue
		}
		if previous == state {
			continue
		}
		a.recordAudit(ctx, auditEvent{OrgID: org, EntityKind: "sandbox", EntityID: "sandbox", Event: "sandbox:egress_state",
			Payload: map[string]any{"previous": previous, "next": state, "terminal": egressAllowsTerminal(state)}})
	}
}
