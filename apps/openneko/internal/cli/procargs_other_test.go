//go:build !darwin && !windows

package cli

import "errors"

func darwinProcargs(int) ([]byte, error) { return nil, errors.New("not macOS") }
