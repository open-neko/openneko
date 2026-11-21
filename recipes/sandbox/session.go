//go:build linux

package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// A session is a run that the control side opened: the egress list, org
// and label are fixed, and its token lets an agent runner send only that
// run's commands.
type session struct {
	token   string
	grant   egressBody
	expires time.Time
}

type scopeKey struct{}

// runScope is the run that a run token allows, or "" for the control side.
func runScope(r *http.Request) string {
	id, _ := r.Context().Value(scopeKey{}).(string)
	return id
}

func pathRun(path string) (id string, exec bool) {
	rest, ok := strings.CutPrefix(path, "/v2/runs/")
	if !ok {
		return "", false
	}
	if id, ok := strings.CutSuffix(rest, "/exec"); ok {
		return id, true
	}
	return rest, false
}

// authorize returns the request with its scope, or nil. The sandbox key, or
// no Authorization at all on a sandbox without a key, is the control side.
func (s *Server) authorize(r *http.Request) *http.Request {
	header := r.Header.Get("Authorization")
	if s.Key == "" && header == "" {
		return r
	}
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || token == "" {
		return nil
	}
	if s.Key != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.Key)) == 1 {
		return r
	}
	id, exec := pathRun(r.URL.Path)
	if !runIDPattern.MatchString(id) || !(exec && r.Method == http.MethodPost || !exec && r.Method == http.MethodDelete) {
		return nil
	}
	s.mu.Lock()
	found, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(found.token)) != 1 {
		return nil
	}
	return r.WithContext(context.WithValue(r.Context(), scopeKey{}, id))
}

// openRun answers POST /v2/runs/{run} from the control side only.
func (s *Server) openRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	if runScope(r) != "" {
		http.Error(w, "only the control side opens a run", http.StatusForbidden)
		return
	}
	var e egressBody
	if !runIDPattern.MatchString(id) || json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&e) != nil {
		http.Error(w, "invalid run", http.StatusBadRequest)
		return
	}
	if err := checkEgress(&e); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e.Hosts = append([]string{}, e.Hosts...)
	token := newEgressToken()
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[string]*session{}
	}
	_, opened := s.sessions[id]
	_, running := s.runs[id]
	if !opened && !running {
		s.sessions[id] = &session{token: token, grant: e, expires: s.sessionExpiry()}
	}
	s.mu.Unlock()
	if opened || running {
		http.Error(w, "the run already exists", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
}

// sessionGrant runs under s.mu.
func (s *Server) sessionGrant(id string) (egressBody, bool) {
	found, ok := s.sessions[id]
	if !ok || !s.now().Before(found.expires) {
		return egressBody{}, false
	}
	found.expires = s.sessionExpiry()
	grant := found.grant
	grant.Hosts = append([]string{}, grant.Hosts...)
	return grant, true
}

func (s *Server) sessionExpiry() time.Time {
	return s.now().Add(maxTimeout*time.Second + s.margin())
}

// reapSessions runs under s.mu.
func (s *Server) reapSessions() {
	for id, found := range s.sessions {
		if _, running := s.runs[id]; !running && !s.now().Before(found.expires) {
			delete(s.sessions, id)
		}
	}
}
