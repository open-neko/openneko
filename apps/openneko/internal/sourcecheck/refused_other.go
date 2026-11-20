//go:build !windows

package sourcecheck

import (
	"errors"
	"syscall"
)

func refused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
