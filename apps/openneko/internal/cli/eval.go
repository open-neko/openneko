package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var runEvalProcess = func(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) (int, error) {
	path, err := exec.LookPath("pnpm")
	if err != nil {
		return 1, errors.New("eval requires pnpm on PATH (run from an OpenNeko source checkout)")
	}
	child := exec.CommandContext(ctx, path, append([]string{"--filter", "@neko/evals", "eval"}, args...)...)
	workspaceRoot := os.Getenv("OPENNEKO_WORKSPACE_ROOT")
	if workspaceRoot == "" {
		workspaceRoot, err = os.Getwd()
		if err != nil {
			return 1, fmt.Errorf("resolve workspace: %w", err)
		}
	}
	child.Dir = workspaceRoot
	child.Env = append(os.Environ(), "OPENNEKO_WORKSPACE_ROOT="+workspaceRoot)
	child.Stdout = stdout
	child.Stderr = stderr
	if err := child.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

func newEvalCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "eval <validate|plan|run|resume|rescore|verify|report|compare> [flags]",
		Short:              "Run and inspect reproducible harness evaluations",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runEvalProcess(cmd.Context(), args, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return fmt.Errorf("eval: %w", err)
			}
			if code != 0 {
				return WithExit(code, nil)
			}
			return nil
		},
	}
}
