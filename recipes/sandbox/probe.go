//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

var (
	probeNames   = []string{"example.com", "metadata.google.internal"}
	probePublic  = []string{"1.1.1.1:443", "8.8.8.8:53", "169.254.169.254:80"}
	gatewayPorts = []string{"22", "53", "80", "443", "2375", "2376"}
)

// egressProbe runs as the probe user. It returns each check that got
// through: a name that resolved, or an address that answered. A refused
// connection counts, because the packet reached the address.
func egressProbe(ctx context.Context) []string {
	targets := append([]string{}, probePublic...)
	for _, gw := range gateways() {
		for _, port := range gatewayPorts {
			targets = append(targets, net.JoinHostPort(gw, port))
		}
	}
	var mu sync.Mutex
	var reached []string
	var wg sync.WaitGroup
	for _, name := range probeNames {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lookup, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			if addrs, err := net.DefaultResolver.LookupHost(lookup, name); err == nil && len(addrs) > 0 {
				mu.Lock()
				reached = append(reached, "resolved "+name)
				mu.Unlock()
			}
		}()
	}
	for _, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "tcp", target)
			if err == nil {
				conn.Close()
			}
			if err == nil || errors.Is(err, syscall.ECONNREFUSED) {
				mu.Lock()
				reached = append(reached, "reached "+target)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return reached
}

// gateways returns the first host address of each IPv4 network the
// sandbox is on: Docker gives that address to the bridge on the host.
func gateways() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil {
			continue
		}
		gw := n.IP.To4().Mask(n.Mask)
		gw[3]++
		if !gw.Equal(n.IP.To4()) {
			out = append(out, gw.String())
		}
	}
	return out
}

func egressProbeMain() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	reached := egressProbe(ctx)
	if reached == nil {
		reached = []string{}
	}
	_ = json.NewEncoder(os.Stdout).Encode(reached)
}

// probeAsRunUser runs the probe in a child process as the probe user, which
// the firewall treats as a run user.
func (s *Server) probeAsRunUser(ctx context.Context) ([]string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "egress-probe")
	cmd.Env = []string{"PATH=" + s.PATH}
	cmd.Dir = "/"
	uid := uint32(s.probeUID())
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: []uint32{}}}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("the egress probe failed: %w", err)
	}
	var reached []string
	if err := json.Unmarshal(out, &reached); err != nil {
		return nil, fmt.Errorf("the egress probe answer: %w", err)
	}
	return reached, nil
}

// watchEgress probes now and then every interval; a probe that gets
// through, or fails to run, marks the egress open until a later probe
// finds it closed.
func (s *Server) watchEgress(ctx context.Context, every time.Duration) {
	s.probeOnce(ctx)
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s.probeOnce(ctx)
			}
		}
	}()
}

func (s *Server) probeOnce(ctx context.Context) {
	reached, err := s.Probe(ctx)
	if ctx.Err() != nil {
		return
	}
	open := err != nil || len(reached) > 0
	if err != nil {
		fmt.Fprintf(os.Stderr, "openneko sandbox: egress open: %v\n", err)
	} else if open {
		fmt.Fprintf(os.Stderr, "openneko sandbox: egress open: a run user %v\n", reached)
	}
	s.mu.Lock()
	changed := s.probeOpen != open
	s.probeOpen = open
	s.mu.Unlock()
	if changed && !open {
		fmt.Fprintln(os.Stderr, "openneko sandbox: egress closed again")
	}
}
