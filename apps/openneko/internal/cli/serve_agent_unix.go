//go:build !windows

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"

	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func serveAgentTurns(ctx context.Context, out io.Writer, socket string, maxTurns int, box sandbox.Config) (<-chan error, error) {
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
	if names := serve.KeyEnvNames(os.Environ()); len(names) > 0 {
		fmt.Fprintf(out, "warning: openneko serve drops %s from its environment; model keys belong to openneko web (INSTALL.md, \"Model keys\")\n", strings.Join(names, ", "))
		for _, name := range names {
			_ = os.Unsetenv(name)
		}
	}
	door := keydoor.SocketPath(os.Getenv, socket)
	keydoor.SetSocket(door)
	fmt.Fprintf(out, "openneko serve reaches the model providers through the key door at %s\n", door)
	srv := &serve.Server{Getenv: os.Getenv, MaxTurns: maxTurns, Sandbox: box, RequireDoor: !webfront.Development()}
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
