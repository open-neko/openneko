//go:build !windows

package cli

import "os"

func replaceInPlace(next, dst string) error { return os.Rename(next, dst) }
