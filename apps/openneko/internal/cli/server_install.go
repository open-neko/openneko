package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/installation"
	"github.com/open-neko/neko/apps/openneko/internal/native"
)

type serverInstallOptions struct {
	server      native.Server
	databaseURL string
	port        int
	bind        string
	systemctl   bool
	start       bool
}

func newServerInstallCmd() *cobra.Command {
	o := serverInstallOptions{}
	cmd := &cobra.Command{
		Use:   "server-install",
		Short: "Set up a native Linux server install with systemd (run once as root)",
		Long: `Sets up OpenNeko as two systemd services, openneko-web and openneko-serve,
that run as one system user. Each agent turn runs in a confined runner that
opens no socket and reads only the files of its turn. The serve unit cannot
see the config folder, and the web unit cannot see the agent folders.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServerInstall(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.server.User, "user", "openneko", "System user for openneko web and openneko serve")
	f.StringVar(&o.server.ConfigDir, "config-dir", "/etc/openneko", "Config folder")
	f.StringVar(&o.server.DataDir, "data-dir", "/var/lib/openneko", "Data folder")
	f.StringVar(&o.server.UnitDir, "unit-dir", "/etc/systemd/system", "Folder for the systemd units")
	f.StringVar(&o.server.Exe, "exe", "", "The openneko executable the services run (default: this one)")
	f.StringVar(&o.server.WebDir, "web-dir", "", "The web app folder (default: share/openneko/web of this release)")
	f.StringVar(&o.server.SandboxDir, "sandbox-dir", "/run/openneko-sandbox", "Socket folder for the sandbox service, apart from serve's sockets")
	f.StringVar(&o.server.TmpfilesDir, "tmpfiles-dir", "/etc/tmpfiles.d", "Folder for the tmpfiles entry that makes the sandbox socket folder at boot")
	f.StringVar(&o.server.PublicURL, "public-url", "", "The URL people open (default: http://localhost:<port>)")
	f.StringVar(&o.databaseURL, "database-url", "", "OpenNeko's Postgres database URL; the login needs CREATEROLE")
	f.IntVar(&o.port, "port", 3000, "Web port")
	f.StringVar(&o.bind, "bind", "0.0.0.0", "Web bind address")
	f.BoolVar(&o.systemctl, "systemctl", true, "Reload systemd and enable the services")
	f.BoolVar(&o.start, "start", false, "Start the services after the install")
	return cmd
}

func runServerInstall(cmd *cobra.Command, o serverInstallOptions) error {
	out := cmd.OutOrStdout()
	if runtime.GOOS != "linux" {
		return errors.New("server-install is for Linux only")
	}
	if os.Geteuid() != 0 {
		return errors.New("server-install needs root once: run it with sudo")
	}
	s, err := serverFromOptions(o)
	if err != nil {
		return err
	}
	if prev, err := native.LoadServer(filepath.Join(s.ConfigDir, native.ServerFile)); err == nil && prev.User == s.User {
		s.Old = prev.Old
	}
	if err := ensureServerAccount(out, s); err != nil {
		return err
	}
	if err := ownServerData(s); err != nil {
		return err
	}
	if o.databaseURL != "" {
		if err := config.WriteLocalDatabaseURL(s.ConfigDir, o.databaseURL); err != nil {
			return err
		}
	}
	settings := installation.Settings{Mode: "prod", WebPort: o.port, WebBindAddress: o.bind,
		Runtime: installation.RuntimeNative, WebDir: s.WebDir}
	if err := installation.Save(s.ConfigDir, settings); err != nil {
		return err
	}
	if err := native.SaveServer(s); err != nil {
		return err
	}
	if err := ownTree(s.ConfigDir, s.User); err != nil {
		return err
	}
	if err := writeServerUnits(out, s); err != nil {
		return err
	}
	if s.Old.Any() {
		fmt.Fprintf(out, "OpenNeko web and serve now run as %s. %s\n", s.User, oldAccountsLine(s))
	}
	if o.systemctl {
		steps := [][]string{{"daemon-reload"}, {"enable", native.WebUnit, native.ServeUnit}}
		if o.start {
			steps = append(steps, []string{"start", native.WebUnit, native.ServeUnit})
		}
		for _, args := range steps {
			if err := runTool(out, "systemctl", args...); err != nil {
				return err
			}
		}
	}
	local, _ := config.ReadLocal(s.ConfigDir)
	if url, _ := databaseURL(local); url == "" {
		fmt.Fprintf(out, "Give OpenNeko its database: sudo openneko server-install --database-url 'postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=verify-full'\n")
	}
	if !o.start {
		fmt.Fprintf(out, "Start OpenNeko: sudo systemctl start %s %s\n", native.WebUnit, native.ServeUnit)
		return nil
	}
	if o.systemctl {
		return waitServerWeb(cmd.Context(), out, s)
	}
	return nil
}

func serverFromOptions(o serverInstallOptions) (native.Server, error) {
	s := o.server
	if s.Exe == "" {
		exe, err := os.Executable()
		if err != nil {
			return s, err
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return s, err
		}
		s.Exe = exe
	}
	if s.WebDir == "" {
		s.WebDir = filepath.Join(filepath.Dir(filepath.Dir(s.Exe)), "share", "openneko", "web")
	}
	s.WebAddr = fmt.Sprintf("%s:%d", o.bind, o.port)
	if s.PublicURL == "" {
		s.PublicURL = fmt.Sprintf("http://localhost:%d", o.port)
	}
	return s, s.Validate()
}

func ensureServerAccount(out io.Writer, s native.Server) error {
	if _, err := user.Lookup(s.User); err == nil {
		return nil
	}
	nologin := "/usr/sbin/nologin"
	if _, err := os.Stat(nologin); err != nil {
		nologin = "/sbin/nologin"
	}
	return runTool(out, "useradd", "--system", "--user-group", "--home-dir", s.Layout().Root, "--no-create-home", "--shell", nologin, s.User)
}

func writeServerUnits(out io.Writer, s native.Server) error {
	for name, text := range s.Units() {
		if err := os.WriteFile(filepath.Join(s.UnitDir, name), []byte(text), 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Wrote %s and %s to %s.\n", native.WebUnit, native.ServeUnit, s.UnitDir)
	if text := s.SandboxTmpfileText(); text != "" && s.TmpfilesDir != "" {
		if err := os.MkdirAll(s.TmpfilesDir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(s.TmpfilesDir, native.SandboxTmpfile), []byte(text), 0o644)
	}
	return nil
}

// refreshServer gives a server install the folders and units of this
// release, so an upgrade needs no second server-install. An install made
// with two accounts moves to its first one.
func refreshServer(out io.Writer, s native.Server) error {
	if s.Old.Any() {
		if err := ownServerData(s); err != nil {
			return err
		}
	} else if err := applyServerDirs(s); err != nil {
		return err
	}
	if err := writeServerUnits(out, s); err != nil {
		return err
	}
	if err := systemctl(context.Background(), out, "daemon-reload"); err != nil {
		return err
	}
	if !s.Old.Any() {
		return nil
	}
	if err := native.SaveServer(s); err != nil {
		return err
	}
	if err := ownTree(s.ConfigDir, s.User); err != nil {
		return err
	}
	fmt.Fprintf(out, "OpenNeko web and serve now run as %s. %s\n", s.User, oldAccountsLine(s))
	return nil
}

func oldAccountsLine(s native.Server) string {
	var names, steps []string
	if s.Old.AgentUser != "" {
		names = append(names, "user "+s.Old.AgentUser)
		steps = append(steps, "sudo userdel "+s.Old.AgentUser)
	}
	if s.Old.Group != "" {
		names = append(names, "group "+s.Old.Group)
		steps = append(steps, "sudo groupdel "+s.Old.Group)
	}
	return fmt.Sprintf("OpenNeko no longer uses the %s and did not remove them. Start the sandbox again with OPENNEKO_SANDBOX_GID=$(id -g %s) (recipes/sandbox/README.md), then remove them: %s",
		strings.Join(names, " and "), s.User, strings.Join(steps, "; "))
}

func applyServerDirs(s native.Server) error {
	uid, gid, err := lookupIDs(s.User)
	if err != nil {
		return err
	}
	for _, dir := range s.Dirs() {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// ownTree gives the user the whole config folder, with no access for anyone
// else, after root wrote into it.
func ownTree(root, owner string) error {
	uid, gid, err := lookupIDs(owner)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a link; the config folder holds none", p)
		}
		mode := fs.FileMode(0o600)
		if d.IsDir() {
			mode = 0o700
		}
		if err := os.Chown(p, uid, gid); err != nil {
			return err
		}
		return os.Chmod(p, mode)
	})
}

func lookupIDs(owner string) (int, int, error) {
	u, err := user.Lookup(owner)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(u.Gid)
	return uid, gid, err
}

func runTool(out io.Writer, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout, c.Stderr = out, out
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}

func newNativePrepareCmd() *cobra.Command {
	var serverFile string
	cmd := &cobra.Command{
		Use:    "native-prepare",
		Short:  "Run the start steps of a native server install",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := native.LoadServer(serverFile)
			if err != nil {
				return err
			}
			layout := s.Layout()
			if err := os.WriteFile(layout.Seed(), assets.GraphJinSeed, 0o640); err != nil {
				return err
			}
			return runNativeSteps(cmd, layout, native.Options{Exe: s.Exe, Env: os.Environ()}, true)
		},
	}
	cmd.Flags().StringVar(&serverFile, "server", "/etc/openneko/"+native.ServerFile, "The server.json of the install")
	return cmd
}
