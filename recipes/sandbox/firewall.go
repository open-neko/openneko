//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	firewallTable = "openneko_sandbox"
	relayAddress  = "127.0.0.1:3128"
	maxUsers      = 4096
	maxUID        = 1<<31 - 2
	capSetPCap    = 8
	capNetAdmin   = 12
)

// firewallRules lets the users first to last send packets only to relay; a
// run's own loopback is closed too, because a loopback rule cannot tell one
// run's listener from another's.
func firewallRules(first, last int, relay string) (string, error) {
	host, port, err := net.SplitHostPort(relay)
	if err != nil || net.ParseIP(host).To4() == nil {
		return "", fmt.Errorf("the relay %q must be an IPv4 address and a port", relay)
	}
	if first <= 0 || last < first {
		return "", fmt.Errorf("invalid user range %d-%d", first, last)
	}
	users := fmt.Sprintf("%d-%d", first, last)
	return fmt.Sprintf(`add table inet %[1]s
delete table inet %[1]s
table inet %[1]s {
	chain output {
		type filter hook output priority 0; policy accept;
		meta skuid %[2]s ip daddr %[3]s tcp dport %[4]s accept
		meta skuid %[2]s drop
	}
}
`, firewallTable, users, host, port), nil
}

func setFirewall(rules string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %v: %s", err, strings.TrimSpace(string(out)))
	}
	out, err := exec.Command("nft", "list", "table", "inet", firewallTable).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "drop") {
		return fmt.Errorf("the rules are not in place after nft: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

type capHeader struct {
	version uint32
	pid     int32
}

type capData struct {
	effective, permitted, inheritable uint32
}

// dropNetworkCaps removes CAP_NET_ADMIN and CAP_SETPCAP from every thread's
// bounding, permitted and effective sets, so no later exec by root gets
// them back.
func dropNetworkCaps() error {
	for _, c := range []uintptr{capNetAdmin, capSetPCap} {
		if _, _, e := syscall.AllThreadsSyscall(syscall.SYS_PRCTL, syscall.PR_CAPBSET_DROP, c, 0); e != 0 {
			return fmt.Errorf("drop capability %d from the bounding set: %w", c, e)
		}
	}
	header := capHeader{version: 0x20080522}
	var data [2]capData
	if _, _, e := syscall.RawSyscall(syscall.SYS_CAPGET, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0); e != 0 {
		return fmt.Errorf("capget: %w", e)
	}
	mask := ^uint32(1<<capNetAdmin | 1<<capSetPCap)
	data[0].effective &= mask
	data[0].permitted &= mask
	data[0].inheritable &= mask
	header.pid = 0
	if _, _, e := syscall.AllThreadsSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0); e != 0 {
		return fmt.Errorf("capset: %w", e)
	}
	return checkCapsDropped("/proc/self/task")
}

func checkCapsDropped(tasks string) error {
	entries, err := os.ReadDir(tasks)
	if err != nil {
		return err
	}
	for _, e := range entries {
		f, err := os.Open(filepath.Join(tasks, e.Name(), "status"))
		if err != nil {
			return err
		}
		err = capsWithout(f, capNetAdmin, capSetPCap)
		f.Close()
		if err != nil {
			return fmt.Errorf("thread %s: %w", e.Name(), err)
		}
	}
	return nil
}

func capsWithout(status io.Reader, caps ...uint) error {
	seen := 0
	lines := bufio.NewScanner(status)
	for lines.Scan() {
		name, value, ok := strings.Cut(lines.Text(), ":")
		if !ok || (name != "CapEff" && name != "CapPrm" && name != "CapBnd" && name != "CapAmb") {
			continue
		}
		seen++
		set, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		for _, c := range caps {
			if set&(1<<c) != 0 {
				return fmt.Errorf("%s still holds capability %d", name, c)
			}
		}
	}
	if seen < 3 {
		return errors.New("the capability sets are not readable")
	}
	return lines.Err()
}

// confine closes the network of the run users and the probe user, starts
// the relay to the egress proxy, and then gives up the capabilities that
// could change the rules.
func (s *Server) confine(relay net.Listener) error {
	if err := s.checkUserRange(); err != nil {
		return err
	}
	first, _ := s.runUIDs()
	rules, err := firewallRules(first, s.probeUID(), relayAddress)
	if err != nil {
		return err
	}
	if err := setFirewall(rules); err != nil {
		return err
	}
	if err := dropNetworkCaps(); err != nil {
		return err
	}
	s.Confined = true
	if relay != nil {
		s.Relay = relay.Addr().String()
	}
	return nil
}

func (s *Server) runUIDs() (first, last int) {
	return s.BaseUID, s.BaseUID + s.Users - 1
}

func (s *Server) probeUID() int {
	_, last := s.runUIDs()
	return last + 1
}

func (s *Server) checkUserRange() error {
	if s.BaseUID < 1000 {
		return fmt.Errorf("OPENNEKO_SANDBOX_BASE_UID=%d: the run users must start at 1000 or above", s.BaseUID)
	}
	if s.Users < 1 || s.Users > maxUsers {
		return fmt.Errorf("OPENNEKO_SANDBOX_USERS=%d: give 1 to %d run users", s.Users, maxUsers)
	}
	if s.probeUID() > maxUID {
		return fmt.Errorf("OPENNEKO_SANDBOX_BASE_UID=%d with %d users passes uid %d", s.BaseUID, s.Users, maxUID)
	}
	return nil
}
