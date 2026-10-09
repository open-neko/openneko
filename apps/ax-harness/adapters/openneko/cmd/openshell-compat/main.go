// Build as `openshell` on the harness-owned worker PATH; preserve the real CLI separately.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/adapters/openneko/internal/openshellcompat"
)

func main() {
	if err := run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "openshell compatibility adapter:", err)
		os.Exit(1)
	}
}
func run() error {
	real := os.Getenv("HARNESS_OPENSHELL_BIN")
	if !filepath.IsAbs(real) {
		return fmt.Errorf("HARNESS_OPENSHELL_BIN must name the absolute path of the real 0.0.116 CLI")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	selfInfo, err := os.Stat(self)
	if err != nil {
		return err
	}
	realInfo, err := os.Stat(real)
	if err != nil {
		return fmt.Errorf("real CLI is unavailable")
	}
	if os.SameFile(selfInfo, realInfo) {
		return fmt.Errorf("real CLI points to this adapter")
	}
	// Pin this behavior to the qualified release. Do not infer compatibility with
	// an older gateway/CLI tuple or silently install/upgrade anything.
	version, err := exec.Command(real, "--version").Output()
	if err != nil || string(version) != "openshell 0.0.116\n" {
		return fmt.Errorf("requires the qualified OpenShell 0.0.116 CLI")
	}

	plan, err := openshellcompat.Prepare(os.Args[1:])
	if err != nil {
		return err
	}
	if len(plan.Commands) == 1 {
		return syscall.Exec(real, append([]string{real}, plan.Commands[0]...), os.Environ())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for index, args := range plan.Commands {
		child := exec.CommandContext(ctx, real, args...)
		child.Stdin = os.Stdin
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Run(); err != nil {
			if index > 0 {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				cleanup := exec.CommandContext(cleanupCtx, real, plan.Cleanup...)
				cleanup.Stderr = os.Stderr
				cleanupErr := cleanup.Run()
				cancel()
				if cleanupErr != nil {
					return fmt.Errorf("upload failed and sandbox cleanup failed; inspect the requested sandbox")
				}
			}
			return err
		}
	}
	return nil
}
