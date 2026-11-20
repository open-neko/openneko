package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"

	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func serveAgentTurns(ctx context.Context, out io.Writer, socket string, maxTurns int, box sandbox.Config, confinement confine.State) (<-chan error, error) {
	if err := box.Check(); err != nil {
		return nil, err
	}
	ln, err := serve.Listen(socket)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "openneko serve runs agent turns on %s\n", socket)
	if box.Enabled() {
		fmt.Fprintf(out, "agent terminal commands go to the sandbox at %s\n", box.URL)
		if box.SharesFolder(socket) {
			fmt.Fprintf(out, "warning: the sandbox socket is in the folder of %s; give the sandbox a socket folder of its own (INSTALL.md, \"Sandbox\")\n", socket)
		}
	} else {
		fmt.Fprintln(out, "agent terminal is off: no sandbox URL")
	}
	names := serve.KeyEnvNames(os.Environ())
	if len(names) > 0 {
		fmt.Fprintf(out, "warning: openneko serve drops %s from its environment; model keys, database URLs and the secret key belong to openneko web (INSTALL.md, \"Model keys\")\n", strings.Join(names, ", "))
		for _, name := range names {
			_ = os.Unsetenv(name)
		}
	}
	door := keydoor.SocketPath(os.Getenv, socket)
	keydoor.SetSocket(door)
	fmt.Fprintf(out, "openneko serve reaches the model providers through the key door at %s\n", door)
	state, _ := json.Marshal(confinement)
	runnerState, confineRunner, turnsOff := runnerConfinement(out, webfront.Development())
	runnerJSON, _ := json.Marshal(runnerState)
	runner, runnerEnv, err := agentRunner()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	if runner != nil {
		fmt.Fprintln(out, "openneko serve runs each agent turn in an agent runner process of its own")
	}
	srv := &serve.Server{Getenv: os.Getenv, MaxTurns: maxTurns, Sandbox: box, RequireDoor: !webfront.Development(), KeyEnv: names, Confinement: state,
		Runner: runner, RunnerEnv: runnerEnv, RunnerConfinement: runnerJSON, ConfineRunner: confineRunner, TurnsOff: turnsOff}
	done := make(chan error, 1)
	go func() {
		err := srv.Serve(ctx, ln)
		_ = os.Remove(socket)
		done <- err
	}()
	return done, nil
}

func serveConfigCheck(ctx context.Context, out io.Writer, socket, configFile string) (<-chan error, error) {
	ln, err := serve.Listen(socket)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "openneko serve checks GraphJin configs on %s\n", socket)
	srv := &http.Server{Handler: gjhost.CheckHandler(configFile), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		err := srv.Serve(ln)
		_ = os.Remove(socket)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	return done, nil
}
