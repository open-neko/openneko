package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct{ ahead atomic.Int64 }

func (c *fakeClock) now() time.Time { return time.Now().Add(time.Duration(c.ahead.Load())) }

func (c *fakeClock) advance(d time.Duration) { c.ahead.Add(int64(d)) }

func (s *Server) usedUsers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.used)
}

func TestRunAndTokenExpireAfterTheLongestCommand(t *testing.T) {
	clock := &fakeClock{}
	rig := startEgress(t, func(s *Server, _ *Proxy) { s.Now = clock.now })
	target := "allowed.test:" + rig.upstream.Port()
	client := newClient(rig.socket)
	proxy := rig.proxyOf(t, client, []string{target})
	if _, err := client.send(context.Background(), execBody{Command: "true", TimeoutSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(rig.sandbox.Workspaces, client.run)

	clock.advance(runMargin + 110*time.Second)
	if _, err := client.Exec(context.Background(), "true"); err != nil {
		t.Fatalf("a run inside its longest command time and the margin ended: %v", err)
	}
	clock.advance(runMargin + 110*time.Second)
	if status, err := connect(proxy, target); err != nil || !strings.HasPrefix(status, "200") {
		t.Fatalf("each command moves the end of the run: %q %v", status, err)
	}

	clock.advance(11 * time.Second)
	if status, err := connect(proxy, target); err != nil || !strings.HasPrefix(status, "407") {
		t.Fatalf("the token worked after the run expired: %q %v", status, err)
	}
	if _, err := client.Exec(context.Background(), "true"); err == nil || !strings.HasPrefix(err.Error(), "409") {
		t.Fatalf("an expired run took a command: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the expired run's folder stays: %v", err)
	}
	if n := rig.sandbox.usedUsers(); n != 0 {
		t.Fatalf("%d run users stay taken after the run expired", n)
	}
}

func TestExpiredRunsGiveTheirUsersBack(t *testing.T) {
	clock := &fakeClock{}
	rig := startEgress(t, func(s *Server, _ *Proxy) { s.Now = clock.now })
	for i := 0; i < rig.sandbox.Users; i++ {
		if _, err := newClient(rig.socket).Exec(context.Background(), "true"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := newClient(rig.socket).Exec(context.Background(), "true"); err == nil || !strings.HasPrefix(err.Error(), "503") {
		t.Fatalf("a run started with every user taken: %v", err)
	}
	clock.advance(runMargin + 11*time.Second)
	if _, err := newClient(rig.socket).Exec(context.Background(), "true"); err != nil {
		t.Fatalf("the users of expired runs did not go back to the pool: %v", err)
	}
}

func TestTheTimerRetiresAnAbandonedRun(t *testing.T) {
	rig := startEgress(t, func(s *Server, _ *Proxy) { s.RunMargin = 50 * time.Millisecond })
	client := newClient(rig.socket)
	if _, err := client.send(context.Background(), execBody{Command: "true", TimeoutSeconds: 1}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(rig.sandbox.Workspaces, client.run)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := os.Stat(dir)
		if errors.Is(err, os.ErrNotExist) && rig.sandbox.usedUsers() == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no command came and the run stayed: %v, %d users", err, rig.sandbox.usedUsers())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func openTunnel(t *testing.T, proxy *url.URL, target string) (net.Conn, string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxy.Host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	pass, _ := proxy.User.Password()
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	req.SetBasicAuth(proxy.User.Username(), pass)
	req.Header["Proxy-Authorization"] = req.Header["Authorization"]
	delete(req.Header, "Authorization")
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, resp.Status
}

func closedWithin(conn net.Conn, wait time.Duration) bool {
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	_, err := io.ReadAll(conn)
	var timeout net.Error
	return !(errors.As(err, &timeout) && timeout.Timeout())
}

func TestEgressClosesAnIdleTunnel(t *testing.T) {
	rig := startEgress(t, func(_ *Server, p *Proxy) { p.IdleTimeout, p.Recheck = 200*time.Millisecond, time.Hour })
	target := "allowed.test:" + rig.upstream.Port()
	proxy := rig.proxyOf(t, newClient(rig.socket), []string{target})
	conn, status := openTunnel(t, proxy, target)
	if !strings.HasPrefix(status, "200") {
		t.Fatal(status)
	}
	if !closedWithin(conn, 3*time.Second) {
		t.Fatal("an idle tunnel stayed open")
	}
	if !strings.Contains(rig.log.String(), `"result":"closed","reason":"idle"`) {
		t.Fatalf("the proxy log does not say why it closed the tunnel:\n%s", rig.log.String())
	}

	busy, _ := openTunnel(t, proxy, target)
	for i := 0; i < 6; i++ {
		if _, err := busy.Write([]byte("GET / HTTP/1.1\r\nHost: allowed.test\r\n\r\n")); err != nil {
			t.Fatalf("a busy tunnel closed: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestEgressClosesATunnelWhenItsHostLeavesTheList(t *testing.T) {
	rig := startEgress(t, func(_ *Server, p *Proxy) { p.IdleTimeout, p.Recheck = time.Hour, 200*time.Millisecond })
	target := "allowed.test:" + rig.upstream.Port()
	client := newClient(rig.socket)
	proxy := rig.proxyOf(t, client, []string{target, "other.test:" + rig.upstream.Port()})
	conn, status := openTunnel(t, proxy, target)
	if !strings.HasPrefix(status, "200") {
		t.Fatal(status)
	}
	if closedWithin(conn, 600*time.Millisecond) {
		t.Fatal("the proxy closed a tunnel whose host is on the list")
	}
	if _, err := client.send(context.Background(), execBody{Command: "true", Egress: &egressBody{Org: "org-1", Hosts: []string{"other.test:" + rig.upstream.Port()}}}); err != nil {
		t.Fatal(err)
	}
	if !closedWithin(conn, 3*time.Second) {
		t.Fatal("the tunnel stayed open after its host left the list")
	}
	if !strings.Contains(rig.log.String(), `"reason":"the host left the organization's egress list"`) {
		t.Fatalf("the proxy log does not say why it closed the tunnel:\n%s", rig.log.String())
	}
}

func TestEgressClosesATunnelWhenItsRunExpires(t *testing.T) {
	clock := &fakeClock{}
	rig := startEgress(t, func(s *Server, p *Proxy) {
		s.Now = clock.now
		p.IdleTimeout, p.Recheck = time.Hour, 200*time.Millisecond
	})
	target := "allowed.test:" + rig.upstream.Port()
	proxy := rig.proxyOf(t, newClient(rig.socket), []string{target})
	conn, status := openTunnel(t, proxy, target)
	if !strings.HasPrefix(status, "200") {
		t.Fatal(status)
	}
	clock.advance(runMargin + 11*time.Second)
	if !closedWithin(conn, 3*time.Second) {
		t.Fatal("the tunnel stayed open after its run expired")
	}
}

func TestEgressCapsTheTunnelsOfARun(t *testing.T) {
	rig := startEgress(t, func(_ *Server, p *Proxy) { p.MaxPerRun = 2 })
	target := "allowed.test:" + rig.upstream.Port()
	proxy := rig.proxyOf(t, newClient(rig.socket), []string{target})
	other := rig.proxyOf(t, newClient(rig.socket), []string{target})
	first, _ := openTunnel(t, proxy, target)
	if _, status := openTunnel(t, proxy, target); !strings.HasPrefix(status, "200") {
		t.Fatal(status)
	}
	if _, status := openTunnel(t, proxy, target); !strings.HasPrefix(status, "429") {
		t.Fatalf("a run opened a tunnel over the cap: %s", status)
	}
	if code, _, err := get(proxy, "http://"+target+"/"); err != nil || code != http.StatusTooManyRequests {
		t.Fatalf("plain HTTP over the cap = %d %v", code, err)
	}
	if _, status := openTunnel(t, other, target); !strings.HasPrefix(status, "200") {
		t.Fatalf("one run's tunnels took another run's room: %s", status)
	}
	first.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, status := openTunnel(t, proxy, target)
		conn.Close()
		if strings.HasPrefix(status, "200") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a closed tunnel kept its place: %s", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
