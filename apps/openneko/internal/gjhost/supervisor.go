package gjhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Supervisor serves the GraphJin address and runs the engine as a child
// process behind it, so a GraphJin crash does not end openneko serve.
type Supervisor struct {
	ConfigFile string
	Addr       string
	Log        io.Writer
	Poll       time.Duration
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// Command builds the child that serves GraphJin on the unix socket.
	Command func(socket string) *exec.Cmd
}

type State struct {
	State    string    `json:"state"`
	Reason   string    `json:"reason,omitempty"`
	Since    time.Time `json:"since"`
	Restarts int       `json:"restarts"`
}

const (
	StateStarting   = "starting"
	StateRunning    = "running"
	StateRestarting = "restarting"
	StateStopped    = "stopped"
)

// SelfCommand runs this executable as `graphjin serve --child`.
func SelfCommand(configFile string) func(socket string) *exec.Cmd {
	return func(socket string) *exec.Cmd {
		exe, err := os.Executable()
		if err != nil {
			exe = os.Args[0]
		}
		cmd := exec.Command(exe, "graphjin", "serve", "--child", "--config", configFile, "--addr", "unix:"+socket)
		cmd.Env = ChildEnv(os.Environ())
		return cmd
	}
}

var childEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "TZ": true, "LANG": true, "LC_ALL": true,
	"TMPDIR": true, "TEMP": true, "TMP": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"GO_ENV": true, "GODEBUG": true, "GOMAXPROCS": true, "GOMEMLIMIT": true, "GOTRACEBACK": true,
	"SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true,
	"USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
}

// ChildEnv keeps only what GraphJin and its trace exporter read. Serve's credentials, socket and
// sandbox key stay in the parent.
func ChildEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if childEnvNames[upper] || strings.HasPrefix(upper, "GJ_") || strings.HasPrefix(upper, "OTEL_") || upper == "OPENNEKO_OTEL_ENABLED" {
			out = append(out, kv)
		}
	}
	return out
}

type child struct {
	cmd     *exec.Cmd
	stdin   io.Closer
	started time.Time
	done    chan struct{}
	ready   chan struct{}
	err     error
	exited  time.Time
	tail    *tail
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

type tail struct {
	mu    sync.Mutex
	lines []string
	at    time.Time
}

func (t *tail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if line = strings.TrimSpace(ansi.ReplaceAllString(line, "")); line == "" {
		return
	}
	if len(t.lines) == 20 {
		t.lines = t.lines[1:]
	}
	t.lines = append(t.lines, line)
	t.at = time.Now()
}

// last returns the final line when it came just before the exit, as a
// Fatal line does; an older line says nothing about why the child ended.
func (t *tail) last(exited time.Time) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 || exited.Sub(t.at) > 2*time.Second {
		return ""
	}
	line := t.lines[len(t.lines)-1]
	if len(line) > 500 {
		line = line[:500]
	}
	return line
}

func (c *child) reason() string {
	msg := "graphjin exited"
	if c.err != nil {
		msg = c.err.Error()
	}
	if last := c.tail.last(c.exited); last != "" {
		msg += ": " + last
	}
	return msg
}

type supervisorState struct {
	mu    sync.Mutex
	state State
	file  string
}

func (s *supervisorState) set(state, reason string, restart bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.State == state && s.state.Reason == reason && !restart {
		return
	}
	s.state.State, s.state.Reason, s.state.Since = state, reason, time.Now().UTC()
	if restart {
		s.state.Restarts++
	}
	if b, err := json.Marshal(s.state); err == nil {
		_ = writeAtomic(s.file, b)
	}
}

func (s *supervisorState) get() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// ReadState reads the state the supervisor writes beside the config.
func ReadState(configFile string) (State, error) {
	var st State
	b, err := os.ReadFile(filepath.Join(filepath.Dir(configFile), StateFile))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

// Run serves Addr until ctx ends. A new token in RestartFile restarts the
// child with the config on disk; a child that exits restarts with backoff,
// or at once when the config file changes.
func (s *Supervisor) Run(ctx context.Context) error {
	dir := filepath.Dir(s.ConfigFile)
	poll, minB, maxB := s.Poll, s.MinBackoff, s.MaxBackoff
	if poll == 0 {
		poll = time.Second
	}
	if minB == 0 {
		minB = time.Second
	}
	if maxB == 0 {
		maxB = 30 * time.Second
	}
	_ = os.Remove(filepath.Join(dir, staleAgentKey))
	sockDir, err := os.MkdirTemp("", "openneko-gj-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "graphjin.sock")

	st := &supervisorState{file: filepath.Join(dir, StateFile)}
	st.set(StateStarting, "", false)
	defer st.set(StateStopped, "", false)

	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.proxy(sock, st), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shut, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	s.logf("graphjin address %s; engine runs as a child process", ln.Addr())

	last := readToken(filepath.Join(dir, RestartAckFile))
	pending := ""
	backoff := minB
	next := time.Now()
	stamp := configStamp(s.ConfigFile)
	var cur *child
	tick := time.NewTicker(poll)
	defer tick.Stop()
	ack := func() {
		if pending == "" {
			return
		}
		if err := writeAtomic(filepath.Join(dir, RestartAckFile), []byte(pending)); err != nil {
			s.logf("graphjin restart ack: %v", err)
		}
		pending = ""
	}
	for {
		var exited, ready chan struct{}
		if cur != nil {
			exited = cur.done
			if st.get().State != StateRunning {
				ready = cur.ready
			}
		}
		select {
		case <-ctx.Done():
			s.stopChild(cur)
			return nil
		case <-exited:
			reason := cur.reason()
			s.logf("graphjin stopped: %s; next start in %s", reason, backoff)
			st.set(StateRestarting, reason, true)
			next = time.Now().Add(backoff)
			backoff = min(2*backoff, maxB)
			stamp = configStamp(s.ConfigFile)
			cur = nil
			ack()
		case <-ready:
			st.set(StateRunning, "", false)
			s.logf("graphjin is running")
			backoff = minB
			ack()
		case <-tick.C:
		}
		if token := readToken(filepath.Join(dir, RestartFile)); token != "" && token != last {
			if cur != nil {
				s.stopChild(cur)
				cur = nil
				st.set(StateRestarting, "restart requested", true)
			}
			last, pending = token, token
			backoff, next = minB, time.Now()
		}
		if cur == nil && (!time.Now().Before(next) || configStamp(s.ConfigFile) != stamp) {
			if cur, err = s.startChild(sock); err != nil {
				reason := "graphjin did not start: " + err.Error()
				s.logf("%s", reason)
				st.set(StateRestarting, reason, false)
				next = time.Now().Add(backoff)
				backoff = min(2*backoff, maxB)
				ack()
			}
		}
		if token := readToken(filepath.Join(dir, PingFile)); token != "" {
			if err := writeAtomic(filepath.Join(dir, PingAckFile), []byte(token)); err != nil {
				s.logf("graphjin ping ack: %v", err)
			}
		}
	}
}

func (s *Supervisor) startChild(sock string) (*child, error) {
	_ = os.Remove(sock)
	cmd := s.Command(sock)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &child{cmd: cmd, stdin: stdin, started: time.Now(), done: make(chan struct{}), ready: make(chan struct{}), tail: &tail{}}
	go func() {
		for {
			select {
			case <-c.done:
				return
			case <-time.After(50 * time.Millisecond):
			}
			if dialable(sock) {
				close(c.ready)
				return
			}
		}
	}()
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			c.tail.add(sc.Text())
			s.logf("%s", sc.Text())
		}
		c.err = cmd.Wait()
		c.exited = time.Now()
		close(c.done)
	}()
	return c, nil
}

func (s *Supervisor) stopChild(c *child) {
	if c == nil {
		return
	}
	_ = c.stdin.Close()
	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = c.cmd.Process.Kill()
	}
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		<-c.done
	}
}

func (s *Supervisor) proxy(sock string, st *supervisorState) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = "graphjin"
			r.Out.Host = r.In.Host
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			cur := st.get()
			msg := "GraphJin is restarting"
			if cur.State == StateStarting {
				msg = "GraphJin is starting"
			}
			if reason := cur.Reason; reason != "" {
				msg += ": " + reason
			} else if !errors.Is(err, context.Canceled) {
				msg += ": " + err.Error()
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": msg}}})
		},
	}
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, format+"\n", args...)
	}
}

func dialable(sock string) bool {
	c, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func configStamp(file string) string {
	fi, err := os.Stat(file)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", fi.ModTime().UnixNano(), fi.Size())
}
