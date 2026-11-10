//go:build !windows

package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func serveAgentTurns(ctx context.Context, out io.Writer, socket string, maxTurns int) (<-chan error, error) {
	ln, err := serve.Listen(socket)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "openneko serve runs agent turns on %s\n", socket)
	srv := &serve.Server{Getenv: os.Getenv, MaxTurns: maxTurns}
	done := make(chan error, 1)
	go func() {
		err := srv.Serve(ctx, ln)
		_ = os.Remove(socket)
		done <- err
	}()
	return done, nil
}
