//go:build !windows

package cli

import (
	"io"

	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// runnerStarter is nil: each runner confines itself on this system.
func runnerStarter(io.Writer, string) func(serve.RunnerStart) (serve.RunnerProcess, error) {
	return nil
}
