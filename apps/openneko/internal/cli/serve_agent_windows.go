//go:build windows

package cli

import (
	"context"
	"errors"
	"io"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func serveAgentTurns(context.Context, io.Writer, string, int, sandbox.Config) (<-chan error, error) {
	return nil, errors.New("agent turns need a Unix socket; Windows is not supported yet")
}

func serveConfigCheck(context.Context, io.Writer, string, string) (<-chan error, error) {
	return nil, errors.New("the GraphJin config check needs a Unix socket; Windows is not supported yet")
}
