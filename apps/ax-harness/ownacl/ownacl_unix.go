//go:build !windows

package ownacl

import (
	"fmt"
	"os"
)

// Set does nothing on Unix: the callers set the file modes.
func Set(string) error { return nil }

// Verify returns an error when the group or others have any mode bit on
// path.
func Verify(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s mode %v is open to others", path, info.Mode().Perm())
	}
	return nil
}

// SocketFolder does nothing on Unix: the listeners set the socket mode, and
// a shared group folder is part of the two-account install.
func SocketFolder(string) error { return nil }
