//go:build !windows

package ownonly

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
