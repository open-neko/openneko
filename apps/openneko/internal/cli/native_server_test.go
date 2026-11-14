//go:build linux

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/native"
)

func testServer(t *testing.T) native.Server {
	t.Helper()
	base := t.TempDir()
	s := native.Server{ControlUser: "openneko", AgentUser: "openneko-agent", Group: "openneko-shared",
		ConfigDir: filepath.Join(base, "etc", "openneko"), DataDir: filepath.Join(base, "data"),
		UnitDir: filepath.Join(base, "units"), Exe: "/opt/openneko/bin/openneko",
		WebDir: "/opt/openneko/share/openneko/web", WebAddr: "127.0.0.1:1", PublicURL: "http://localhost:1"}
	if err := os.MkdirAll(s.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := native.SaveServer(s); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "OPENNEKO_WORKSPACE_ROOT", "OPENNEKO_PROXIED"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENNEKO_SERVER_FILE", filepath.Join(s.ConfigDir, native.ServerFile))
	t.Cleanup(func() { activeServer = nil })
	return s
}

func commandNamed(path ...string) *cobra.Command {
	cmd := &cobra.Command{Use: "openneko"}
	for _, name := range path {
		child := &cobra.Command{Use: name}
		cmd.AddCommand(child)
		cmd = child
	}
	return cmd
}

func TestUseServerInstall(t *testing.T) {
	s := testServer(t)
	if err := useServerInstall(commandNamed("web"), ""); err != nil || activeServer != nil {
		t.Fatalf("web took the server install: %v", err)
	}
	if err := useServerInstall(commandNamed("status"), "other"); err != nil || activeServer != nil {
		t.Fatalf("a named instance took the server install: %v", err)
	}
	if err := useServerInstall(commandNamed("backup", "key", "export"), ""); err != nil || activeServer == nil {
		t.Fatalf("backup key export: %v", err)
	}
	if got := config.Dir(""); got != s.ConfigDir {
		t.Errorf("config folder %s, want %s", got, s.ConfigDir)
	}
	if got, want := nativeLayout(), s.Layout(); got != want {
		t.Errorf("layout %+v, want %+v", got, want)
	}
	if got := os.Getenv("XDG_STATE_HOME"); got != s.Layout().Root {
		t.Errorf("state home %s", got)
	}

	t.Setenv("OPENNEKO_SERVER_FILE", filepath.Join(t.TempDir(), "absent.json"))
	if err := useServerInstall(commandNamed("status"), ""); err != nil || activeServer != nil {
		t.Fatalf("no server file: %v", err)
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(t.TempDir(), "locked")
		if err := os.Mkdir(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o700) })
		t.Setenv("OPENNEKO_SERVER_FILE", filepath.Join(locked, native.ServerFile))
		err := useServerInstall(commandNamed("status"), "")
		if err == nil || !strings.Contains(err.Error(), "sudo openneko status") {
			t.Fatalf("an unreadable server file: %v", err)
		}
	}
}

func TestBackupFoldersMoveBetweenLayouts(t *testing.T) {
	one := native.Layout{Root: t.TempDir()}
	writeTree(t, one.Root, map[string]string{"files/u1/a.txt": "A", "credentials/database-url": "C", "graphjin/agentic.yml": "G", "logs/web.log": "L"})
	var archive bytes.Buffer
	if err := archiveFolder(folderOf(one.DataFolders()), &archive); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	server := native.Layout{Root: filepath.Join(base, "control"), Agent: filepath.Join(base, "agent"), Shared: filepath.Join(base, "shared")}
	writeTree(t, server.Files(), map[string]string{"old.txt": "gone"})
	if err := restoreFolder(folderOf(server.DataFolders()), &archive); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(server.Files(), "u1", "a.txt"):        "A",
		filepath.Join(server.Files(), "old.txt"):            "missing",
		filepath.Join(server.Credentials(), "database-url"): "C",
		server.GraphJin(): "G",
		filepath.Join(server.Root, "logs", "web.log"): "missing",
	} {
		if got := readFileOr(path, "missing"); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func fakeSystemctl(t *testing.T, state string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nif [ \"$1\" = is-active ]; then echo " + state + "; [ " + state + " = active ]; fi\n"
	path := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := systemctlCmd
	systemctlCmd = path
	t.Cleanup(func() { systemctlCmd = old })
	return log
}

func asUser(t *testing.T, uid int) {
	old := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = old })
}

func TestServerStopAndStatus(t *testing.T) {
	s := testServer(t)
	activeServer = &s
	ctx := context.Background()
	calls := fakeSystemctl(t, "inactive")
	var out bytes.Buffer

	asUser(t, 1000)
	if err := stopNative(ctx, &out); err == nil || !strings.Contains(err.Error(), "sudo openneko stop") {
		t.Fatalf("stop as a user: %v", err)
	}
	if err := startNative(&cobra.Command{}, "", "", true, false); err == nil || !strings.Contains(err.Error(), "sudo openneko start") {
		t.Fatalf("start as a user: %v", err)
	}
	asUser(t, 0)
	if err := stopNative(ctx, &out); err != nil {
		t.Fatal(err)
	}
	if got := readFileOr(calls, ""); got != "stop openneko-web.service openneko-serve.service\n" {
		t.Errorf("systemctl calls %q", got)
	}
	if nativeRunning(ctx) {
		t.Error("inactive units count as running")
	}

	out.Reset()
	err := statusNative(ctx, &out)
	if ExitCodeFor(err) != 1 {
		t.Errorf("status exit %v", err)
	}
	for _, want := range []string{"down", "openneko-serve.service: inactive", "openneko-web.service: inactive", "web: not answering"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
	if err := logsNative(ctx, &out, []string{"supervisor"}, false, 5); err == nil || !strings.Contains(err.Error(), "serve, web") {
		t.Errorf("logs supervisor on a server: %v", err)
	}
}
