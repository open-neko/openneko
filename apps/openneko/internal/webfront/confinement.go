package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func confinementService() externalService {
	return externalService{
		id: "agent-confinement", label: "Agent confinement", timeout: 5 * time.Second,
		whenOff: "Agent turns are off: OPENNEKO_SERVE_SOCKET is not set.",
		settings: func(context.Context, string) (*serviceSettings, error) {
			socket := strings.TrimSpace(os.Getenv("OPENNEKO_SERVE_SOCKET"))
			if socket == "" {
				return nil, nil
			}
			return &serviceSettings{url: "unix://" + socket, where: "openneko serve"}, nil
		},
		check: func(ctx context.Context, _ string, s *serviceSettings) (string, string, error) {
			st, runner, err := serveConfinement(ctx, strings.TrimPrefix(s.url, "unix://"))
			if err != nil {
				return "", "", err
			}
			if runner.turnsOff != "" {
				return "", "", errors.New(runner.turnsOff)
			}
			if !runner.state.Confined && runner.state.Mechanism != "" {
				return "", "", fmt.Errorf("agent runners run unconfined: %s", runner.state.Reason)
			}
			if !st.Confined {
				if st.Mechanism == "" {
					return fmt.Sprintf("Confinement of openneko serve on %s is not built yet: %s.", runtime.GOOS, st.Reason), "", nil
				}
				return "", "", fmt.Errorf("openneko serve runs unconfined: %s", st.Reason)
			}
			detail := fmt.Sprintf("openneko serve runs under %s: it cannot read the config folder or other files of this account.", st)
			if len(st.Gaps) > 0 {
				detail += " This kernel cannot refuse: " + strings.Join(st.Gaps, "; ") + "."
			}
			if runner.state.Confined {
				detail += fmt.Sprintf(" Each agent turn runs in a runner under %s: it opens no socket and reaches only the files of its turn.", runner.state)
			} else if runner.state.Reason != "" {
				detail += " Agent runners: " + runner.state.Reason + "."
			}
			return detail, "", nil
		},
	}
}

type runnerState struct {
	state    confine.State
	turnsOff string
}

// serveConfinement asks serve over its agent socket how the OS confines it
// and its agent runners.
func serveConfinement(ctx context.Context, socket string) (confine.State, runnerState, error) {
	var st confine.State
	var runner runnerState
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return st, runner, errors.New("openneko serve did not answer on its agent socket")
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	var reply serve.Frame
	if err := json.NewEncoder(conn).Encode(serve.Frame{Type: "status"}); err == nil {
		err = json.NewDecoder(conn).Decode(&reply)
	}
	var status serve.Status
	if reply.Type != "status" || json.Unmarshal(reply.Result, &status) != nil || len(status.Confinement) == 0 || json.Unmarshal(status.Confinement, &st) != nil {
		return st, runner, errors.New("openneko serve did not report its confinement; run serve from the same release as web")
	}
	if len(status.RunnerConfinement) > 0 {
		_ = json.Unmarshal(status.RunnerConfinement, &runner.state)
	}
	runner.turnsOff = status.TurnsOff
	return st, runner, nil
}
