package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TurnEgress reads a turn's org, run label and egress hosts from the
// environment that the control side gives the turn.
func TurnEgress(getenv func(string) string) (Egress, error) {
	var hosts []string
	for _, part := range strings.Split(getenv("OPENNEKO_HARNESS_EGRESS_HOSTS"), ",") {
		if part = strings.TrimSpace(part); part != "" {
			hosts = append(hosts, part)
		}
	}
	normal, err := NormalizeEgressHosts(hosts)
	if err != nil {
		return Egress{}, err
	}
	return Egress{Org: egressLabel(getenv("OPENNEKO_MCP_ORG_ID")), Label: egressLabel(getenv("OPENNEKO_HARNESS_RUN_ID")), Hosts: normal}, nil
}

func newRunID() string {
	var id [12]byte
	_, _ = rand.Read(id[:])
	return "run-" + hex.EncodeToString(id[:])
}

// Gate is the control side's hold on one run in the sandbox. It opens the
// run's session with the sandbox key and the run's egress list, and passes
// on only that run's commands from an agent runner, which holds neither.
type Gate struct {
	cfg    Config
	run    string
	egress Egress
	http   *http.Client
	base   string
	ln     *connListener
	srv    *http.Server

	mu     sync.Mutex
	token  string
	opened bool
}

// NewGate fixes the run id and the egress list for one turn.
func NewGate(cfg Config, egress Egress) (*Gate, error) {
	httpClient, base, err := transport(cfg.URL)
	if err != nil {
		return nil, err
	}
	g := &Gate{cfg: cfg, run: newRunID(), egress: egress, http: httpClient, base: base, ln: newConnListener()}
	g.srv = &http.Server{Handler: http.HandlerFunc(g.pass), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = g.srv.Serve(g.ln) }()
	return g, nil
}

// Run is the run id that the runner must use.
func (g *Gate) Run() string { return g.run }

// Serve answers the runner's HTTP requests on conn.
func (g *Gate) Serve(conn net.Conn) { g.ln.push(conn) }

// Close stops passing requests and ends the run in the sandbox.
func (g *Gate) Close() error {
	_ = g.srv.Close()
	g.mu.Lock()
	opened := g.opened
	g.mu.Unlock()
	if !opened {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := g.send(ctx, http.MethodDelete, "/v2/runs/"+url.PathEscape(g.run), g.cfg.Key, nil, "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (g *Gate) send(ctx context.Context, method, path, bearer string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return g.http.Do(req)
}

// session opens the run's session once and returns its token.
func (g *Gate) session(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" {
		return g.token, nil
	}
	raw, _ := json.Marshal(g.egress)
	resp, err := g.send(ctx, http.MethodPost, "/v2/runs/"+url.PathEscape(g.run), g.cfg.Key, bytes.NewReader(raw), "application/json")
	if err != nil {
		return "", errors.New("the sandbox did not answer")
	}
	defer resp.Body.Close()
	g.opened = true
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return "", errors.New("the sandbox is older than this release; rebuild it from recipes/sandbox")
	default:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("the sandbox refused the run: %s", bytes.TrimSpace(msg))
	}
	var answer struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&answer) != nil || answer.Token == "" {
		return "", errors.New("the sandbox gave the run no token")
	}
	g.token = answer.Token
	return g.token, nil
}

func (g *Gate) forget(token string) {
	g.mu.Lock()
	if g.token == token {
		g.token = ""
	}
	g.mu.Unlock()
}

func (g *Gate) pass(w http.ResponseWriter, r *http.Request) {
	runPath := "/v2/runs/" + g.run
	if !(r.Method == http.MethodPost && r.URL.Path == runPath+"/exec") && !(r.Method == http.MethodDelete && r.URL.Path == runPath) {
		http.Error(w, "the agent runner reaches only the exec and end of its own run", http.StatusForbidden)
		return
	}
	token, err := g.session(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var body io.Reader
	contentType := ""
	if r.Method == http.MethodPost {
		body, contentType = http.MaxBytesReader(w, r.Body, maxBody), "application/json"
	}
	resp, err := g.send(r.Context(), r.Method, r.URL.Path, token, body, contentType)
	if err != nil {
		http.Error(w, "the sandbox did not answer", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		g.forget(token)
		http.Error(w, "the sandbox lost the run", http.StatusConflict)
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxBody))
}

type connListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newConnListener() *connListener {
	return &connListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *connListener) push(conn net.Conn) {
	select {
	case l.conns <- conn:
	case <-l.done:
		_ = conn.Close()
	}
}

func (l *connListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *connListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *connListener) Addr() net.Addr { return &net.UnixAddr{Name: "gate", Net: "unix"} }
