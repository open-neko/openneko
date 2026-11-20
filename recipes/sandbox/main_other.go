//go:build !linux

package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr, "openneko-sandbox runs on Linux only, not on %s; run its image in Docker\n", runtime.GOOS)
	os.Exit(1)
}
