package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/native"
)

var (
	activeServer *native.Server
	systemctlCmd = "systemctl"
	geteuid      = os.Geteuid
)

var serverCommands = map[string]bool{
	"start": true, "stop": true, "status": true, "logs": true,
	"doctor": true, "backup": true, "restore": true, "upgrade": true,
}

func serverFilePath() string {
	if p := strings.TrimSpace(os.Getenv("OPENNEKO_SERVER_FILE")); p != "" {
		return p
	}
	return filepath.Join("/etc", "openneko", native.ServerFile)
}

func topCommand(cmd *cobra.Command) string {
	for cmd.HasParent() && cmd.Parent().HasParent() {
		cmd = cmd.Parent()
	}
	return cmd.Name()
}

// useServerInstall points the config, data and state paths of this process
// at a systemd server install, so that the native commands act on it.
func useServerInstall(cmd *cobra.Command, selected string) error {
	activeServer = nil
	name := topCommand(cmd)
	if !serverCommands[name] || runtime.GOOS != "linux" || strings.TrimSpace(selected) != "" || os.Getenv("OPENNEKO_PROXIED") == "1" {
		return nil
	}
	path := serverFilePath()
	s, err := native.LoadServer(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("this host has a server install (%s) that this user cannot read: run `sudo openneko %s`", path, name)
	case err != nil:
		return err
	}
	l := s.Layout()
	for k, v := range map[string]string{
		"XDG_CONFIG_HOME":         filepath.Dir(s.ConfigDir),
		"XDG_DATA_HOME":           l.Root,
		"XDG_STATE_HOME":          l.Root,
		"OPENNEKO_WORKSPACE_ROOT": l.Root,
	} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	activeServer = &s
	return nil
}

func requireServerRoot(command string) error {
	if activeServer != nil && geteuid() != 0 {
		return fmt.Errorf("`openneko %s` on a server install needs root: run `sudo openneko %s`", command, command)
	}
	return nil
}

func systemctl(ctx context.Context, out io.Writer, args ...string) error {
	c := exec.CommandContext(ctx, systemctlCmd, args...)
	c.Stdout, c.Stderr = out, out
	if err := c.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func unitState(ctx context.Context, unit string) string {
	raw, _ := exec.CommandContext(ctx, systemctlCmd, "is-active", unit).Output()
	if state := strings.TrimSpace(string(raw)); state != "" {
		return state
	}
	return "unknown"
}

func nativeRunning(ctx context.Context) bool {
	if activeServer != nil {
		return unitState(ctx, native.WebUnit) == "active" || unitState(ctx, native.ServeUnit) == "active"
	}
	_, ok := native.Running(nativeLayout())
	return ok
}

func nativeStop(ctx context.Context, out io.Writer) error {
	if activeServer != nil {
		return systemctl(ctx, out, "stop", native.WebUnit, native.ServeUnit)
	}
	err := native.Stop(nativeLayout(), 30*time.Second)
	if errors.Is(err, native.ErrNotRunning) {
		return nil
	}
	return err
}

func nativeStartAgain(ctx context.Context, out, errOut io.Writer, exe string) error {
	if activeServer != nil {
		return startServer(ctx, out)
	}
	return runSelf(ctx, out, errOut, exe, "start", "--native", "-d")
}

func startServer(ctx context.Context, out io.Writer) error {
	if err := requireServerRoot("start"); err != nil {
		return err
	}
	if err := systemctl(ctx, out, "start", native.WebUnit, native.ServeUnit); err != nil {
		return err
	}
	probe, err := native.ProbeHost(activeServer.WebAddr)
	if err != nil {
		return err
	}
	if err := waitNativeWeb(ctx, "http://"+probe, nativeReadyWait); err != nil {
		fmt.Fprintf(out, "OpenNeko started, but web did not answer in %s: %v\nRun `openneko status` and `sudo openneko logs web`.\n", nativeReadyWait, err)
		return WithExit(1, nil)
	}
	fmt.Fprintf(out, "OpenNeko runs at %s.\n", activeServer.PublicURL)
	return nil
}

func statusServer(ctx context.Context, out io.Writer) error {
	s := activeServer
	state := serving
	var lines []string
	inactive := 0
	for _, unit := range []string{native.ServeUnit, native.WebUnit} {
		st := unitState(ctx, unit)
		lines = append(lines, fmt.Sprintf("  ·  %s: %s", unit, st))
		if st != "active" {
			state = degraded
			inactive++
		}
	}
	checks := []struct{ name, url string }{{"serve", "http://" + native.ServeAddr + "/healthz"}}
	if probe, err := native.ProbeHost(s.WebAddr); err == nil {
		checks = append(checks, struct{ name, url string }{"web", "http://" + probe + "/api/v1/version"})
	}
	for _, c := range checks {
		if err := probeNative(ctx, c.url); err != nil {
			lines = append(lines, fmt.Sprintf("  ·  %s: not answering (%v)", c.name, err))
			state = degraded
		} else {
			lines = append(lines, fmt.Sprintf("  ·  %s: answering", c.name))
		}
	}
	if inactive == 2 {
		state = down
	}
	writeStatus(out, state, lines, "")
	if state == serving {
		return nil
	}
	return WithExit(1, nil)
}

func serverDoctorLines(out io.Writer) bool {
	s := activeServer
	failed := false
	if s.OneUser() {
		fmt.Fprintf(out, "server: systemd, one user (%s)\n", s.ControlUser)
	} else {
		fmt.Fprintf(out, "server: systemd, control zone %s, agent zone %s, group %s\n", s.ControlUser, s.AgentUser, s.Group)
	}
	for _, d := range s.Dirs() {
		problem, err := dirProblem(d)
		switch {
		case err != nil:
			fmt.Fprintf(out, "folder %s: not checked (%v)\n", d.Path, err)
		case problem != "":
			fmt.Fprintf(out, "folder %s: FAIL, %s; run server-install again\n", d.Path, problem)
			failed = true
		}
	}
	for name, want := range s.Units() {
		path := filepath.Join(s.UnitDir, name)
		raw, err := os.ReadFile(path)
		switch {
		case err != nil:
			fmt.Fprintf(out, "unit %s: FAIL, %v\n", name, err)
			failed = true
		case !bytes.Equal(raw, []byte(want)):
			fmt.Fprintf(out, "unit %s: warning, differs from what server-install writes\n", name)
		default:
			fmt.Fprintf(out, "unit %s: ok\n", name)
		}
	}
	return failed
}

func dirProblem(d native.Dir) (string, error) {
	info, err := os.Stat(d.Path)
	if err != nil {
		return "", err
	}
	uid, gid, err := lookupIDs(d.Owner, d.Group)
	if err != nil {
		return "", err
	}
	want := d.Mode.Perm() | d.Mode&fs.ModeSetgid
	got := info.Mode().Perm() | info.Mode()&fs.ModeSetgid
	var problems []string
	if fu, fg, ok := fileOwner(info); ok && (fu != uid || fg != gid) {
		problems = append(problems, fmt.Sprintf("owner %d:%d, want %s:%s", fu, fg, d.Owner, d.Group))
	}
	if got != want {
		problems = append(problems, fmt.Sprintf("mode %v, want %v", got, want))
	}
	return strings.Join(problems, ", "), nil
}

// ownServerData gives restored files the owner and group of the folder that
// holds them, and keeps the setgid bit on the folders of a shared folder.
func ownServerData(s native.Server) error {
	if err := applyServerDirs(s); err != nil {
		return err
	}
	if err := ownTree(s.ConfigDir, s.ControlUser, s.ControlUser); err != nil {
		return err
	}
	dirs := map[string]native.Dir{}
	for _, d := range s.Dirs() {
		dirs[d.Path] = d
	}
	for _, path := range s.Layout().DataFolders() {
		d, ok := dirs[path]
		if !ok {
			return fmt.Errorf("no owner is set for %s", path)
		}
		uid, gid, err := lookupIDs(d.Owner, d.Group)
		if err != nil {
			return err
		}
		err = filepath.WalkDir(path, func(p string, e fs.DirEntry, err error) error {
			if err != nil || p == path {
				return err
			}
			if err := os.Lchown(p, uid, gid); err != nil {
				return err
			}
			if e.IsDir() && d.Mode&fs.ModeSetgid != 0 {
				info, err := e.Info()
				if err != nil {
					return err
				}
				return os.Chmod(p, info.Mode().Perm()|fs.ModeSetgid)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
