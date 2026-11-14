package native

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func testServer() Server {
	return Server{ControlUser: "openneko", AgentUser: "openneko-agent", Group: "openneko-shared",
		ConfigDir: "/etc/openneko", DataDir: "/var/lib/openneko", UnitDir: "/etc/systemd/system",
		Exe: "/opt/openneko/bin/openneko", WebDir: "/opt/openneko/share/openneko/web",
		WebAddr: "0.0.0.0:3000", PublicURL: "http://localhost:3000"}
}

func TestServerDirsKeepTheAgentOutOfTheControlZone(t *testing.T) {
	s := testServer()
	l := s.Layout()
	for _, d := range s.Dirs() {
		if d.Mode&0o007 != 0 {
			t.Errorf("%s is open to others: %v", d.Path, d.Mode)
		}
		control := d.Path == s.ConfigDir || d.Path == l.Root || strings.HasPrefix(d.Path, l.Root+"/")
		if control && (d.Owner != s.ControlUser || d.Group != s.ControlUser || d.Mode.Perm() != 0o700) {
			t.Errorf("control folder %s: %s:%s %v", d.Path, d.Owner, d.Group, d.Mode)
		}
		if d.Path == l.Agent || strings.HasPrefix(d.Path, l.Agent+"/") {
			if d.Owner != s.AgentUser || d.Mode.Perm() != 0o700 {
				t.Errorf("agent folder %s: %s %v", d.Path, d.Owner, d.Mode)
			}
		}
	}
	want := map[string]Dir{
		l.Run():                      {Owner: s.AgentUser, Group: s.Group, Mode: fs.ModeSetgid | 0o750},
		l.AgentRuns():                {Owner: s.ControlUser, Group: s.Group, Mode: fs.ModeSetgid | 0o770},
		filepath.Dir(l.GraphJin()):   {Owner: s.ControlUser, Group: s.Group, Mode: fs.ModeSetgid | 0o770},
		l.Credentials():              {Owner: s.ControlUser, Group: s.AgentUser, Mode: fs.ModeSetgid | 0o750},
		filepath.Dir(l.Log("serve")): {Owner: s.AgentUser, Group: s.AgentUser, Mode: 0o700},
	}
	found := 0
	for _, d := range s.Dirs() {
		if w, ok := want[d.Path]; ok {
			found++
			if d.Owner != w.Owner || d.Group != w.Group || d.Mode != w.Mode {
				t.Errorf("%s: %s:%s %v, want %s:%s %v", d.Path, d.Owner, d.Group, d.Mode, w.Owner, w.Group, w.Mode)
			}
		}
	}
	if found != len(want) {
		t.Fatalf("found %d of the %d shared folders", found, len(want))
	}
}

func TestServerUnits(t *testing.T) {
	s := testServer()
	l := s.Layout()
	units := s.Units()
	web, serve := units[WebUnit], units[ServeUnit]
	for _, line := range []string{"User=openneko\n", "UMask=0007\n", "ExecStartPre=/opt/openneko/bin/openneko native-prepare --server /etc/openneko/server.json\n",
		"ExecStart=/opt/openneko/bin/openneko web\n", "Environment=\"XDG_CONFIG_HOME=/etc\"\n", "InaccessiblePaths=" + l.Agent + "\n",
		"StandardOutput=append:" + l.Log("web") + "\n"} {
		if !strings.Contains(web, line) {
			t.Errorf("web unit lacks %q:\n%s", line, web)
		}
	}
	for _, line := range []string{"User=openneko-agent\n", "ExecStart=/opt/openneko/bin/openneko serve\n", "InaccessiblePaths=/etc/openneko " + l.Root + "\n",
		"Environment=\"OPENNEKO_SERVE_CREDENTIALS=" + l.Credentials() + "\"\n", "After=network-online.target " + WebUnit + "\n"} {
		if !strings.Contains(serve, line) {
			t.Errorf("serve unit lacks %q:\n%s", line, serve)
		}
	}
	for _, leak := range []string{"/etc/openneko\"", "DATABASE_URL", "ExecStartPre", l.Root + "/"} {
		if strings.Contains(serve, leak) {
			t.Errorf("serve unit holds %q:\n%s", leak, serve)
		}
	}
}

func TestServerOneUser(t *testing.T) {
	s := testServer()
	s.AgentUser, s.Group = s.ControlUser, s.ControlUser
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, d := range s.Dirs() {
		if d.Owner != "root" && d.Owner != s.ControlUser {
			t.Errorf("%s owned by %s", d.Path, d.Owner)
		}
	}
	for name, u := range s.Units() {
		if strings.Contains(u, "InaccessiblePaths") || !strings.Contains(u, "User=openneko\n") {
			t.Errorf("%s:\n%s", name, u)
		}
	}
}

func TestServerValidate(t *testing.T) {
	for name, change := range map[string]func(*Server){
		"group is a user":   func(s *Server) { s.Group = s.AgentUser },
		"relative data":     func(s *Server) { s.DataDir = "var/lib/openneko" },
		"space in exe":      func(s *Server) { s.Exe = "/opt/open neko/bin/openneko" },
		"config base":       func(s *Server) { s.ConfigDir = "/etc/neko" },
		"bad user":          func(s *Server) { s.ControlUser = "Root;" },
		"bad web address":   func(s *Server) { s.WebAddr = "3000" },
		"percent in folder": func(s *Server) { s.DataDir = "/var/lib/%h" },
	} {
		s := testServer()
		change(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := testServer().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOneUserLayoutIsUnchanged(t *testing.T) {
	l := Layout{Root: "/r"}
	for got, want := range map[string]string{l.Files(): "/r/files", l.Credentials(): "/r/credentials", l.Run(): "/r/run",
		l.Log("serve"): "/r/logs/serve.log", l.Log("web"): "/r/logs/web.log", l.GraphJin(): "/r/graphjin/agentic.yml"} {
		if got != want {
			t.Errorf("%s, want %s", got, want)
		}
	}
}
