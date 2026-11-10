//go:build windows

package cli

import (
	"context"
	"errors"
	"io"
)

func serveAgentTurns(context.Context, io.Writer, string, int) (<-chan error, error) {
	return nil, errors.New("agent turns need a Unix socket; Windows is not supported yet")
}
