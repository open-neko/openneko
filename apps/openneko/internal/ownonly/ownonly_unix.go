//go:build !windows

package ownonly

// Set does nothing on Unix: the callers set the file modes.
func Set(string) error { return nil }
