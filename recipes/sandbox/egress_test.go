package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
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

func TestSandboxCheckReportsEgress(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct{ v4, want string }{
		{"eth0\t000012AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n", "closed"},
		{"eth0\t00000000\t010012AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n", "open"},
	} {
		server := &Server{Workspaces: filepath.Join(root, "runs"), Homes: filepath.Join(root, "homes"), Key: "k", ProcNet: writeRoutes(t, c.v4, "-")}
		listener, err := server.listen("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- server.Serve(ctx, listener) }()
		client := &testClient{http: http.DefaultClient, base: "http://" + listener.Addr().String(), key: "k"}
		resp, err := client.do(ctx, http.MethodGet, "/v2/check", nil)
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Status, Egress string }
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		cancel()
		<-done
		if err != nil || body.Status != "ok" || body.Egress != c.want {
			t.Fatalf("check = %+v, %v; want egress %q", body, err, c.want)
		}
	}
}
