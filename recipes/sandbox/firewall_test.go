package main

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFirewallRulesLetRunsReachOnlyTheRelay(t *testing.T) {
	rules, err := firewallRules(20000, 20256, "127.0.0.1:3128")
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(rules, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "meta skuid") {
			kept = append(kept, line)
		}
	}
	want := []string{
		"meta skuid 20000-20256 ip daddr 127.0.0.1 tcp dport 3128 accept",
		"meta skuid 20000-20256 drop",
	}
	if strings.Join(kept, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rules for the run users:\n%s\nwant:\n%s", strings.Join(kept, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(rules, "type filter hook output priority 0; policy accept;") || !strings.HasPrefix(rules, "add table inet openneko_sandbox\ndelete table inet openneko_sandbox\n") {
		t.Fatalf("rules:\n%s", rules)
	}
	for _, bad := range []struct {
		first, last int
		relay       string
	}{{20000, 20256, "openneko-egress:3128"}, {20000, 20256, "[::1]:3128"}, {0, 10, "127.0.0.1:3128"}, {20, 10, "127.0.0.1:3128"}} {
		if _, err := firewallRules(bad.first, bad.last, bad.relay); err == nil {
			t.Errorf("firewallRules(%d, %d, %q) gave no error", bad.first, bad.last, bad.relay)
		}
	}
}

func TestTheProbeUserIsInTheFirewallRange(t *testing.T) {
	s := &Server{BaseUID: 20000, Users: 256}
	if s.probeUID() != 20256 {
		t.Fatalf("probe user = %d", s.probeUID())
	}
	for uid := s.BaseUID; uid < s.BaseUID+s.Users; uid++ {
		if uid == s.probeUID() {
			t.Fatal("the probe user is a run user")
		}
	}
}

func TestCapsWithout(t *testing.T) {
	status := "Name:\tx\nCapInh:\t0000000000000000\nCapPrm:\t%s\nCapEff:\t%s\nCapBnd:\t%s\nCapAmb:\t0000000000000000\n"
	clean := "00000000000000e3"
	netAdmin := "00000000000010e3"
	setPCap := "00000000000001e3"
	for _, c := range []struct {
		name          string
		prm, eff, bnd string
		ok            bool
	}{
		{"dropped", clean, clean, clean, true},
		{"effective NET_ADMIN", clean, netAdmin, clean, false},
		{"bounding NET_ADMIN", clean, clean, netAdmin, false},
		{"permitted SETPCAP", setPCap, clean, clean, false},
	} {
		err := capsWithout(strings.NewReader(fmtStatus(status, c.prm, c.eff, c.bnd)), capNetAdmin, capSetPCap)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if err := capsWithout(strings.NewReader("Name:\tx\n"), capNetAdmin); err == nil {
		t.Error("a status with no capability lines passed")
	}
}

func fmtStatus(format string, values ...string) string {
	for _, v := range values {
		format = strings.Replace(format, "%s", v, 1)
	}
	return format
}

func TestCommandsGetTheRelayAsTheirProxy(t *testing.T) {
	s := &Server{EgressProxy: "openneko-egress:3128", Relay: "127.0.0.1:3128"}
	env := strings.Join(s.proxyEnv("run-abcdefgh", "tok"), "\n")
	if !strings.Contains(env, "HTTPS_PROXY=http://run-abcdefgh:tok@127.0.0.1:3128\n") || strings.Contains(env, "openneko-egress") {
		t.Fatalf("env:\n%s", env)
	}
}

func TestRelayPassesTheConnectionToTheProxy(t *testing.T) {
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	go func() {
		conn, err := proxy.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 5)
		_, _ = io.ReadFull(conn, buf)
		_, _ = conn.Write([]byte("got " + string(buf)))
	}()
	relay, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relayToProxy(ctx, relay, proxy.Addr().String())
	conn, err := net.Dial("tcp", relay.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("hello"))
	answer, err := io.ReadAll(conn)
	if err != nil || string(answer) != "got hello" {
		t.Fatalf("answer = %q, %v", answer, err)
	}
}

// TestDropNetworkCaps runs as root without cgo (the CI root step). It
// drops the capabilities in a child process, then has that child exec a
// program as root, which must not get them back.
func TestDropNetworkCaps(t *testing.T) {
	if os.Getenv("OPENNEKO_TEST_DROP_CAPS") == "1" {
		if err := dropNetworkCaps(); err != nil {
			os.Stdout.WriteString("drop failed: " + err.Error() + "\n")
			os.Exit(3)
		}
		out, err := exec.Command("cat", "/proc/self/status").Output()
		if err != nil {
			os.Exit(4)
		}
		os.Stdout.Write(out)
		os.Exit(0)
	}
	if os.Geteuid() != 0 {
		t.Skip("dropping capabilities needs root")
	}
	before, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	if capsWithout(strings.NewReader(string(before)), capSetPCap) == nil {
		t.Fatal("this root process has no CAP_SETPCAP, so the test proves nothing")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run", "^TestDropNetworkCaps$")
	cmd.Env = append(os.Environ(), "OPENNEKO_TEST_DROP_CAPS=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if err := capsWithout(strings.NewReader(string(out)), capNetAdmin, capSetPCap); err != nil {
		t.Fatalf("a program that root ran after the drop: %v\n%s", err, out)
	}
}
