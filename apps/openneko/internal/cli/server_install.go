package cli

import (
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

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/installation"
	"github.com/open-neko/neko/apps/openneko/internal/native"
)

type serverInstallOptions struct {
	server      native.Server
	oneUser     bool
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
		Long: `Sets up OpenNeko as two systemd services. openneko-web runs as the control-zone
user, which owns the config folder: the secret key, the database login and
the provider settings. openneko-serve runs as the agent-zone user, with its
own database roles, its socket and the agent runs folder. The two users share
one group for the shared folders only. --one-user runs both services as one user.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServerInstall(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.server.ControlUser, "control-user", "openneko", "System user for openneko web (control zone)")
	f.StringVar(&o.server.AgentUser, "agent-user", "openneko-agent", "System user for openneko serve (agent zone)")
	f.StringVar(&o.server.Group, "group", "openneko-shared", "Group that the two users share for the serve socket, the agent runs and the GraphJin config")
	f.BoolVar(&o.oneUser, "one-user", false, "Run web and serve as the control-zone user, with no agent-zone user")
	f.StringVar(&o.server.ConfigDir, "config-dir", "/etc/openneko", "Config folder of the control zone")
	f.StringVar(&o.server.DataDir, "data-dir", "/var/lib/openneko", "Data folder")
	f.StringVar(&o.server.UnitDir, "unit-dir", "/etc/systemd/system", "Folder for the systemd units")
	f.StringVar(&o.server.Exe, "exe", "", "The openneko executable the services run (default: this one)")
	f.StringVar(&o.server.WebDir, "web-dir", "", "The web app folder (default: share/openneko/web of this release)")
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
	if err := ensureServerAccounts(out, s); err != nil {
		return err
	}
	if err := applyServerDirs(s); err != nil {
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
	if err := ownTree(s.ConfigDir, s.ControlUser, s.ControlUser); err != nil {
		return err
	}
	for name, text := range s.Units() {
		if err := os.WriteFile(filepath.Join(s.UnitDir, name), []byte(text), 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Wrote %s and %s to %s.\n", native.WebUnit, native.ServeUnit, s.UnitDir)
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
	}
	return nil
}

func serverFromOptions(o serverInstallOptions) (native.Server, error) {
	s := o.server
	if o.oneUser {
		s.AgentUser, s.Group = s.ControlUser, s.ControlUser
	}
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

func ensureServerAccounts(out io.Writer, s native.Server) error {
	nologin := "/usr/sbin/nologin"
	if _, err := os.Stat(nologin); err != nil {
		nologin = "/sbin/nologin"
	}
	l := s.Layout()
	if !s.OneUser() {
		if _, err := user.LookupGroup(s.Group); err != nil {
			if err := runTool(out, "groupadd", "--system", s.Group); err != nil {
				return err
			}
		}
	}
	for _, u := range []struct{ name, home string }{{s.ControlUser, l.Root}, {s.AgentUser, l.Agent}} {
		if _, err := user.Lookup(u.name); err != nil {
			args := []string{"--system", "--user-group", "--home-dir", u.home, "--no-create-home", "--shell", nologin}
			if !s.OneUser() {
				args = append(args, "--groups", s.Group)
			}
			if err := runTool(out, "useradd", append(args, u.name)...); err != nil {
				return err
			}
		} else if !s.OneUser() {
			if err := runTool(out, "usermod", "--append", "--groups", s.Group, u.name); err != nil {
				return err
			}
		}
		if s.OneUser() {
			break
		}
	}
	return nil
}

func applyServerDirs(s native.Server) error {
	for _, d := range s.Dirs() {
		if err := os.MkdirAll(d.Path, 0o700); err != nil {
			return err
		}
		uid, gid, err := lookupIDs(d.Owner, d.Group)
		if err != nil {
			return err
		}
		if err := os.Chown(d.Path, uid, gid); err != nil {
			return err
		}
		if err := os.Chmod(d.Path, d.Mode); err != nil {
			return err
		}
	}
	return nil
}

// ownTree gives the control-zone user the whole config folder, with no
// access for anyone else, after root wrote into it.
func ownTree(root, owner, group string) error {
	uid, gid, err := lookupIDs(owner, group)
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

func lookupIDs(owner, group string) (int, int, error) {
	u, err := user.Lookup(owner)
	if err != nil {
		return 0, 0, err
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(g.Gid)
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
