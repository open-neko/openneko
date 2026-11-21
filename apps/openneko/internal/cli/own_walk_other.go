//go:build !linux

package cli

import (
	"errors"
	"os"
)

var errServerLinuxOnly = errors.New("a server install is for Linux only")

func ownDir(string, string, int, int, os.FileMode) error { return errServerLinuxOnly }

func ownTreeNoFollow(string, int, int, map[string]bool) error { return errServerLinuxOnly }
