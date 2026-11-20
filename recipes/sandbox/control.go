package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type egressBody struct {
	Org   string   `json:"org"`
	Label string   `json:"label"`
	Hosts []string `json:"hosts"`
}

type runEgress struct {
	token string
	grant egressGrant
}

var egressLabelPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{0,128}$`)

func checkEgress(e *egressBody) error {
	if e == nil {
		return nil
	}
	if !egressLabelPattern.MatchString(e.Org) || !egressLabelPattern.MatchString(e.Label) {
		return errors.New("invalid egress org or label")
	}
	_, err := parseEgressHosts(e.Hosts)
	return err
}

func newEgressToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// setEgress keeps the run's latest list under s.mu: the proxy asks for it
// while the run's command holds run.mu.
func (s *Server) setEgress(r *run, e *egressBody) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.egress.token == "" {
		r.egress.token = newEgressToken()
	}
	if e != nil {
		r.egress.grant = egressGrant{Org: e.Org, Label: e.Label, Hosts: append([]string{}, e.Hosts...)}
	}
	return r.egress.token
}

func (s *Server) proxyEnv(id, token string) []string {
	if s.EgressProxy == "" {
		return nil
	}
	proxy := "http://" + id + ":" + token + "@" + s.EgressProxy
	return []string{"HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy, "http_proxy=" + proxy, "https_proxy=" + proxy, "NODE_USE_ENV_PROXY=1"}
}

// controlHandler answers the egress proxy on a socket that only root in
// the sandbox and the proxy can open.
func (s *Server) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/egress/grant", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Run   string `json:"run"`
			Token string `json:"token"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || req.Token == "" {
			http.Error(w, "invalid grant request", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		found, ok := s.runs[req.Run]
		var e runEgress
		if ok {
			e = found.egress
		}
		s.mu.Unlock()
		if !ok || e.token == "" || subtle.ConstantTimeCompare([]byte(req.Token), []byte(e.token)) != 1 {
			http.Error(w, "unknown run or token", http.StatusForbidden)
			return
		}
		grant := e.grant
		if grant.Hosts == nil {
			grant.Hosts = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(grant)
	})
	return mux
}

func listenControl(address string) (net.Listener, error) {
	path, ok := strings.CutPrefix(address, "unix://")
	if !ok || path == "" {
		return nil, errors.New("the egress control address must be unix:///path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	old := syscall.Umask(0o177)
	listener, err := net.Listen("unix", path)
	syscall.Umask(old)
	return listener, err
}
