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

func (a *API) confinementService() externalService {
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
			st, err := serveConfinement(ctx, strings.TrimPrefix(s.url, "unix://"))
			if err != nil {
				return "", "", err
			}
			if st.turnsOff != "" {
				setting, err := a.unconfinedTurnsSetting(ctx)
				if err != nil {
					return "", "", fmt.Errorf("%s; the admin setting for turns without confinement cannot be read: %w", st.turnsOff, err)
				}
				if setting.appliesTo(st) {
					return "", "", errors.New(setting.line())
				}
				if why := setting.notHere(st); why != "" {
					return "", "", fmt.Errorf("%s. The override for agent turns without OS confinement does not apply: %s", st.turnsOff, why)
				}
				return "", "", errors.New(st.turnsOff)
			}
			if !st.runner.Confined && st.runner.Mechanism != "" {
				return "", "", fmt.Errorf("agent runners run unconfined: %s", st.runner.Reason)
			}
			if !st.network.Confined && st.network.Mechanism != "" {
				return "", "", fmt.Errorf("the network part of openneko serve runs unconfined: %s", st.network.Reason)
			}
			if !st.host.Confined && st.host.Mechanism != "" {
				return "", "", fmt.Errorf("openneko serve runs unconfined: %s", st.host.Reason)
			}
			if !st.runner.Confined && st.production {
				return "", "", errors.New(confine.NotAvailable + ": agent turns run unconfined")
			}
			if !st.network.Confined {
				return fmt.Sprintf("Confinement of openneko serve on %s is not built yet: %s.", runtime.GOOS, st.network.Reason), "", nil
			}
			detail := fmt.Sprintf("The network part of openneko serve (files, GraphJin) runs under %s: it cannot read the config folder or other files of this account.", st.network)
			if len(st.network.Gaps) > 0 {
				detail += " This system cannot refuse: " + strings.Join(st.network.Gaps, "; ") + "."
			}
			if st.host.Confined {
				detail += fmt.Sprintf(" The part that starts agent turns runs under %s.", st.host)
			} else if st.host.Reason != "" {
				detail += " The part that starts agent turns runs unconfined: " + st.host.Reason + "."
			}
			if st.runner.Confined {
				detail += fmt.Sprintf(" Each agent turn runs in a runner under %s: it opens no socket and reaches only the files of its turn.", st.runner)
			} else if st.runner.Reason != "" {
				detail += " Agent runners: " + st.runner.Reason + "."
			}
			return detail, "", nil
		},
	}
}

type serveStates struct {
	host, network, runner              confine.State
	turnsOff, hostName, hostID, kernel string
	production                         bool
}

// serveConfinement asks serve over its agent socket how the OS confines it
// and its agent runners.
func serveConfinement(ctx context.Context, socket string) (serveStates, error) {
	var st serveStates
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return st, errors.New("openneko serve did not answer on its agent socket")
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
	if reply.Type != "status" || json.Unmarshal(reply.Result, &status) != nil || len(status.Confinement) == 0 || json.Unmarshal(status.Confinement, &st.host) != nil ||
		len(status.NetworkConfinement) == 0 || json.Unmarshal(status.NetworkConfinement, &st.network) != nil {
		return st, errors.New("openneko serve did not report its confinement; run serve from the same release as web")
	}
	if len(status.RunnerConfinement) > 0 {
		_ = json.Unmarshal(status.RunnerConfinement, &st.runner)
	}
	st.turnsOff, st.production, st.hostName, st.hostID, st.kernel = status.TurnsOff, status.RequireDoor, status.Host, status.HostID, status.Kernel
	return st, nil
}
