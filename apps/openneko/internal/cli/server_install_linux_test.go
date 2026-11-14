package cli

import (
	"bytes"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/native"
)

// TestServerInstallZones creates real system users, so it runs only as root
// in a throwaway container: OPENNEKO_TEST_SERVER_INSTALL=1.
func TestServerInstallZones(t *testing.T) {
	if os.Getenv("OPENNEKO_TEST_SERVER_INSTALL") != "1" || os.Geteuid() != 0 {
		t.Skip("needs root and OPENNEKO_TEST_SERVER_INSTALL=1")
	}
	base, err := os.MkdirTemp("/", "openneko-zone-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"etc", "units", "opt/bin", "opt/share/openneko/web"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	o := serverInstallOptions{
		server: native.Server{ControlUser: "ozt-control", AgentUser: "ozt-agent", Group: "ozt-shared",
			ConfigDir: filepath.Join(base, "etc", "openneko"), DataDir: filepath.Join(base, "data"),
			UnitDir: filepath.Join(base, "units"), Exe: filepath.Join(base, "opt", "bin", "openneko")},
		databaseURL: "postgres://owner:control-secret@127.0.0.1:5432/neko?sslmode=disable",
		port:        3000, bind: "127.0.0.1",
	}
	cmd := newServerInstallCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runServerInstall(cmd, o); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if err := runServerInstall(cmd, o); err != nil {
		t.Fatalf("a second run: %v\n%s", err, out.String())
	}
	s, err := native.LoadServer(filepath.Join(o.server.ConfigDir, native.ServerFile))
	if err != nil {
		t.Fatal(err)
	}
	l := s.Layout()
	as := func(name string, script string) error {
		u, err := user.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		ids, _ := u.GroupIds()
		var groups []uint32
		for _, g := range ids {
			n, _ := strconv.Atoi(g)
			groups = append(groups, uint32(n))
		}
		c := exec.Command("/bin/sh", "-c", "umask 0007; "+script)
		c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}}
		c.Dir = "/"
		raw, err := c.CombinedOutput()
		if err != nil && testing.Verbose() {
			t.Logf("%s: %s: %s", name, script, strings.TrimSpace(string(raw)))
		}
		return err
	}
	if err := as(s.ControlUser, "mkdir -p "+l.Root+"/.config && echo secret > "+l.Root+"/.config/provider && echo log > "+l.Log("web")); err != nil {
		t.Fatal("the control user cannot write its own folder")
	}
	if err := as(s.ControlUser, "grep -q database_url "+s.ConfigDir+"/config.json"); err != nil {
		t.Fatal("the control user cannot read its config")
	}
	for _, path := range []string{
		filepath.Join(s.ConfigDir, "config.json"), filepath.Join(s.ConfigDir, "secret-key"),
		filepath.Join(s.ConfigDir, native.ServerFile), filepath.Join(s.ConfigDir, "installation.json"),
		filepath.Join(l.Root, ".config", "provider"), l.Log("web"),
	} {
		if err := as(s.AgentUser, "cat "+path); err == nil {
			t.Errorf("the agent-zone user read %s", path)
		}
	}
	for _, dir := range []string{s.ConfigDir, l.Root} {
		if err := as(s.AgentUser, "ls "+dir); err == nil {
			t.Errorf("the agent-zone user listed %s", dir)
		}
		if err := as(s.AgentUser, "touch "+dir+"/planted"); err == nil {
			t.Errorf("the agent-zone user wrote into %s", dir)
		}
	}
	if err := as(s.ControlUser, "ls "+l.Files()); err == nil {
		t.Error("the control user listed the agent's files")
	}

	creds := filepath.Join(l.Credentials(), "database-url")
	if err := as(s.ControlUser, "umask 0027; echo role > "+creds); err != nil {
		t.Fatal("the control user cannot write the serve credentials")
	}
	if err := as(s.AgentUser, "cat "+creds); err != nil {
		t.Error("the agent-zone user cannot read the serve credentials")
	}
	if err := as(s.AgentUser, "echo x > "+creds); err == nil {
		t.Error("the agent-zone user changed the serve credentials")
	}

	stage := filepath.Join(l.AgentRuns(), "stage")
	if err := as(s.ControlUser, "mkdir -m 0770 "+stage+" && echo in > "+stage+"/input"); err != nil {
		t.Fatal("the control user cannot stage a turn")
	}
	if err := as(s.AgentUser, "cat "+stage+"/input && mkdir -m 0775 "+stage+"/out && echo out > "+stage+"/out/answer"); err != nil {
		t.Error("the agent-zone user cannot use the stage")
	}
	if err := as(s.ControlUser, "cat "+stage+"/out/answer && rm -r "+stage); err != nil {
		t.Error("the control user cannot read and remove the stage")
	}
	if err := as(s.AgentUser, "touch "+l.Run()+"/serve.sock"); err != nil {
		t.Error("the agent-zone user cannot write the run folder")
	}
	if err := as(s.ControlUser, "touch "+l.Run()+"/planted"); err == nil {
		t.Error("the control user wrote into the run folder")
	}
	if err := as(s.ControlUser, "echo cfg > "+l.GraphJin()); err != nil {
		t.Fatal("the control user cannot write the GraphJin config")
	}
	if err := as(s.AgentUser, "cat "+l.GraphJin()+" && echo ack > "+filepath.Dir(l.GraphJin())+"/.ack"); err != nil {
		t.Error("the agent-zone user cannot use the GraphJin folder")
	}
	for _, name := range []string{native.WebUnit, native.ServeUnit} {
		if _, err := os.Stat(filepath.Join(s.UnitDir, name)); err != nil {
			t.Error(err)
		}
	}
}
