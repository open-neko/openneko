// Package cli wires the cobra command tree.
//
// Exit code reference:
//
//	0  ok
//	1  generic error
//	2  usage error (missing args, unknown flag)
//	3  host not supported for microsandbox
package cli

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/dockerproxy"
	"github.com/open-neko/neko/apps/openneko/internal/version"
)

// MaybeProxyToWorker is set by plugin-op commands; if a worker container is
// running and --local wasn't passed, the command re-executes inside the
// worker via docker exec. Defined here so subcommand files can call it
// consistently. Returns (exitCode, true) when proxied, (0, false) when the
// caller should fall through to the local implementation.
func MaybeProxyToWorker(cmd *cobra.Command) (int, bool) {
	if local, _ := cmd.Flags().GetBool("local"); local {
		return 0, false
	}
	container := dockerproxy.FindRunningWorker()
	if container == "" {
		return 0, false
	}
	return dockerproxy.ProxyToWorker(container, os.Args[1:]), true
}

type exitErr struct {
	code int
	err  error
}

func (e *exitErr) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *exitErr) Unwrap() error { return e.err }

func WithExit(code int, err error) error {
	return &exitErr{code: code, err: err}
}

// ExitCodeFor returns the exit code carried by err, or 0 if no exitErr wraps it.
func ExitCodeFor(err error) int {
	var e *exitErr
	if errors.As(err, &e) {
		return e.code
	}
	return 0
}

func NewRoot() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "openneko",
		Short: "OpenNeko operator CLI",
		Long: `openneko — supervises the OpenNeko stack and manages plugins.

Plugin ops: init, install, list, remove, marketplace, secrets, doctor.
Stack ops:  start, stop, logs, status, migrate, seed, reset.`,
		Version: version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	// Persistent flag: plugin-op commands (init/install/remove/list/
	// marketplace/secrets) auto-proxy into a running openneko-*-worker-1
	// container so the brew-installed binary on the host can manage
	// plugins for an operator-side docker compose stack. --local forces
	// host-side execution (use this for source-build dev workflows that
	// happen to have a compose stack running alongside `pnpm dev`).
	cmd.PersistentFlags().Bool("local", false, "Force local execution; don't auto-proxy plugin ops into a running worker container")
	cmd.AddCommand(
		newInitCmd(),
		newInstallCmd(),
		newRemoveCmd(),
		newListCmd(),
		newDoctorCmd(),
		newMarketplaceCmd(),
		newSecretsCmd(),
		newVersionCmd(),
		newStartCmd(),
		newStopCmd(),
		newStatusCmd(),
		newLogsCmd(),
		newMigrateCmd(),
		newSeedCmd(),
		newResetCmd(),
	)
	return cmd
}
