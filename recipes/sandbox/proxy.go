package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Proxy is the only way out of the sandbox network. It takes CONNECT and
// absolute-form HTTP from a run that shows its token, and only to a host and
// port on the run's list.
type Proxy struct {
	Control string
	Log     io.Writer
	Lookup  func(ctx context.Context, host string) ([]netip.Addr, error)
	Dial    func(ctx context.Context, address string) (net.Conn, error)
	Public  func(netip.Addr) bool

	once    sync.Once
	control *http.Client
	logMu   sync.Mutex
}

type egressGrant struct {
	Org   string   `json:"org"`
	Label string   `json:"label"`
	Hosts []string `json:"hosts"`
}

type egressLog struct {
	Time    string `json:"time"`
	Run     string `json:"run,omitempty"`
	Org     string `json:"org,omitempty"`
	Label   string `json:"label,omitempty"`
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
	Address string `json:"address,omitempty"`
	Result  string `json:"result"`
	Reason  string `json:"reason,omitempty"`
}

const egressDialTimeout = 15 * time.Second

func (p *Proxy) ListenAndServe(ctx context.Context, address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("egress proxy listener unavailable: %w", err)
	}
	fmt.Fprintf(os.Stderr, "openneko egress: listening on %s\n", address)
	server := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entry := egressLog{Result: "refused"}
	defer func() { p.record(entry) }()
	run, token, ok := proxyCredentials(r)
	entry.Run = run
	if !ok {
		entry.Reason = "no run token"
		w.Header().Set("Proxy-Authenticate", `Basic realm="openneko-egress"`)
		http.Error(w, "openneko egress: this proxy needs the run's token", http.StatusProxyAuthRequired)
		return
	}
	grant, err := p.grant(r.Context(), run, token)
	if err != nil {
		entry.Reason = err.Error()
		w.Header().Set("Proxy-Authenticate", `Basic realm="openneko-egress"`)
		http.Error(w, "openneko egress: the run token is not valid", http.StatusProxyAuthRequired)
		return
	}
	entry.Org, entry.Label = grant.Org, grant.Label
	host, port, err := proxyTarget(r)
	entry.Host, entry.Port = host, port
	if err != nil {
		entry.Reason = err.Error()
		http.Error(w, "openneko egress: "+err.Error(), http.StatusBadRequest)
		return
	}
	rules, err := parseEgressHosts(grant.Hosts)
	if err != nil || !allows(rules, host, port) {
		entry.Reason = "the host is not on the organization's egress list"
		http.Error(w, "openneko egress: "+net.JoinHostPort(host, strconv.Itoa(port))+" is not on the egress list. An admin adds hosts at Admin, Settings, Agent.", http.StatusForbidden)
		return
	}
	addr, err := p.resolve(r.Context(), host)
	if err != nil {
		entry.Reason = err.Error()
		http.Error(w, "openneko egress: "+err.Error(), http.StatusForbidden)
		return
	}
	target := netip.AddrPortFrom(addr, uint16(port)).String()
	entry.Address = target
	dialCtx, cancel := context.WithTimeout(r.Context(), egressDialTimeout)
	upstream, err := p.dial(dialCtx, target)
	cancel()
	if err != nil {
		entry.Reason = "connect failed"
		http.Error(w, "openneko egress: the host did not accept the connection", http.StatusBadGateway)
		return
	}
	entry.Result = "allowed"
	if r.Method == http.MethodConnect {
		tunnel(w, upstream)
		return
	}
	forward(w, r, upstream)
}

func (p *Proxy) record(e egressLog) {
	if p.Log == nil {
		return
	}
	e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	line, _ := json.Marshal(e)
	p.logMu.Lock()
	defer p.logMu.Unlock()
	_, _ = p.Log.Write(append(line, '\n'))
}

func proxyCredentials(r *http.Request) (string, string, bool) {
	header := r.Header.Get("Proxy-Authorization")
	if header == "" {
		return "", "", false
	}
	fake := &http.Request{Header: http.Header{"Authorization": {header}}}
	run, token, ok := fake.BasicAuth()
	if !ok || !runIDPattern.MatchString(run) || token == "" {
		return "", "", false
	}
	return run, token, true
}

func proxyTarget(r *http.Request) (string, int, error) {
	hostport, defaultPort := r.Host, ""
	if r.Method != http.MethodConnect {
		if r.URL.Scheme != "http" || r.URL.Host == "" {
			return "", 0, errors.New("plain requests must use an absolute http:// URL; use CONNECT for https")
		}
		hostport, defaultPort = r.URL.Host, "80"
	}
	host, portText, err := net.SplitHostPort(hostport)
	if err != nil && defaultPort != "" {
		host, portText, err = strings.Trim(hostport, "[]"), defaultPort, nil
	}
	if err != nil {
		return "", 0, errors.New("the request names no port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || host == "" {
		return "", 0, errors.New("the request names no valid host and port")
	}
	return host, port, nil
}

// resolve looks the name up once and refuses it when any answer is not
// public, so the dial uses the address that passed the check.
func (p *Proxy) resolve(ctx context.Context, host string) (netip.Addr, error) {
	public := p.Public
	if public == nil {
		public = publicAddr
	}
	var addrs []netip.Addr
	if addr, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{addr}
	} else {
		lookup := p.Lookup
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			}
		}
		found, err := lookup(ctx, host)
		if err != nil || len(found) == 0 {
			return netip.Addr{}, errors.New("the host name did not resolve")
		}
		addrs = found
	}
	for _, a := range addrs {
		if !public(a) {
			return netip.Addr{}, errNotPublic
		}
	}
	return addrs[0].Unmap(), nil
}

func (p *Proxy) dial(ctx context.Context, address string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, address)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", address)
}

func (p *Proxy) grant(ctx context.Context, run, token string) (egressGrant, error) {
	p.once.Do(func() {
		socket := strings.TrimPrefix(p.Control, "unix://")
		p.control = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}}}
	})
	body, _ := json.Marshal(map[string]string{"run": run, "token": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://sandbox/v2/egress/grant", bytes.NewReader(body))
	if err != nil {
		return egressGrant{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.control.Do(req)
	if err != nil {
		return egressGrant{}, errors.New("the sandbox control socket did not answer")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return egressGrant{}, errors.New("unknown run or token")
	}
	var grant egressGrant
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&grant); err != nil {
		return egressGrant{}, errors.New("the sandbox control socket answered badly")
	}
	return grant, nil
}

func tunnel(w http.ResponseWriter, upstream net.Conn) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "openneko egress: CONNECT needs HTTP/1.1", http.StatusHTTPVersionNotSupported)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	splice(client, buffered.Reader, upstream)
}

func splice(client net.Conn, fromClient *bufio.Reader, upstream net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, fromClient)
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
	client.Close()
	upstream.Close()
}

func closeWrite(c net.Conn) {
	if tcp, ok := c.(interface{ CloseWrite() error }); ok {
		_ = tcp.CloseWrite()
		return
	}
	_ = c.Close()
}

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func forward(w http.ResponseWriter, r *http.Request, upstream net.Conn) {
	defer upstream.Close()
	out := r.Clone(r.Context())
	for _, name := range strings.Split(r.Header.Get("Connection"), ",") {
		out.Header.Del(strings.TrimSpace(name))
	}
	for _, name := range hopHeaders {
		out.Header.Del(name)
	}
	out.Close = true
	if err := out.Write(upstream); err != nil {
		http.Error(w, "openneko egress: the request could not be sent", http.StatusBadGateway)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(upstream), out)
	if err != nil {
		http.Error(w, "openneko egress: the host answered badly", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, name := range hopHeaders {
		resp.Header.Del(name)
	}
	for name, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
