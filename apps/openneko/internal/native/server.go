package native

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	ServerFile   = "server.json"
	WebUnit      = "openneko-web.service"
	ServeUnit    = "openneko-serve.service"
	configDirApp = "openneko"
)

// Server is a native install that systemd runs: web and serve as one
// system user. Old names the accounts of an install made with two.
type Server struct {
	User        string      `json:"user"`
	ConfigDir   string      `json:"config_dir"`
	DataDir     string      `json:"data_dir"`
	UnitDir     string      `json:"unit_dir"`
	Exe         string      `json:"exe"`
	WebDir      string      `json:"web_dir"`
	WebAddr     string      `json:"web_addr"`
	PublicURL   string      `json:"public_url"`
	SandboxDir  string      `json:"sandbox_dir,omitempty"`
	TmpfilesDir string      `json:"tmpfiles_dir,omitempty"`
	Old         OldAccounts `json:"-"`
}

// OldAccounts are the agent user and the shared group of a two-account
// install. OpenNeko never deletes them.
type OldAccounts struct {
	AgentUser string
	Group     string
}

func (o OldAccounts) Any() bool { return o.AgentUser != "" || o.Group != "" }

const SandboxTmpfile = "openneko-sandbox.conf"

func (s Server) Layout() Layout {
	return Layout{
		Root:   filepath.Join(s.DataDir, "control"),
		Agent:  filepath.Join(s.DataDir, "agent"),
		Shared: filepath.Join(s.DataDir, "shared"),
	}
}

func (s Server) Validate() error {
	if !validName(s.User) {
		return fmt.Errorf("the user %q is not a valid name", s.User)
	}
	paths := map[string]string{"config folder": s.ConfigDir, "data folder": s.DataDir, "unit folder": s.UnitDir, "executable": s.Exe, "web folder": s.WebDir}
	for name, v := range map[string]string{"sandbox socket folder": s.SandboxDir, "tmpfiles folder": s.TmpfilesDir} {
		if v != "" {
			paths[name] = v
		}
	}
	for name, v := range paths {
		if !filepath.IsAbs(v) || strings.ContainsAny(v, " \t\n\"'\\%$") {
			return fmt.Errorf("the %s %q must be an absolute path with no spaces or quotes", name, v)
		}
	}
	if filepath.Base(s.ConfigDir) != configDirApp {
		return fmt.Errorf("the config folder must end in /%s", configDirApp)
	}
	if _, err := ProbeHost(s.WebAddr); err != nil {
		return err
	}
	return nil
}

func validName(v string) bool {
	if v == "" || len(v) > 32 || v[0] == '-' {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Dirs lists each folder of the install. The user owns each one, mode 0700.
func (s Server) Dirs() []string {
	l := s.Layout()
	dirs := []string{s.ConfigDir, s.DataDir, l.Root, l.Logs(), l.agent(), l.Files(), l.Index(), l.Subscriptions(), l.Tmp(),
		filepath.Dir(l.Log("serve")), l.shared(), l.Run(), l.Check(), l.Door(), l.AgentRuns(), filepath.Dir(l.GraphJin()), l.Credentials()}
	if s.SandboxDir != "" {
		dirs = append(dirs, s.SandboxDir)
	}
	return dirs
}

// SandboxTmpfileText makes systemd-tmpfiles create the sandbox socket folder
// again at each boot, as /run is empty then.
func (s Server) SandboxTmpfileText() string {
	if s.SandboxDir == "" {
		return ""
	}
	return fmt.Sprintf("d %s 0700 %s %s -\n", s.SandboxDir, s.User, s.User)
}

func (s Server) WebEnv() map[string]string {
	l := s.Layout()
	env := WebEnv(l, Options{WebDir: s.WebDir, WebAddr: s.WebAddr, PublicURL: s.PublicURL})
	env["HOME"] = l.Root
	env["XDG_DATA_HOME"] = l.Root
	env["XDG_CONFIG_HOME"] = filepath.Dir(s.ConfigDir)
	return env
}

func (s Server) ServeEnv() map[string]string {
	l := s.Layout()
	env := ServeEnv(l)
	env["HOME"] = l.agent()
	env["XDG_DATA_HOME"] = l.agent()
	env["XDG_CONFIG_HOME"] = l.agent()
	return env
}

func (s Server) Units() map[string]string {
	l := s.Layout()
	web := unit{
		description: "OpenNeko web",
		wants:       "network-online.target " + ServeUnit,
		after:       "network-online.target",
		user:        s.User,
		envFile:     filepath.Join(s.ConfigDir, "web.env"),
		env:         s.WebEnv(),
		pre:         s.Exe + " native-prepare --server " + filepath.Join(s.ConfigDir, ServerFile),
		start:       s.Exe + " web",
		log:         l.Log("web"),
		writable:    []string{s.ConfigDir, l.Root, l.shared()},
	}
	serve := unit{
		description: "OpenNeko serve",
		wants:       WebUnit,
		after:       "network-online.target " + WebUnit,
		user:        s.User,
		envFile:     filepath.Join(s.ConfigDir, "serve.env"),
		env:         s.ServeEnv(),
		start:       s.Exe + " serve",
		log:         l.Log("serve"),
		writable:    []string{l.agent(), l.shared()},
		hidden:      []string{s.ConfigDir, l.Root},
	}
	web.hidden = []string{l.agent()}
	return map[string]string{WebUnit: web.String(), ServeUnit: serve.String()}
}

type unit struct {
	description, wants, after, user, envFile, pre, start, log string
	env                                                       map[string]string
	writable, hidden                                          []string
}

func (u unit) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\nWants=%s\nAfter=%s\n\n[Service]\nType=simple\n", u.description, u.wants, u.after)
	fmt.Fprintf(&b, "User=%s\nGroup=%s\nUMask=0007\n", u.user, u.user)
	fmt.Fprintf(&b, "EnvironmentFile=-%s\n", u.envFile)
	keys := make([]string, 0, len(u.env))
	for k := range u.env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "Environment=\"%s=%s\"\n", k, u.env[k])
	}
	if u.pre != "" {
		fmt.Fprintf(&b, "ExecStartPre=%s\nTimeoutStartSec=300\n", u.pre)
	}
	fmt.Fprintf(&b, "ExecStart=%s\nRestart=always\nRestartSec=2\n", u.start)
	fmt.Fprintf(&b, "StandardOutput=append:%s\nStandardError=inherit\n", u.log)
	b.WriteString("NoNewPrivileges=yes\nPrivateTmp=yes\nProtectSystem=strict\nProtectHome=yes\n")
	fmt.Fprintf(&b, "ReadWritePaths=%s\n", strings.Join(u.writable, " "))
	if len(u.hidden) > 0 {
		fmt.Fprintf(&b, "InaccessiblePaths=%s\n", strings.Join(u.hidden, " "))
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

func SaveServer(s Server) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.ConfigDir, ServerFile), append(raw, '\n'), 0o600)
}

func LoadServer(path string) (Server, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Server{}, err
	}
	var s Server
	var old struct {
		ControlUser string `json:"control_user"`
		AgentUser   string `json:"agent_user"`
		Group       string `json:"group"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return Server{}, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		return Server{}, fmt.Errorf("read %s: %w", path, err)
	}
	if s.User == "" {
		s.User = old.ControlUser
	}
	for _, name := range []*string{&old.AgentUser, &old.Group} {
		if *name == s.User || !validName(*name) {
			*name = ""
		}
	}
	s.Old = OldAccounts{AgentUser: old.AgentUser, Group: old.Group}
	return s, s.Validate()
}

// BaseOf names the folder of the install, config, data or sandbox, that
// holds dir.
func (s Server) BaseOf(dir string) string {
	for _, base := range []string{s.DataDir, s.ConfigDir, s.SandboxDir} {
		if base == "" {
			continue
		}
		if rel, err := filepath.Rel(base, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return base
		}
	}
	return dir
}
