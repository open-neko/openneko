package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// Server is a native install that systemd runs: web in the control zone and
// serve in the agent zone, each as its own system user.
type Server struct {
	ControlUser string `json:"control_user"`
	AgentUser   string `json:"agent_user"`
	Group       string `json:"group"`
	ConfigDir   string `json:"config_dir"`
	DataDir     string `json:"data_dir"`
	UnitDir     string `json:"unit_dir"`
	Exe         string `json:"exe"`
	WebDir      string `json:"web_dir"`
	WebAddr     string `json:"web_addr"`
	PublicURL   string `json:"public_url"`
}

func (s Server) OneUser() bool { return s.ControlUser == s.AgentUser }

func (s Server) Layout() Layout {
	return Layout{
		Root:   filepath.Join(s.DataDir, "control"),
		Agent:  filepath.Join(s.DataDir, "agent"),
		Shared: filepath.Join(s.DataDir, "shared"),
	}
}

func (s Server) Validate() error {
	for name, v := range map[string]string{"control user": s.ControlUser, "agent user": s.AgentUser, "group": s.Group} {
		if !validName(v) {
			return fmt.Errorf("the %s %q is not a valid name", name, v)
		}
	}
	for name, v := range map[string]string{"config folder": s.ConfigDir, "data folder": s.DataDir, "unit folder": s.UnitDir, "executable": s.Exe, "web folder": s.WebDir} {
		if !filepath.IsAbs(v) || strings.ContainsAny(v, " \t\n\"'\\%$") {
			return fmt.Errorf("the %s %q must be an absolute path with no spaces or quotes", name, v)
		}
	}
	if filepath.Base(s.ConfigDir) != configDirApp {
		return fmt.Errorf("the config folder must end in /%s", configDirApp)
	}
	if !s.OneUser() && (s.Group == s.ControlUser || s.Group == s.AgentUser) {
		return errors.New("the shared group must differ from the two users")
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

type Dir struct {
	Path  string
	Owner string
	Group string
	Mode  fs.FileMode
}

// Dirs lists each folder with its owner and the least mode that works. The
// agent zone gets no way into the control folders, and the control zone
// reaches the agent zone only through the shared folders.
func (s Server) Dirs() []Dir {
	l := s.Layout()
	c, a, g := s.ControlUser, s.AgentUser, s.Group
	return []Dir{
		{s.ConfigDir, c, c, 0o700},
		{s.DataDir, "root", g, 0o750},
		{l.Root, c, c, 0o700},
		{l.Logs(), c, c, 0o700},
		{l.agent(), a, a, 0o700},
		{l.Files(), a, a, 0o700},
		{l.Index(), a, a, 0o700},
		{l.Subscriptions(), a, a, 0o700},
		{filepath.Dir(l.Log("serve")), a, a, 0o700},
		{l.shared(), "root", g, 0o750},
		{l.Run(), a, g, fs.ModeSetgid | 0o750},
		{l.AgentRuns(), c, g, fs.ModeSetgid | 0o770},
		{filepath.Dir(l.GraphJin()), c, g, fs.ModeSetgid | 0o770},
		{l.Credentials(), c, a, fs.ModeSetgid | 0o750},
	}
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
		description: "OpenNeko web (control zone)",
		wants:       "network-online.target " + ServeUnit,
		after:       "network-online.target",
		user:        s.ControlUser,
		envFile:     filepath.Join(s.ConfigDir, "web.env"),
		env:         s.WebEnv(),
		pre:         s.Exe + " native-prepare --server " + filepath.Join(s.ConfigDir, ServerFile),
		start:       s.Exe + " web",
		log:         l.Log("web"),
		writable:    []string{s.ConfigDir, l.Root, l.shared()},
	}
	serve := unit{
		description: "OpenNeko serve (agent zone)",
		wants:       WebUnit,
		after:       "network-online.target " + WebUnit,
		user:        s.AgentUser,
		envFile:     filepath.Join(s.ConfigDir, "serve.env"),
		env:         s.ServeEnv(),
		start:       s.Exe + " serve",
		log:         l.Log("serve"),
		writable:    []string{l.agent(), l.shared()},
	}
	if !s.OneUser() {
		web.hidden = []string{l.agent()}
		serve.hidden = []string{s.ConfigDir, l.Root}
	}
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
	if err := json.Unmarshal(raw, &s); err != nil {
		return Server{}, fmt.Errorf("read %s: %w", path, err)
	}
	return s, s.Validate()
}
