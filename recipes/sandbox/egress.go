package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// egressState reports "closed" when the sandbox's network namespace has no
// default route, so a command reaches only its own network, and "open" when
// it has one or the route tables cannot be read.
func egressState(procNet string) string {
	v4, err := os.Open(filepath.Join(procNet, "route"))
	if err != nil {
		return "open"
	}
	defer v4.Close()
	lines := bufio.NewScanner(v4)
	for lines.Scan() {
		f := strings.Fields(lines.Text())
		if len(f) >= 8 && f[1] == "00000000" && f[7] == "00000000" {
			return "open"
		}
	}
	if lines.Err() != nil {
		return "open"
	}
	v6, err := os.Open(filepath.Join(procNet, "ipv6_route"))
	if os.IsNotExist(err) {
		return "closed"
	}
	if err != nil {
		return "open"
	}
	defer v6.Close()
	lines = bufio.NewScanner(v6)
	for lines.Scan() {
		f := strings.Fields(lines.Text())
		if len(f) >= 10 && f[0] == strings.Repeat("0", 32) && f[1] == "00" && !rejectRoute(f[8]) {
			return "open"
		}
	}
	if lines.Err() != nil {
		return "open"
	}
	return "closed"
}

func rejectRoute(flags string) bool {
	n, err := strconv.ParseUint(flags, 16, 32)
	return err == nil && n&0x0200 != 0
}
