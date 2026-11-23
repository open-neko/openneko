package webfront

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
)

// StartKeyDoor serves the key door beside serve's socket at start, so a
// socket fault stops web at once. With no serve socket the host calls run
// in this process and need no door.
func (a *API) StartKeyDoor(ctx context.Context, out io.Writer) (<-chan error, error) {
	socket := a.serveSocket()
	if socket == "" {
		return nil, nil
	}
	a.doorMu.Lock()
	defer a.doorMu.Unlock()
	path := keydoor.SocketPath(os.Getenv, socket)
	door := a.newKeyDoor()
	done, err := llm.ListenDoor(ctx, path, door)
	if err != nil {
		return nil, err
	}
	a.Door, a.doorPath = door, path
	fmt.Fprintf(out, "openneko web: the key door for serve's model calls is on %s\n", path)
	return done, nil
}

// keyDoor is the door for calls that serve makes on socket; it starts the
// door when StartKeyDoor did not.
func (a *API) keyDoor(socket string) (*llm.KeyDoor, error) {
	a.doorMu.Lock()
	defer a.doorMu.Unlock()
	if socket == "" {
		return nil, errors.New("the key door needs the serve socket")
	}
	path := keydoor.SocketPath(os.Getenv, socket)
	if a.Door != nil && a.doorPath == path {
		return a.Door, nil
	}
	door := a.newKeyDoor()
	if _, err := llm.ListenDoor(context.Background(), path, door); err != nil {
		return nil, err
	}
	a.Door, a.doorPath = door, path
	return door, nil
}

func (a *API) newKeyDoor() *llm.KeyDoor {
	return &llm.KeyDoor{Mismatch: a.doorMismatch, MaxOpen: a.doorOpenLimit}
}

// doorOpenLimit lets every agent turn that may run at once hold a token's
// full share of calls, beside the host and embedding calls.
func (a *API) doorOpenLimit() int {
	turns := max(a.AgentTurnLimit(), agentDefaultGlobalCap)
	return max(64, turns*llm.MaxGrantOpen+32)
}

// doorMismatch puts a call count that serve misreported in the org's audit
// chain. An embedding call belongs to no org, so only its log line stays.
func (a *API) doorMismatch(ctx context.Context, m llm.DoorMismatch) {
	if m.OrgID == "" || a.DB == nil {
		return
	}
	a.recordAudit(ctx, auditEvent{OrgID: m.OrgID, EntityKind: "key_door", EntityID: m.OrgID, Event: "agent:model_calls_mismatch",
		Payload: map[string]any{"purpose": m.Purpose, "doorCalls": m.Counted, "reportedCalls": m.Reported}})
}
