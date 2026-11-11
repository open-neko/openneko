package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultBaseUID = 20000
	defaultUsers   = 256
	maxTimeout     = 3600
	maxOutput      = 1_000_000
)

var runIDPattern = regexp.MustCompile(`^run-[A-Za-z0-9]{8,64}$`)

type execBody struct {
	Fresh          bool              `json:"fresh"`
	Root           string            `json:"root"`
	Dir            string            `json:"dir,omitempty"`
	Command        string            `json:"command"`
	SkillDir       string            `json:"skill_dir,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	MaxOutput      int               `json:"max_output"`
	Dirs           []string          `json:"dirs,omitempty"`
	Put            []fileData        `json:"put,omitempty"`
	Delete         []string          `json:"delete,omitempty"`
}

type execAnswer struct {
	execResult
	Dirs    []string   `json:"dirs"`
	Changed []fileData `json:"changed"`
	Deleted []string   `json:"deleted"`
	Skipped []skipped  `json:"skipped"`
}

// Server runs commands for runs. Each run gets a folder under Workspaces
// that only the sandbox holds, and a user and a group of its own between
// BaseUID and BaseUID+Users. GID owns the Unix socket.
type Server struct {
	Workspaces          string
	Homes               string
	Key                 string
	BaseUID, Users, GID int
	SwitchUsers         bool
	PATH                string
	NodePath            string

	mu   sync.Mutex
	runs map[string]*run
	used map[int]bool
}

type run struct {
	mu     sync.Mutex
	uid    int
	dir    string
	synced map[string]fileState
	dirs   map[string]bool
	told   map[string]bool
	ended  bool
}

// ListenAndServe serves on address until ctx ends: unix:///path for a
// Unix socket, or host:port for TCP, which needs a key.
func (s *Server) ListenAndServe(ctx context.Context, address string) error {
	listener, err := s.listen(address)
	if err != nil {
		return fmt.Errorf("sandbox listener unavailable: %w", err)
	}
	fmt.Fprintf(os.Stderr, "openneko sandbox: listening on %s\n", address)
	return s.Serve(ctx, listener)
}

func (s *Server) listen(address string) (net.Listener, error) {
	path, isUnix := strings.CutPrefix(address, "unix://")
	if !isUnix {
		if s.Key == "" {
			return nil, errors.New("a TCP listener needs OPENNEKO_SANDBOX_KEY")
		}
		return net.Listen("tcp", strings.TrimPrefix(address, "tcp://"))
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	old := syscall.Umask(0o117)
	listener, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(path, -1, s.GID); err != nil {
			_ = listener.Close()
			return nil, err
		}
	}
	return listener, nil
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if err := s.prepareWorkspaces(); err != nil {
		_ = listener.Close()
		return fmt.Errorf("runs folder unavailable: %w", err)
	}
	httpServer := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// prepareWorkspaces removes run folders left by an earlier start: their
// runs ended with it.
func (s *Server) prepareWorkspaces() error {
	if err := os.MkdirAll(s.Workspaces, 0o711); err != nil {
		return err
	}
	if err := os.Chmod(s.Workspaces, 0o711); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.Workspaces)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if runIDPattern.MatchString(entry.Name()) {
			_ = os.RemoveAll(filepath.Join(s.Workspaces, entry.Name()))
		}
	}
	return nil
}

// Handler is the sandbox API:
//
//	GET    /v2/health
//	GET    /v2/check            the health check with the key
//	POST   /v2/runs/{run}/exec  writes the files sent, runs the command, returns the files it changed
//	DELETE /v2/runs/{run}       kills the run's commands and removes its folder and home
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /v2/check", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /v2/runs/{run}/exec", s.exec)
	mux.HandleFunc("DELETE /v2/runs/{run}", s.end)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/health" && !s.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	if s.Key == "" {
		return true
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(token), []byte(s.Key)) == 1
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	var req execBody
	if !runIDPattern.MatchString(id) || json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req) != nil ||
		strings.TrimSpace(req.Command) == "" || len(req.Command) > 16384 || len(req.Root) > 4096 ||
		req.TimeoutSeconds < 1 || req.TimeoutSeconds > maxTimeout || req.MaxOutput < 1_000 || req.MaxOutput > maxOutput {
		http.Error(w, "invalid terminal command", http.StatusBadRequest)
		return
	}
	if err := checkTransfer(req.Dirs, req.Put, req.Delete); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := checkPaths(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	run, status, err := s.run(id, req.Fresh)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.ended {
		http.Error(w, "the run has ended", http.StatusConflict)
		return
	}
	home, err := s.home(id, run.uid)
	if err != nil {
		http.Error(w, "run home unavailable", http.StatusInternalServerError)
		return
	}
	env := []string{"PATH=" + s.PATH, "HOME=" + home, "TMPDIR=" + filepath.Join(home, "tmp"), "LANG=C.UTF-8"}
	if s.NodePath != "" {
		env = append(env, "NODE_PATH="+s.NodePath)
	}
	runPaths, err := runPathEnv(run.dir, req.Env)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	env = append(env, runPaths...)
	if req.SkillDir != "" {
		dir, ok := runPath(run.dir, req.SkillDir)
		if !ok {
			http.Error(w, "the skill folder is outside the run", http.StatusBadRequest)
			return
		}
		env = append(env, "SKILL_DIR="+dir)
	}
	cwd, ok := runPath(run.dir, req.Dir)
	if !ok {
		http.Error(w, "the working folder is outside the run", http.StatusBadRequest)
		return
	}
	if err := s.receive(run, req.Dirs, req.Put, req.Delete); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.mkdirOwned(run, cwd); err != nil {
		http.Error(w, "the working folder is unavailable", http.StatusBadRequest)
		return
	}
	cmd := command{
		Dir:            cwd,
		Script:         "umask 077\n" + rewriteRoot(req.Command, req.Root, run.dir, req.Root),
		Env:            env,
		TimeoutSeconds: req.TimeoutSeconds,
		MaxOutput:      req.MaxOutput,
	}
	if s.SwitchUsers {
		cmd.Credential = &syscall.Credential{Uid: uint32(run.uid), Gid: uint32(run.uid), Groups: []uint32{}}
	}
	result, err := runCommand(r.Context(), cmd)
	if s.SwitchUsers {
		killUser(run.uid)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	answer := execAnswer{execResult: result}
	answer.Output = rewriteRoot(answer.Output, run.dir, req.Root, req.Root)
	answer.Dirs, answer.Changed, answer.Deleted, answer.Skipped, err = collect(run)
	if err != nil {
		http.Error(w, "the run's files could not be read", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
}

// run returns the run's state. A fresh run gets a new folder; a run the
// sandbox does not know (it restarted, or the run ended) answers 409, so the
// caller can send its files again.
func (s *Server) run(id string, fresh bool) (*run, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == nil {
		s.runs, s.used = map[string]*run{}, map[int]bool{}
	}
	if existing, ok := s.runs[id]; ok {
		if fresh {
			return nil, http.StatusConflict, errors.New("the run already exists")
		}
		return existing, 0, nil
	}
	if !fresh {
		return nil, http.StatusConflict, errors.New("the sandbox does not know this run")
	}
	users := s.Users
	if users <= 0 {
		users = defaultUsers
	}
	uid := -1
	for i := 0; i < users; i++ {
		if !s.used[s.BaseUID+i] {
			uid = s.BaseUID + i
			break
		}
	}
	if uid < 0 {
		return nil, http.StatusServiceUnavailable, errors.New("the sandbox has no free run user")
	}
	dir := filepath.Join(s.Workspaces, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, http.StatusInternalServerError, errors.New("run folder unavailable")
	}
	if s.SwitchUsers {
		if err := os.Chown(dir, uid, uid); err != nil {
			_ = os.RemoveAll(dir)
			return nil, http.StatusInternalServerError, errors.New("run folder unavailable")
		}
	}
	s.used[uid] = true
	created := &run{uid: uid, dir: dir, synced: map[string]fileState{}, dirs: map[string]bool{}, told: map[string]bool{}}
	s.runs[id] = created
	return created, 0, nil
}

func (s *Server) end(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	if !runIDPattern.MatchString(id) {
		http.Error(w, "invalid run", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	run, ok := s.runs[id]
	delete(s.runs, id)
	s.mu.Unlock()
	if ok {
		if s.SwitchUsers {
			killUser(run.uid)
		}
		run.mu.Lock()
		run.ended = true
		_ = os.RemoveAll(run.dir)
		run.mu.Unlock()
	}
	_ = os.RemoveAll(filepath.Join(s.Homes, id))
	if ok {
		s.mu.Lock()
		delete(s.used, run.uid)
		s.mu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}

func runPathEnv(dir string, in map[string]string) ([]string, error) {
	allowed := map[string]bool{}
	for _, name := range runPathVars {
		allowed[name] = true
	}
	var out []string
	for name, value := range in {
		if !allowed[name] {
			return nil, fmt.Errorf("the sandbox does not pass %s", name)
		}
		var parts []string
		for _, part := range strings.Split(value, ":") {
			resolved, ok := runPath(dir, part)
			if !ok {
				return nil, fmt.Errorf("%s points outside the run", name)
			}
			parts = append(parts, resolved)
		}
		out = append(out, name+"="+strings.Join(parts, ":"))
	}
	sort.Strings(out)
	return out, nil
}

func checkPaths(req execBody) error {
	if _, err := runPathEnv("/", req.Env); err != nil {
		return err
	}
	if _, ok := runPath("/", req.SkillDir); !ok {
		return errors.New("the skill folder is outside the run")
	}
	if _, ok := runPath("/", req.Dir); !ok {
		return errors.New("the working folder is outside the run")
	}
	return nil
}

// runPath maps a slash path relative to the run's folder to a path in it.
func runPath(dir, rel string) (string, bool) {
	if rel == "" || rel == "." {
		return dir, true
	}
	clean := path.Clean(rel)
	if strings.ContainsRune(rel, '\\') || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", false
	}
	return filepath.Join(dir, filepath.FromSlash(clean)), true
}

// rewriteRoot swaps the caller's path for the run's folder, so a command
// and its output use the paths the agent was given.
func rewriteRoot(text, from, to, callerRoot string) string {
	if callerRoot == "" || from == to {
		return text
	}
	return strings.ReplaceAll(text, from, to)
}

func (s *Server) home(id string, uid int) (string, error) {
	home := filepath.Join(s.Homes, id)
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		return "", err
	}
	if s.SwitchUsers {
		for _, dir := range []string{home, filepath.Join(home, "tmp")} {
			if err := os.Chown(dir, uid, uid); err != nil {
				return "", err
			}
		}
	}
	return home, nil
}

func killUser(uid int) {
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		var stat syscall.Stat_t
		if syscall.Stat(filepath.Join("/proc", entry.Name()), &stat) == nil && int(stat.Uid) == uid {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func intEnv(name string, fallback int) int {
	n, err := strconv.Atoi(os.Getenv(name))
	if err != nil || n < 0 {
		return fallback
	}
	return n
}
