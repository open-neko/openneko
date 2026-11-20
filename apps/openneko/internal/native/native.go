// Package native runs OpenNeko on the host as plain processes: openneko
// serve and openneko web under one supervisor, with no container.
package native

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/ownonly"
)

type Layout struct {
	Root   string
	Agent  string
	Shared string
}

func (l Layout) agent() string {
	if l.Agent != "" {
		return l.Agent
	}
	return l.Root
}

func (l Layout) shared() string {
	if l.Shared != "" {
		return l.Shared
	}
	return l.Root
}

func (l Layout) Files() string         { return filepath.Join(l.agent(), "files") }
func (l Layout) Index() string         { return filepath.Join(l.agent(), "file-index") }
func (l Layout) Subscriptions() string { return filepath.Join(l.agent(), "subscriptions") }
func (l Layout) Credentials() string   { return filepath.Join(l.shared(), "credentials") }
func (l Layout) GraphJin() string      { return filepath.Join(l.shared(), "graphjin", "agentic.yml") }
func (l Layout) Seed() string          { return filepath.Join(l.shared(), "graphjin", "seed.yml") }
func (l Layout) Run() string           { return filepath.Join(l.shared(), "run") }
func (l Layout) Door() string          { return filepath.Join(l.shared(), "door") }
func (l Layout) AgentRuns() string     { return filepath.Join(l.shared(), "agent-runs") }
func (l Layout) WebAdmin() string      { return filepath.Join(l.Root, "web-admin.sock") }
func (l Layout) Logs() string          { return filepath.Join(l.Root, "logs") }
func (l Layout) State() string         { return filepath.Join(l.Run(), "state.json") }
func (l Layout) Lock() string          { return filepath.Join(l.Run(), "supervisor.lock") }
func (l Layout) Log(name string) string {
	if name == "serve" {
		return filepath.Join(l.agent(), "logs", "serve.log")
	}
	return filepath.Join(l.Logs(), name+".log")
}

// DataFolders maps each data folder that a backup holds to its path.
func (l Layout) DataFolders() map[string]string {
	return map[string]string{
		"files": l.Files(), "file-index": l.Index(), "subscriptions": l.Subscriptions(),
		"credentials": l.Credentials(), "graphjin": filepath.Dir(l.GraphJin()), "agent-runs": l.AgentRuns(),
	}
}

// MaxSocketPath is the longest Unix socket path that net.Listen accepts here.
func MaxSocketPath() int {
	switch runtime.GOOS {
	case "linux", "windows":
		return 107
	default:
		return 103
	}
}

// CheckSocketPath fails with one line when path is too long for a Unix socket.
func CheckSocketPath(path string) error {
	if n := len(path); n > MaxSocketPath() {
		return fmt.Errorf("the socket path %s has %d bytes and %s allows %d; set XDG_DATA_HOME to a shorter folder or use a shorter instance name", path, n, runtime.GOOS, MaxSocketPath())
	}
	return nil
}

func (l Layout) sockets() []string {
	paths := []string{l.WebAdmin()}
	for k, v := range sharedEnv(l) {
		if strings.HasSuffix(k, "_SOCKET") {
			paths = append(paths, v)
		}
	}
	sort.Strings(paths)
	return paths
}

func (l Layout) Prepare() error {
	for _, path := range l.sockets() {
		if err := CheckSocketPath(path); err != nil {
			return err
		}
	}
	for _, dir := range []string{l.Root, l.Files(), l.Index(), l.Credentials(), filepath.Dir(l.GraphJin()), l.Run(), l.Door(), l.Logs(), l.AgentRuns(), l.Subscriptions()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
		if err := ownonly.Set(dir); err != nil {
			return err
		}
	}
	return nil
}

const (
	ServeAddr         = "127.0.0.1:8090"
	GraphJinAddr      = "127.0.0.1:8080"
	SubscriptionsAddr = "127.0.0.1:8089"
)

type Options struct {
	Exe       string
	WebDir    string
	WebAddr   string
	PublicURL string
	Env       []string
}

type Proc struct {
	Name string
	Args []string
	Env  []string
}

func (o Options) env(extra map[string]string) []string {
	out := make([]string, 0, len(o.Env)+len(extra))
	for _, kv := range o.Env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			if _, set := extra[k]; set {
				continue
			}
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

func sharedEnv(l Layout) map[string]string {
	return map[string]string{
		"OPENNEKO_GRAPHJIN_CONFIG":     l.GraphJin(),
		"OPENNEKO_SERVE_SOCKET":        filepath.Join(l.Run(), "serve.sock"),
		"OPENNEKO_CONFIG_CHECK_SOCKET": filepath.Join(l.Run(), "config-check.sock"),
		"OPENNEKO_KEY_DOOR_SOCKET":     filepath.Join(l.Door(), "keydoor.sock"),
		"OPENNEKO_AGENT_RUNS_DIR":      l.AgentRuns(),
		"OPENNEKO_FILES_DIR":           l.Files(),
		"OPENNEKO_FILES_INDEX_DIR":     l.Index(),
		"OPENNEKO_IN_CONTAINER":        "false",
	}
}

// Steps run in order before the processes start; the first failure stops
// the start.
func Steps(l Layout, o Options, migrate bool) []Proc {
	env := o.env(sharedEnv(l))
	steps := []Proc{{Name: "database check", Args: []string{o.Exe, "database", "check"}, Env: env}}
	if migrate {
		steps = append(steps, Proc{Name: "migrate", Args: []string{o.Exe, "migrate"}, Env: env})
	}
	return append(steps, Proc{Name: "serve-init", Args: []string{o.Exe, "serve-init", "--dir", l.Credentials(), "--graphjin-config", l.GraphJin(), "--graphjin-seed", l.Seed()}, Env: env})
}

func ServeEnv(l Layout) map[string]string {
	serve := sharedEnv(l)
	serve["OPENNEKO_SERVE_CREDENTIALS"] = l.Credentials()
	serve["OPENNEKO_SERVE_ADDR"] = ServeAddr
	serve["OPENNEKO_GRAPHJIN_ADDR"] = GraphJinAddr
	serve["OPENNEKO_SUBSCRIPTIONS_ADDR"] = SubscriptionsAddr
	serve["OPENNEKO_SUBSCRIPTIONS_DIR"] = l.Subscriptions()
	return serve
}

func WebEnv(l Layout, o Options) map[string]string {
	web := sharedEnv(l)
	web["OPENNEKO_ENV"] = "production"
	web["OPENNEKO_WEB_ADDR"] = o.WebAddr
	web["OPENNEKO_WEB_DIR"] = o.WebDir
	web["OPENNEKO_PUBLIC_URL"] = o.PublicURL
	web["OPENNEKO_SERVE_URL"] = "http://" + ServeAddr
	web["OPENNEKO_GRAPHJIN_URL"] = "http://" + SubscriptionsAddr
	web["OPENNEKO_WEB_ADMIN_SOCKET"] = l.WebAdmin()
	if !hasEnv(o.Env, "OPENNEKO_METRICS_REFRESH") {
		web["OPENNEKO_METRICS_REFRESH"] = "off"
	}
	return web
}

func Procs(l Layout, o Options) []Proc {
	return []Proc{
		{Name: "serve", Args: []string{o.Exe, "serve"}, Env: Options{Env: ServeEnviron(o.Env)}.env(ServeEnv(l))},
		{Name: "web", Args: []string{o.Exe, "web"}, Env: o.env(WebEnv(l, o))},
	}
}

var serveOwnSecrets = map[string]bool{"OPENNEKO_SANDBOX_KEY": true, "OPENNEKO_FILES_BUCKET_KEY_ID": true, "OPENNEKO_FILES_BUCKET_SECRET": true}

// ServeEnviron drops the control zone's secrets from env: model and
// provider keys, tokens, passwords, database URLs and the secret key. Serve
// keeps its own sandbox key, its bucket login and GraphJin's GJ_ variables.
func ServeEnviron(env []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if serveOwnSecrets[upper] || strings.HasPrefix(upper, "GJ_") || !secretEnvName(upper) {
			out = append(out, kv)
		}
	}
	return out
}

func secretEnvName(upper string) bool {
	for _, part := range []string{"KEY", "TOKEN", "SECRET", "PASS", "CREDENTIAL", "DATABASE_URL", "DSN", "AUTH"} {
		if strings.Contains(upper, part) {
			return true
		}
	}
	return false
}

func hasEnv(env []string, key string) bool {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// ProbeHost turns a listen address into one a local client can dial.
func ProbeHost(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("web address %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}
