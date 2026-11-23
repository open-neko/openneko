//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const routeHeader = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"

func writeRoutes(t *testing.T, v4, v6 string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "route"), []byte(routeHeader+v4), 0o644); err != nil {
		t.Fatal(err)
	}
	if v6 != "-" {
		if err := os.WriteFile(filepath.Join(dir, "ipv6_route"), []byte(v6), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestEgressStateReadsTheDefaultRoute(t *testing.T) {
	onLink := "eth0\t000012AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
	defaultV4 := "eth0\t00000000\t010012AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"
	rejectV6 := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo\n"
	linkLocalV6 := "fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001     eth0\n"
	defaultV6 := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 fd000000000000000000000000000001 00000400 00000001 00000000 00000003     eth0\n"
	cases := []struct {
		name, v4, v6, want string
	}{
		{"internal network", onLink, rejectV6 + linkLocalV6, "closed"},
		{"no ipv6", onLink, "-", "closed"},
		{"ipv4 default route", onLink + defaultV4, rejectV6, "open"},
		{"ipv6 default route", onLink, linkLocalV6 + defaultV6, "open"},
	}
	for _, c := range cases {
		if got := egressState(writeRoutes(t, c.v4, c.v6)); got != c.want {
			t.Errorf("%s: egress = %q, want %q", c.name, got, c.want)
		}
	}
	if got := egressState(filepath.Join(t.TempDir(), "missing")); got != "open" {
		t.Errorf("unreadable route table: egress = %q, want open", got)
	}
}

func askEgress(t *testing.T, server *Server) string {
	t.Helper()
	root := t.TempDir()
	key := strings.Repeat("k", minTCPKeyLength)
	server.Workspaces, server.Homes, server.Key = filepath.Join(root, "runs"), filepath.Join(root, "homes"), key
	listener, err := server.listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	defer func() {
		cancel()
		<-done
	}()
	client := &testClient{http: http.DefaultClient, base: "http://" + listener.Addr().String(), key: key}
	var resp *http.Response
	for i := 0; i < 200; i++ {
		if resp, err = client.do(ctx, http.MethodGet, "/v2/check", nil); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct{ Status, Egress string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Status != "ok" {
		t.Fatalf("check = %+v, %v", body, err)
	}
	return body.Egress
}

func TestSandboxCheckReportsEgress(t *testing.T) {
	onLink := "eth0\t000012AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
	defaultV4 := "eth0\t00000000\t010012AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"
	quiet := func(context.Context) ([]string, error) { return []string{}, nil }
	through := func(context.Context) ([]string, error) { return []string{"reached 1.1.1.1:443"}, nil }
	broken := func(context.Context) ([]string, error) { return nil, errors.New("the probe could not start") }
	cases := []struct {
		name     string
		routes   string
		confined bool
		probe    func(context.Context) ([]string, error)
		want     string
	}{
		{"firewall, no route, quiet probe", onLink, true, quiet, "closed"},
		{"firewall, no route, no probe", onLink, true, nil, "closed"},
		{"no firewall", onLink, false, quiet, "open"},
		{"default route", onLink + defaultV4, true, quiet, "open"},
		{"a probe got through", onLink, true, through, "open"},
		{"the probe did not run", onLink, true, broken, "open"},
	}
	for _, c := range cases {
		server := &Server{ProcNet: writeRoutes(t, c.routes, "-"), Confined: c.confined, Probe: c.probe}
		if got := askEgress(t, server); got != c.want {
			t.Errorf("%s: egress = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestProbeOnATimerOpensAndClosesTheEgress(t *testing.T) {
	var mu sync.Mutex
	reached := []string{}
	server := &Server{ProcNet: writeRoutes(t, "eth0\t000012AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n", "-"), Confined: true, ProbeEvery: 20 * time.Millisecond,
		Probe: func(context.Context) ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			return reached, nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.watchEgress(ctx, server.ProbeEvery)
	if got := server.egress(); got != "closed" {
		t.Fatalf("start: egress = %q, want closed", got)
	}
	mu.Lock()
	reached = []string{"resolved example.com"}
	mu.Unlock()
	waitFor(t, func() bool { return server.egress() == "open" })
	mu.Lock()
	reached = nil
	mu.Unlock()
	waitFor(t, func() bool { return server.egress() == "closed" })
}
