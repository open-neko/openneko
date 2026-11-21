package cli

import (
	"errors"

	"golang.org/x/sys/windows"

	"github.com/open-neko/neko/apps/openneko/internal/native"
)

// becomeSupervisor keeps the supervisor in this process: Windows has no exec
// that replaces a process.
func becomeSupervisor(native.Layout, native.Options) error { return nil }

func sharingViolation(err error) bool { return errors.Is(err, windows.ERROR_SHARING_VIOLATION) }
