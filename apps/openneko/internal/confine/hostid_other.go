//go:build !linux && !darwin && !windows

package confine

func machineID() string { return "" }
