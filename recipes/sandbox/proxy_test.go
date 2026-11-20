package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseEgressHosts(t *testing.T) {
	good := map[string]string{
		"API.Example.com":      "api.example.com:443",
		"example.com.":         "example.com:443",
		"example.com:8443":     "example.com:8443",
		"*.example.com":        "*.example.com:443",
		"1.1.1.1":              "1.1.1.1:443",
		"[2606:4700::1111]:80": "[2606:4700::1111]:80",
		"2606:4700::1111":      "[2606:4700::1111]:443",
	}
	for in, want := range good {
		rules, err := parseEgressHosts([]string{in})
		if err != nil || rules[0].String() != want {
			t.Errorf("parse %q = %v, %v; want %s", in, rules, err, want)
		}
	}
	for _, in := range []string{"", "*", "*.com", "exa mple.com", "-a.com", "example.com:0", "example.com:65536", "example.com:08", "http://example.com", "0x7f.1", "127.1", "fe80::1%eth0", "a/b"} {
		if _, err := parseEgressHosts([]string{in}); err == nil {
			t.Errorf("parse %q: no error", in)
		}
	}
	if _, err := parseEgressHosts(make([]string, maxEgressHosts+1)); err == nil {
		t.Error("a list over the limit passed")
	}
}

func TestEgressRulesMatch(t *testing.T) {
	rules, err := parseEgressHosts([]string{"api.example.com", "*.files.example.com", "example.org:8080", "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		host string
		port int
		want bool
	}{
		{"api.example.com", 443, true},
		{"API.EXAMPLE.COM.", 443, true},
		{"api.example.com", 80, false},
		{"example.com", 443, false},
		{"evil-api.example.com", 443, false},
		{"api.example.com.evil.net", 443, false},
		{"a.files.example.com", 443, true},
		{"a.b.files.example.com", 443, true},
		{"files.example.com", 443, false},
		{"xfiles.example.com", 443, false},
		{"example.org", 8080, true},
		{"example.org", 443, false},
		{"1.1.1.1", 443, true},
		{"::ffff:1.1.1.1", 443, true},
		{"1.0.0.1", 443, false},
	}
	for _, c := range cases {
		if got := allows(rules, c.host, c.port); got != c.want {
			t.Errorf("allows(%s:%d) = %v, want %v", c.host, c.port, got, c.want)
		}
	}
}

func TestPublicAddr(t *testing.T) {
	for _, a := range []string{"1.1.1.1", "8.8.8.8", "2606:4700::1111"} {
		if !publicAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s refused", a)
		}
	}
	for _, a := range []string{"127.0.0.1", "127.0.0.11", "10.1.2.3", "172.17.0.1", "192.168.1.1", "169.254.169.254",
		"100.100.100.200", "0.0.0.0", "224.0.0.1", "255.255.255.255", "::1", "::", "::ffff:10.0.0.1",
		"::ffff:169.254.169.254", "fd00:ec2::254", "fe80::1", "64:ff9b::a9fe:a9fe", "2002:a9fe:a9fe::1", "ff02::1"} {
		if publicAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s passed as public", a)
		}
	}
}

type egressRig struct {
	sandbox  *Server
	socket   string
	proxyURL string
	log      *lockedBuffer
	upstream *url.URL
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startEgress runs a sandbox and its proxy. The upstream on 127.0.0.1
// stands for a public host: Public lets 127.0.0.1 through and keeps every
// other rule.
func startEgress(t *testing.T) *egressRig {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "upstream %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Proxy-Authorization"))
	}))
	t.Cleanup(upstream.Close)
	up, _ := url.Parse(upstream.URL)

	controlDir := filepath.Join(t.TempDir(), "egress")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddr := listener.Addr().String()
	_ = listener.Close()

	root := t.TempDir()
	_ = os.Chmod(root, 0o755)
	_ = os.Chmod(filepath.Dir(root), 0o755)
	server := &Server{Workspaces: filepath.Join(root, "runs"), Homes: filepath.Join(root, "homes"), BaseUID: 23100, Users: 4,
		GID: os.Getgid(), SwitchUsers: os.Geteuid() == 0, PATH: "/usr/bin:/bin",
		EgressProxy: proxyAddr, EgressControl: "unix://" + filepath.Join(controlDir, "control.sock")}
	socket := filepath.Join(root, "sandbox.sock")
	logs := &lockedBuffer{}
	loopback := netip.MustParseAddr("127.0.0.1")
	proxy := &Proxy{Control: server.EgressControl, Log: logs,
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			switch host {
			case "allowed.test", "other.test":
				return []netip.Addr{loopback}, nil
			case "private.test":
				return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
			case "mixed.test":
				return []netip.Addr{loopback, netip.MustParseAddr("169.254.169.254")}, nil
			}
			return nil, fmt.Errorf("no such host")
		},
		Public: func(a netip.Addr) bool { return a == loopback || publicAddr(a) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- server.ListenAndServe(ctx, "unix://"+socket) }()
	go func() { done <- proxy.ListenAndServe(ctx, proxyAddr) }()
	t.Cleanup(func() {
		cancel()
		<-done
		<-done
	})
	waitFor(t, func() bool { _, err := os.Stat(socket); return err == nil })
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(controlDir, "control.sock")); return err == nil })
	waitFor(t, func() bool {
		c, err := net.Dial("tcp", proxyAddr)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	return &egressRig{sandbox: server, socket: socket, proxyURL: proxyAddr, log: logs, upstream: up}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// proxyOf starts a run with hosts and returns the proxy URL its command gets.
func (r *egressRig) proxyOf(t *testing.T, client *testClient, hosts []string) *url.URL {
	t.Helper()
	answer, err := client.send(context.Background(), execBody{Command: `printf %s "$HTTPS_PROXY"`,
		Egress: &egressBody{Org: "org-1", Label: "work-run-1", Hosts: hosts}})
	if err != nil || answer.ExitCode != 0 {
		t.Fatalf("exec = %+v, %v", answer, err)
	}
	proxy, err := url.Parse(answer.Output)
	if err != nil || proxy.User == nil || proxy.User.Username() != client.run || proxy.Host != r.proxyURL {
		t.Fatalf("the command's HTTPS_PROXY = %q", answer.Output)
	}
	return proxy
}

func get(proxy *url.URL, target string) (int, string, error) {
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	resp, err := c.Get(target)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), nil
}

func connect(proxy *url.URL, target string) (string, error) {
	conn, err := net.DialTimeout("tcp", proxy.Host, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	if proxy.User != nil {
		pass, _ := proxy.User.Password()
		req.SetBasicAuth(proxy.User.Username(), pass)
		req.Header["Proxy-Authorization"] = req.Header["Authorization"]
		delete(req.Header, "Authorization")
	}
	if err := req.Write(conn); err != nil {
		return "", err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return resp.Status, nil
	}
	fmt.Fprintf(conn, "GET /tunnel HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target)
	inner, err := http.ReadResponse(reader, nil)
	if err != nil {
		return "", err
	}
	body, _ := io.ReadAll(inner.Body)
	return resp.Status + " | " + string(body), nil
}

func TestEgressProxyAllowsOnlyTheRunsList(t *testing.T) {
	rig := startEgress(t)
	port := rig.upstream.Port()
	client := newClient(rig.socket)
	proxy := rig.proxyOf(t, client, []string{"allowed.test:" + port, "private.test:" + port, "mixed.test:" + port})

	code, body, err := get(proxy, "http://allowed.test:"+port+"/plain")
	if err != nil || code != 200 || !strings.HasPrefix(body, "upstream GET /plain") || !strings.HasSuffix(body, `auth=""`) {
		t.Fatalf("listed host over plain HTTP = %d %q %v", code, body, err)
	}
	status, err := connect(proxy, "allowed.test:"+port)
	if err != nil || !strings.HasPrefix(status, "200") || !strings.Contains(status, "upstream GET /tunnel") {
		t.Fatalf("listed host over CONNECT = %q %v", status, err)
	}
	for target, why := range map[string]string{
		"other.test:" + port:   "a host not on the list",
		"allowed.test:1":       "a listed host on another port",
		"127.0.0.1:" + port:    "an address not on the list",
		"private.test:" + port: "a listed name with a private address",
		"mixed.test:" + port:   "a listed name with one metadata address",
	} {
		if status, err := connect(proxy, target); err != nil || !strings.HasPrefix(status, "403") {
			t.Errorf("%s (%s) = %q %v, want 403", target, why, status, err)
		}
	}
	if code, _, _ := get(proxy, "http://other.test:"+port+"/"); code != http.StatusForbidden {
		t.Errorf("plain HTTP to a host not on the list = %d", code)
	}
	logs := rig.log.String()
	for _, want := range []string{`"run":"` + client.run + `"`, `"org":"org-1"`, `"label":"work-run-1"`, `"result":"allowed"`,
		`"host":"private.test"`, `"reason":"the host resolves to an address that is not public"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the proxy log lacks %s:\n%s", want, logs)
		}
	}
}

func TestEgressProxyRefusesAnEmptyList(t *testing.T) {
	rig := startEgress(t)
	client := newClient(rig.socket)
	proxy := rig.proxyOf(t, client, nil)
	if status, err := connect(proxy, "allowed.test:"+rig.upstream.Port()); err != nil || !strings.HasPrefix(status, "403") {
		t.Fatalf("an empty list let a run out: %q %v", status, err)
	}
}

func TestEgressTokenBelongsToItsRunAndEndsWithIt(t *testing.T) {
	rig := startEgress(t)
	port := rig.upstream.Port()
	target := "allowed.test:" + port
	first, second := newClient(rig.socket), newClient(rig.socket)
	proxy := rig.proxyOf(t, first, []string{target})
	other := rig.proxyOf(t, second, []string{target})
	if status, err := connect(proxy, target); err != nil || !strings.HasPrefix(status, "200") {
		t.Fatalf("the run's own token = %q %v", status, err)
	}

	pass, _ := other.User.Password()
	stolen := *proxy
	stolen.User = url.UserPassword(first.run, pass)
	if status, err := connect(&stolen, target); err != nil || !strings.HasPrefix(status, "407") {
		t.Fatalf("a run used another run's token: %q %v", status, err)
	}
	none := *proxy
	none.User = nil
	if status, err := connect(&none, target); err != nil || !strings.HasPrefix(status, "407") {
		t.Fatalf("a request with no token = %q %v", status, err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if status, err := connect(proxy, target); err != nil || !strings.HasPrefix(status, "407") {
		t.Fatalf("the token still worked after the run ended: %q %v", status, err)
	}
	if status, err := connect(other, target); err != nil || !strings.HasPrefix(status, "200") {
		t.Fatalf("ending one run broke another: %q %v", status, err)
	}
}

func TestSandboxSetsNoProxyWithoutOne(t *testing.T) {
	_, socket := startSandbox(t, os.Geteuid() == 0)
	client := newClient(socket)
	answer, err := client.send(context.Background(), execBody{Command: "env", Egress: &egressBody{Hosts: []string{"example.com"}}})
	if err != nil || strings.Contains(strings.ToLower(answer.Output), "_proxy=") {
		t.Fatalf("exec = %+v, %v", answer, err)
	}
}

func TestSandboxRefusesABadEgressList(t *testing.T) {
	_, socket := startSandbox(t, os.Geteuid() == 0)
	client := newClient(socket)
	for _, e := range []*egressBody{{Hosts: []string{"http://example.com"}}, {Org: "a b"}, {Label: strings.Repeat("x", 129)}} {
		if _, err := client.send(context.Background(), execBody{Command: "true", Egress: e}); err == nil || !strings.HasPrefix(err.Error(), "400") {
			t.Errorf("egress %+v: %v", e, err)
		}
	}
}

func TestSandboxCommandCannotOpenTheControlSocket(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("switching run users needs root")
	}
	rig := startEgress(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, d := range []string{filepath.Dir(dir), dir} {
		_ = os.Chmod(d, 0o755)
	}
	dial := filepath.Join(dir, "dial")
	if err := os.WriteFile(dial, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	control := strings.TrimPrefix(rig.sandbox.EgressControl, "unix://")
	res, err := newClient(rig.socket).Exec(context.Background(), dial+" -test.run=^TestDialHelper$ -- "+control)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 || !strings.Contains(res.Output, "permission denied") {
		t.Fatalf("a run user opened the egress control socket: %+v", res)
	}
}
