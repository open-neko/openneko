package serve

import (
	"bufio"
	"context"
	"errors"
	"io"
)

// RunnerSupported reports whether this system builds the agent runner.
const RunnerSupported = false

var errNoRunner = errors.New("the agent runner process is not built on Windows yet")

func (s *Server) runInRunner(context.Context, io.Writer, *bufio.Reader, Frame) error {
	return errNoRunner
}

// RunRunner is not built on Windows yet.
func RunRunner(context.Context) error { return errNoRunner }
