// Package ownonly limits files and folders to the user that runs OpenNeko.
package ownonly

import "os"

// Mkdir makes dir and its parents with mode 0700 and limits dir to the
// current user.
func Mkdir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return Set(dir)
}
