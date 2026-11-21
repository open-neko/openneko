//go:build !windows

package cli

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/secretenv"
)

var supervisorArgs = func(exe string) []string { return []string{exe, "native-run"} }

// becomeSupervisor replaces `openneko start` with `openneko native-run`,
// so that the supervisor keeps neither the shell's secrets nor
// --database-url in the arguments and environment that the OS shows to
// another process. The secrets go on stdin through an unlinked file.
func becomeSupervisor(layout native.Layout, opts native.Options) error {
	clean, secrets := supervisorEnv(opts.Env)
	f, err := os.CreateTemp(layout.Run(), ".supervisor-*")
	if err != nil {
		return err
	}
	defer f.Close()
	if err := os.Remove(f.Name()); err != nil {
		return err
	}
	if _, err := f.Write(secretenv.Line(secrets)); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	if err := unix.Dup2(int(f.Fd()), 0); err != nil {
		return fmt.Errorf("hand the supervisor its secrets: %w", err)
	}
	return syscall.Exec(opts.Exe, supervisorArgs(opts.Exe), clean)
}
