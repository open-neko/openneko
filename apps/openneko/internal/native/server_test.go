package native

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func testServer(t *testing.T) Server {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the server install writes systemd units and runs on Linux only; cli/server_install.go refuses it on Windows")
	}
	return Server{User: "openneko",
		ConfigDir: "/etc/openneko", DataDir: "/var/lib/openneko", UnitDir: "/etc/systemd/system",
		Exe: "/opt/openneko/bin/openneko", WebDir: "/opt/openneko/share/openneko/web",
		WebAddr: "0.0.0.0:3000", PublicURL: "http://localhost:3000"}
}

func TestServerDirsHoldEveryFolderOnce(t *testing.T) {
	s := testServer(t)
	l := s.Layout()
	seen := map[string]bool{}
	for _, d := range s.Dirs() {
		if seen[d] {
			t.Errorf("%s is listed twice", d)
		}
		seen[d] = true
	}
	for _, want := range append([]string{s.ConfigDir, s.DataDir, l.Root, l.Logs(), filepath.Dir(l.Log("serve")), l.Run(), l.Check(), l.Door(),
		filepath.Dir(l.GraphJin()), l.Tmp(), l.AgentRuns()}, slices.Collect(maps.Values(l.DataFolders()))...) {
		if !seen[want] {
			t.Errorf("Dirs lacks %s", want)
		}
	}
}

func TestServerUnits(t *testing.T) {
	s := testServer(t)
	l := s.Layout()
	units := s.Units()
	web, serve := units[WebUnit], units[ServeUnit]
	for _, line := range []string{"User=openneko\n", "Group=openneko\n", "UMask=0007\n", "ExecStartPre=/opt/openneko/bin/openneko native-prepare --server /etc/openneko/server.json\n",
		"ExecStart=/opt/openneko/bin/openneko web\n", "Environment=\"XDG_CONFIG_HOME=/etc\"\n", "InaccessiblePaths=" + l.Agent + "\n",
		"StandardOutput=append:" + l.Log("web") + "\n"} {
		if !strings.Contains(web, line) {
			t.Errorf("web unit lacks %q:\n%s", line, web)
		}
	}
	for _, line := range []string{"User=openneko\n", "Group=openneko\n", "ExecStart=/opt/openneko/bin/openneko serve\n", "InaccessiblePaths=/etc/openneko " + l.Root + "\n",
		"Environment=\"OPENNEKO_SERVE_CREDENTIALS=" + l.Credentials() + "\"\n", "After=network-online.target " + WebUnit + "\n"} {
		if !strings.Contains(serve, line) {
			t.Errorf("serve unit lacks %q:\n%s", line, serve)
		}
	}
	for _, unit := range []string{web, serve} {
		for _, line := range []string{"NoNewPrivileges=yes\n", "PrivateTmp=yes\n", "ProtectSystem=strict\n", "ProtectHome=yes\n", "ReadWritePaths="} {
			if !strings.Contains(unit, line) {
				t.Errorf("unit lacks %q:\n%s", line, unit)
			}
		}
		if strings.Contains(unit, "openneko-agent") || strings.Contains(unit, "openneko-shared") {
			t.Errorf("unit names an account of the two-account install:\n%s", unit)
		}
	}
	for _, leak := range []string{"/etc/openneko\"", "DATABASE_URL", "ExecStartPre", l.Root + "/"} {
		if strings.Contains(serve, leak) {
			t.Errorf("serve unit holds %q:\n%s", leak, serve)
		}
	}
}

func TestLoadServerOfTwoAccounts(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	s.ConfigDir = filepath.Join(dir, "openneko")
	if err := os.MkdirAll(s.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.ConfigDir, ServerFile)
	for name, c := range map[string]struct {
		json string
		old  OldAccounts
	}{
		"two accounts": {`"control_user":"openneko","agent_user":"openneko-agent","group":"openneko-shared"`, OldAccounts{"openneko-agent", "openneko-shared"}},
		"one user":     {`"control_user":"openneko","agent_user":"openneko","group":"openneko"`, OldAccounts{}},
		"this release": {`"user":"openneko"`, OldAccounts{}},
	} {
		raw := fmt.Sprintf(`{%s,"config_dir":%q,"data_dir":%q,"unit_dir":%q,"exe":%q,"web_dir":%q,"web_addr":%q,"public_url":%q}`,
			c.json, s.ConfigDir, s.DataDir, s.UnitDir, s.Exe, s.WebDir, s.WebAddr, s.PublicURL)
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadServer(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.User != "openneko" || got.Old != c.old {
			t.Errorf("%s: user %q, old %+v, want %+v", name, got.User, got.Old, c.old)
		}
		if err := SaveServer(got); err != nil {
			t.Fatal(err)
		}
		if again, err := LoadServer(path); err != nil || again.Old.Any() {
			t.Errorf("%s: saved again: %+v %v", name, again.Old, err)
		}
	}
}

func TestServerValidate(t *testing.T) {
	for name, change := range map[string]func(*Server){
		"no user":           func(s *Server) { s.User = "" },
		"relative data":     func(s *Server) { s.DataDir = "var/lib/openneko" },
		"space in exe":      func(s *Server) { s.Exe = "/opt/open neko/bin/openneko" },
		"config base":       func(s *Server) { s.ConfigDir = "/etc/neko" },
		"bad user":          func(s *Server) { s.User = "Root;" },
		"bad web address":   func(s *Server) { s.WebAddr = "3000" },
		"percent in folder": func(s *Server) { s.DataDir = "/var/lib/%h" },
	} {
		s := testServer(t)
		change(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := testServer(t).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOneUserLayoutIsUnchanged(t *testing.T) {
	l := Layout{Root: "/r"}
	for got, want := range map[string]string{l.Files(): "/r/files", l.Credentials(): "/r/credentials", l.Run(): "/r/run",
		l.Log("serve"): "/r/logs/serve.log", l.Log("web"): "/r/logs/web.log", l.GraphJin(): "/r/graphjin/agentic.yml"} {
		if want = filepath.FromSlash(want); got != want {
			t.Errorf("%s, want %s", got, want)
		}
	}
}

func TestServerSandboxSocketDir(t *testing.T) {
	s := testServer(t)
	if slices.Contains(s.Dirs(), "") || s.SandboxTmpfileText() != "" {
		t.Fatal("a server with no sandbox folder lists one")
	}
	s.SandboxDir, s.TmpfilesDir = "/run/openneko-sandbox", "/etc/tmpfiles.d"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.Dirs(), s.SandboxDir) || s.SandboxDir == s.Layout().Run() {
		t.Errorf("dirs %v", s.Dirs())
	}
	if got := s.SandboxTmpfileText(); got != "d /run/openneko-sandbox 0700 openneko openneko -\n" {
		t.Errorf("tmpfile %q", got)
	}
	s.SandboxDir = "run/openneko-sandbox"
	if err := s.Validate(); err == nil {
		t.Error("a relative sandbox folder passed")
	}
}
