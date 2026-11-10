package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/open-neko/neko/apps/openneko/internal/compose"
)

type startStep struct {
	service  string
	withDeps bool
	failure  string
}

func startSteps(mode compose.Mode) []startStep {
	steps := []startStep{
		{service: "storage-reconcile", withDeps: true, failure: "database storage reconciliation failed"},
		{service: "neko-migrate", failure: "database preparation failed"},
		{service: "openneko-config-init", failure: "config volume preparation failed"},
	}
	if mode == compose.ModeDemo {
		steps = append(steps,
			startStep{service: "adventureworks-init", withDeps: true, failure: "AdventureWorks data load failed"},
			startStep{service: "neko-adventureworks-seed", failure: "AdventureWorks seed failed"},
		)
	}
	return append(steps, startStep{service: "openneko-serve-init", failure: "openneko serve preparation failed"})
}

// Releases before the start steps ran these services inside `up`, which left
// exited containers that `up --remove-orphans` does not remove.
func legacyStepContainers(mode compose.Mode) []string {
	services := []string{"storage-reconcile", "neko-migrate", "openneko-config-init", "openneko-serve-init"}
	if mode == compose.ModeDemo {
		services = append(services, "neko-adventureworks-seed")
	}
	return services
}

func startStepArgs(step startStep, runFlags []string) []string {
	args := []string{"run", "--rm", "-T"}
	if !step.withDeps {
		args = append(args, "--no-deps")
	}
	args = append(args, runFlags...)
	return append(args, step.service)
}

func runStartSteps(ctx context.Context, sup *compose.Supervisor, project string, files []string, mode compose.Mode, runFlags []string) error {
	removeArgs := append([]string{"rm", "--force", "--stop"}, legacyStepContainers(mode)...)
	if code, err := sup.Run(ctx, project, files, removeArgs, os.Stdout, os.Stderr); err != nil {
		return err
	} else if code != 0 {
		return WithExit(code, fmt.Errorf("could not remove the old start-step containers"))
	}
	for _, step := range startSteps(mode) {
		code, err := sup.Run(ctx, project, files, startStepArgs(step, runFlags), os.Stdout, os.Stderr)
		if err != nil {
			return err
		}
		if code != 0 {
			return WithExit(code, fmt.Errorf("%s (step %s)", step.failure, step.service))
		}
	}
	return nil
}
