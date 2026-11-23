package sourcecheck

import (
	"errors"

	"golang.org/x/sys/windows"
)

func refused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }
