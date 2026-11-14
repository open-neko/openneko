// Package native runs OpenNeko on the host as plain processes: openneko
// serve and openneko web under one supervisor, with no container.
package native

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type Layout struct{ Root string }

func (l Layout) Files() string         { return filepath.Join(l.Root, "files") }
func (l Layout) Index() string         { return filepath.Join(l.Root, "file-index") }
func (l Layout) Credentials() string   { return filepath.Join(l.Root, "credentials") }
func (l Layout) GraphJin() string      { return filepath.Join(l.Root, "graphjin", "agentic.yml") }
func (l Layout) Seed() string          { return filepath.Join(l.Root, "graphjin", "seed.yml") }
func (l Layout) Run() string           { return filepath.Join(l.Root, "run") }
func (l Layout) Logs() string          { return filepath.Join(l.Root, "logs") }
func (l Layout) AgentRuns() string     { return filepath.Join(l.Root, "agent-runs") }
func (l Layout) Subscriptions() string { return filepath.Join(l.Root, "subscriptions") }
func (l Layout) State() string         { return filepath.Join(l.Run(), "state.json") }
func (l Layout) Lock() string          { return filepath.Join(l.Run(), "supervisor.lock") }
func (l Layout) Log(name string) string {
	return filepath.Join(l.Logs(), name+".log")
}

func (l Layout) Prepare() error {
	for _, dir := range []string{l.Root, l.Files(), l.Index(), l.Credentials(), filepath.Dir(l.GraphJin()), l.Run(), l.Logs(), l.AgentRuns(), l.Subscriptions()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

const (
	ServeAddr         = "127.0.0.1:8090"
	GraphJinAddr      = "127.0.0.1:8080"
	SubscriptionsAddr = "127.0.0.1:8089"
	nodeAddr          = "127.0.0.1:3001"
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

func (o Options) shared(l Layout) map[string]string {
	return map[string]string{
		"OPENNEKO_GRAPHJIN_CONFIG":     l.GraphJin(),
		"OPENNEKO_SERVE_SOCKET":        filepath.Join(l.Run(), "serve.sock"),
		"OPENNEKO_CONFIG_CHECK_SOCKET": filepath.Join(l.Run(), "config-check.sock"),
		"OPENNEKO_AGENT_RUNS_DIR":      l.AgentRuns(),
		"OPENNEKO_FILES_DIR":           l.Files(),
		"OPENNEKO_FILES_INDEX_DIR":     l.Index(),
		"OPENNEKO_IN_CONTAINER":        "false",
	}
}

// Steps run in order before the processes start; the first failure stops
// the start.
func Steps(l Layout, o Options, migrate bool) []Proc {
	env := o.env(o.shared(l))
	steps := []Proc{{Name: "database check", Args: []string{o.Exe, "database", "check"}, Env: env}}
	if migrate {
		steps = append(steps, Proc{Name: "migrate", Args: []string{o.Exe, "migrate"}, Env: env})
	}
	return append(steps, Proc{Name: "serve-init", Args: []string{o.Exe, "serve-init", "--dir", l.Credentials(), "--graphjin-config", l.GraphJin(), "--graphjin-seed", l.Seed()}, Env: env})
}

func Procs(l Layout, o Options) []Proc {
	serve := o.shared(l)
	serve["OPENNEKO_SERVE_CREDENTIALS"] = l.Credentials()
	serve["OPENNEKO_SERVE_ADDR"] = ServeAddr
	serve["OPENNEKO_GRAPHJIN_ADDR"] = GraphJinAddr
	serve["OPENNEKO_SUBSCRIPTIONS_ADDR"] = SubscriptionsAddr
	serve["OPENNEKO_SUBSCRIPTIONS_DIR"] = l.Subscriptions()

	web := o.shared(l)
	web["NODE_ENV"] = "production"
	web["OPENNEKO_WEB_ADDR"] = o.WebAddr
	web["OPENNEKO_WEB_DIR"] = o.WebDir
	web["OPENNEKO_WEB_NODE_ADDR"] = nodeAddr
	web["OPENNEKO_PUBLIC_URL"] = o.PublicURL
	web["OPENNEKO_SERVE_URL"] = "http://" + ServeAddr
	web["OPENNEKO_GRAPHJIN_URL"] = "http://" + SubscriptionsAddr
	web["WORKER_ADMIN_URL"] = "unix://" + filepath.Join(l.Run(), "worker.sock")
	if !hasEnv(o.Env, "OPENNEKO_METRICS_REFRESH") {
		web["OPENNEKO_METRICS_REFRESH"] = "off"
	}
	return []Proc{
		{Name: "serve", Args: []string{o.Exe, "serve"}, Env: o.env(serve)},
		{Name: "web", Args: []string{o.Exe, "web"}, Env: o.env(web)},
	}
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
