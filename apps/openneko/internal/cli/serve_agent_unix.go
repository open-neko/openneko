//go:build !windows

package cli

import (
	"context"
	"fmt"
	"io"
	"os"

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
	} else {
		fmt.Fprintln(out, "agent terminal is off: no sandbox URL")
	}
	srv := &serve.Server{Getenv: os.Getenv, MaxTurns: maxTurns, Sandbox: box}
	done := make(chan error, 1)
	go func() {
		err := srv.Serve(ctx, ln)
		_ = os.Remove(socket)
		done <- err
	}()
	return done, nil
}
