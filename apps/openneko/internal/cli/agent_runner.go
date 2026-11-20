package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

const agentRunnerCmd = "agent-runner"

func newAgentRunnerCmd() *cobra.Command {
	return &cobra.Command{
		Use:    agentRunnerCmd,
		Short:  "Run one agent turn for openneko serve, on the links that serve passes",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := native.NotifyStop(cmd.Context())
			defer stop()
			return serve.RunRunner(ctx, confineRunner)
		},
	}
}

// agentRunner is the command and environment of serve's agent runner. The
// runner gets no variable of serve's except PATH and TMPDIR.
func agentRunner() ([]string, []string, error) {
	if !serve.RunnerSupported {
		return nil, nil, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	var env []string
	for _, name := range []string{"PATH", "TMPDIR"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return []string{exe, agentRunnerCmd}, env, nil
}

// runnerSystemReads are the files that a runner reads after it confines
// itself: the time zone data.
var runnerSystemReads = []string{"/etc/localtime", "/usr/share/zoneinfo"}

// runnerSpec keeps the folders of one turn and the time zone data. The
// runner runs no program and opens no socket.
func runnerSpec(folders []string) confine.Spec {
	return confine.Spec{Read: runnerSystemReads, ReadWrite: folders}
}

func confineRunner(folders []string) error {
	if len(folders) == 0 {
		return fmt.Errorf("the turn names no workspace folder")
	}
	_, err := confine.ApplyRunner(runnerSpec(folders))
	return err
}
