package cli

import (
	"bytes"
	"context"
	"io"
	"io/fs"
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

const testServerUser = "ozt-openneko"

func runAs(t *testing.T, name, script string) error {
	t.Helper()
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

// TestServerInstallOneAccount creates a real system user, so it runs only as
// root in a throwaway container: OPENNEKO_TEST_SERVER_INSTALL=1.
func TestServerInstallOneAccount(t *testing.T) {
	s := installTestServer(t)
	l := s.Layout()
	for _, dir := range s.Dirs() {
		if problem, err := dirProblem(dir, s.User); err != nil || problem != "" {
			t.Errorf("%s: %q %v", dir, problem, err)
		}
	}
	if err := runAs(t, s.User, "grep -q database_url "+s.ConfigDir+"/config.json"); err != nil {
		t.Fatal("the user cannot read its config")
	}
	for _, dir := range []string{l.Root, l.Files(), l.AgentRuns(), l.Run(), filepath.Dir(l.GraphJin()), l.Credentials(), s.SandboxDir} {
		if err := runAs(t, s.User, "touch "+dir+"/written && rm "+dir+"/written"); err != nil {
			t.Errorf("the user cannot write %s", dir)
		}
	}
	for _, path := range []string{s.ConfigDir, s.DataDir, s.SandboxDir, filepath.Join(s.ConfigDir, "config.json")} {
		if err := runAs(t, "nobody", "ls "+path+" || cat "+path); err == nil {
			t.Errorf("another user reached %s", path)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(s.TmpfilesDir, native.SandboxTmpfile)); err != nil || string(raw) != "d "+s.SandboxDir+" 0700 "+s.User+" "+s.User+" -\n" {
		t.Errorf("tmpfiles entry %q %v", raw, err)
	}
	for _, name := range []string{native.WebUnit, native.ServeUnit} {
		raw, err := os.ReadFile(filepath.Join(s.UnitDir, name))
		if err != nil || !bytes.Contains(raw, []byte("\nUser="+s.User+"\nGroup="+s.User+"\n")) {
			t.Errorf("%s: %v\n%s", name, err, raw)
		}
	}
	activeServer = &s
	t.Cleanup(func() { activeServer = nil })
	var buf bytes.Buffer
	if serverDoctorLines(&buf) {
		t.Errorf("doctor failed on a fresh install:\n%s", buf.String())
	}
}

func TestServerRestoreOwners(t *testing.T) {
	s := installTestServer(t)
	l := s.Layout()
	writeTree(t, l.Files(), map[string]string{"u1/report.pdf": "pdf"})
	writeTree(t, l.AgentRuns(), map[string]string{"run1/out/answer": "a"})
	if err := os.Chmod(filepath.Join(l.Files(), "u1"), fs.ModeSetgid|0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.ConfigDir, "planted.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ownServerData(s); err != nil {
		t.Fatal(err)
	}
	uid, gid, _ := lookupIDs(s.User)
	for _, path := range []string{filepath.Join(l.Files(), "u1", "report.pdf"), filepath.Join(s.ConfigDir, "planted.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if u, g, _ := fileOwner(info); u != uid || g != gid {
			t.Errorf("%s owned by %d:%d, want %s", path, u, g, s.User)
		}
	}
	if info, _ := os.Stat(filepath.Join(l.Files(), "u1")); info.Mode()&fs.ModeSetgid != 0 {
		t.Error("a restored folder kept the setgid bit")
	}
	if left, _ := os.ReadDir(l.AgentRuns()); len(left) != 0 {
		t.Errorf("agent-runs kept the folders of old turns: %v", left)
	}
	if info, _ := os.Stat(filepath.Join(s.ConfigDir, "planted.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("config file mode %v", info.Mode().Perm())
	}
}

// TestServerInstallStopsARunningInstall: the units stop before root walks
// the data folder and start again after it.
func TestServerInstallStopsARunningInstall(t *testing.T) {
	s := installTestServer(t)
	o := testServerOptions(t)
	o.server = s
	o.server.Old = native.OldAccounts{}
	o.systemctl = true
	journal := filepath.Join(t.TempDir(), "journal")
	fake := filepath.Join(t.TempDir(), "systemctl")
	script := "#!/bin/sh\necho \"$*\" >> " + journal + "\n[ \"$1\" = is-active ] && echo active\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	systemctlCmd = fake
	ownWalkHook = func(dir string) {
		if dir == s.DataDir {
			f, _ := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("walk\n")
			f.Close()
		}
	}
	waited := false
	serverWaitWeb = func(context.Context, io.Writer, native.Server) error { waited = true; return nil }
	t.Cleanup(func() { systemctlCmd = "systemctl"; ownWalkHook = nil; serverWaitWeb = waitServerWeb })
	cmd := newServerInstallCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runServerInstall(cmd, o); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	raw, _ := os.ReadFile(journal)
	var steps []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !strings.HasPrefix(line, "is-active") {
			steps = append(steps, line)
		}
	}
	units := native.WebUnit + " " + native.ServeUnit
	want := []string{"stop " + units, "walk", "daemon-reload", "enable " + units, "start " + units}
	if strings.Join(steps, "|") != strings.Join(want, "|") {
		t.Errorf("steps %q, want %q", steps, want)
	}
	if !waited {
		t.Error("server-install did not wait for web after it started the units again")
	}
}

func installTestServer(t *testing.T) native.Server {
	t.Helper()
	o := testServerOptions(t)
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
	return s
}

func testServerOptions(t *testing.T) serverInstallOptions {
	t.Helper()
	if os.Getenv("OPENNEKO_TEST_SERVER_INSTALL") != "1" || os.Geteuid() != 0 {
		t.Skip("needs root and OPENNEKO_TEST_SERVER_INSTALL=1")
	}
	base, err := os.MkdirTemp("/", "openneko-server-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"etc", "run", "units", "opt/bin", "opt/share/openneko/web"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return serverInstallOptions{
		server: native.Server{User: testServerUser,
			ConfigDir: filepath.Join(base, "etc", "openneko"), DataDir: filepath.Join(base, "data"),
			UnitDir: filepath.Join(base, "units"), Exe: filepath.Join(base, "opt", "bin", "openneko"),
			SandboxDir: filepath.Join(base, "run", "openneko-sandbox"), TmpfilesDir: filepath.Join(base, "tmpfiles.d")},
		databaseURL: "postgres://owner:control-secret@127.0.0.1:5432/neko?sslmode=disable",
		port:        3000, bind: "127.0.0.1",
	}
}

// TestServerUpgradeFromTwoAccounts makes a two-account install with the
// executable of the last release that made one (OPENNEKO_TEST_TWO_ACCOUNT_EXE),
// then runs this release's upgrade step, or its server-install, on it.
func TestServerUpgradeFromTwoAccounts(t *testing.T) {
	for _, how := range []string{"upgrade", "server-install"} {
		t.Run(how, func(t *testing.T) { upgradeFromTwoAccounts(t, how) })
	}
}

func upgradeFromTwoAccounts(t *testing.T, how string) {
	o := testServerOptions(t)
	oldExe := os.Getenv("OPENNEKO_TEST_TWO_ACCOUNT_EXE")
	if oldExe == "" {
		t.Skip("needs OPENNEKO_TEST_TWO_ACCOUNT_EXE, an openneko built at dbe93d5c9")
	}
	agent, group := "ozt-agent", "ozt-shared"
	s := o.server
	c := exec.Command(oldExe, "server-install", "--systemctl=false", "--control-user", s.User, "--agent-user", agent, "--group", group,
		"--config-dir", s.ConfigDir, "--data-dir", s.DataDir, "--unit-dir", s.UnitDir, "--exe", s.Exe, "--web-dir", filepath.Join(filepath.Dir(filepath.Dir(s.Exe)), "share", "openneko", "web"),
		"--sandbox-dir", s.SandboxDir, "--tmpfiles-dir", s.TmpfilesDir, "--database-url", o.databaseURL, "--bind", o.bind, "--port", strconv.Itoa(o.port))
	if raw, err := c.CombinedOutput(); err != nil {
		t.Fatalf("the two-account server-install: %v\n%s", err, raw)
	}
	l := s.Layout()
	if err := runAs(t, agent, "echo pdf > "+l.Files()+"/report.pdf && chmod 0600 "+l.Files()+"/report.pdf && echo cfg > "+filepath.Dir(l.GraphJin())+"/.ack"); err != nil {
		t.Fatal("the agent user cannot write its folders")
	}
	if err := runAs(t, s.User, "mkdir "+l.AgentRuns()+"/run1 && echo a > "+l.AgentRuns()+"/run1/answer"); err != nil {
		t.Fatal("the control user cannot stage a turn")
	}
	if err := runAs(t, s.User, "cat "+l.Files()+"/report.pdf"); err == nil {
		t.Fatal("the old install let the control user read the agent's files; it is not a two-account install")
	}

	old, err := native.LoadServer(filepath.Join(s.ConfigDir, native.ServerFile))
	if err != nil || old.Old != (native.OldAccounts{AgentUser: agent, Group: group}) {
		t.Fatalf("load: %+v %v", old.Old, err)
	}
	activeServer = &old
	var before bytes.Buffer
	if !serverDoctorLines(&before) || !strings.Contains(before.String(), "the install has two accounts") {
		t.Errorf("doctor passed a two-account install:\n%s", before.String())
	}
	activeServer = nil
	systemctlCmd = "true"
	t.Cleanup(func() { systemctlCmd = "systemctl" })
	var out bytes.Buffer
	if how == "upgrade" {
		err = refreshServer(&out, old)
	} else {
		cmd := newServerInstallCmd()
		cmd.SetOut(&out)
		err = runServerInstall(cmd, o)
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "sudo userdel "+agent+"; sudo groupdel "+group) {
		t.Errorf("no line on the old accounts:\n%s", out.String())
	}
	if _, err := user.Lookup(agent); err != nil {
		t.Errorf("the upgrade removed %s", agent)
	}
	if _, err := user.LookupGroup(group); err != nil {
		t.Errorf("the upgrade removed %s", group)
	}
	uid, gid, _ := lookupIDs(s.User)
	err = filepath.WalkDir(s.DataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if u, g, _ := fileOwner(info); u != uid || g != gid {
			t.Errorf("%s owned by %d:%d after the upgrade", p, u, g)
		}
		if info.Mode()&fs.ModeSetgid != 0 {
			t.Errorf("%s kept the setgid bit", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runAs(t, s.User, "cat "+l.Files()+"/report.pdf"); err != nil {
		t.Error("the user cannot read the files that the agent user wrote")
	}
	if left, _ := os.ReadDir(l.AgentRuns()); len(left) != 0 {
		t.Errorf("agent-runs kept the folders of old turns: %v", left)
	}
	if err := runAs(t, agent, "ls "+s.DataDir); err == nil {
		t.Error("the old agent user still reaches the data folder")
	}
	again, err := native.LoadServer(filepath.Join(s.ConfigDir, native.ServerFile))
	if err != nil || again.Old.Any() || again.User != s.User {
		t.Fatalf("server.json after the upgrade: %+v %v", again, err)
	}
	activeServer = &again
	t.Cleanup(func() { activeServer = nil })
	var doctor bytes.Buffer
	if serverDoctorLines(&doctor) {
		t.Errorf("doctor failed after the upgrade:\n%s", doctor.String())
	}
	for _, name := range []string{native.WebUnit, native.ServeUnit} {
		raw, _ := os.ReadFile(filepath.Join(s.UnitDir, name))
		if bytes.Contains(raw, []byte(agent)) || bytes.Contains(raw, []byte(group)) {
			t.Errorf("%s names an old account:\n%s", name, raw)
		}
	}
}
