package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/installation"
	"github.com/open-neko/neko/apps/openneko/internal/instance"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

const nativeReadyWait = 90 * time.Second

var errNativeNoDatabase = errors.New(`a native install needs OpenNeko's database URL, and none is set.
Give OpenNeko a Postgres database that you run, then do one of these:
  openneko setup --database-url 'postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=verify-full'
  set OPENNEKO_DATABASE_URL_FILE to a file that holds the URL`)

func nativeLayout() native.Layout {
	return native.Layout{Root: instance.ScopeConfigDir(filepath.Join(dataHome(), "openneko"))}
}

func nativeInstall() (installation.Settings, bool) {
	settings, ok, err := loadCurrentInstallation()
	return settings, err == nil && ok && settings.Native()
}

func nativeSettings(webDir string) (installation.Settings, error) {
	settings, ok, err := loadCurrentInstallation()
	if err != nil {
		return settings, err
	}
	if ok && !settings.Native() {
		return settings, errors.New("this installation runs on Docker; `openneko start` without --native starts it")
	}
	if !ok {
		if instance.Current() != "" {
			return settings, errors.New("the native runtime runs the default installation only; leave out --instance")
		}
		port, err := strconv.Atoi(envOr("OPENNEKO_PORT", "3000"))
		if err != nil {
			return settings, fmt.Errorf("OPENNEKO_PORT: %w", err)
		}
		settings = installation.Settings{Version: installation.CurrentVersion, Mode: "prod", WebPort: port,
			WebBindAddress: envOr("OPENNEKO_WEB_BIND_ADDRESS", "0.0.0.0")}
	}
	settings.Runtime = installation.RuntimeNative
	if webDir != "" {
		abs, err := filepath.Abs(webDir)
		if err != nil {
			return settings, err
		}
		settings.WebDir = abs
	}
	if settings.WebDir == "" {
		settings.WebDir = defaultNativeWebDir()
	}
	return settings, settings.Validate()
}

// defaultNativeWebDir is the web folder of a release: share/openneko/web
// beside the bin folder that holds the executable.
func defaultNativeWebDir() string {
	if dir := strings.TrimSpace(os.Getenv("OPENNEKO_WEB_DIR")); dir != "" {
		return dir
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "openneko", "web")
}

func nativeOptions(settings installation.Settings) (native.Options, error) {
	exe, err := os.Executable()
	if err != nil {
		return native.Options{}, err
	}
	addr := fmt.Sprintf("%s:%d", settings.WebBindAddress, settings.WebPort)
	public := envOr("OPENNEKO_PUBLIC_URL", fmt.Sprintf("http://localhost:%d", settings.WebPort))
	return native.Options{Exe: exe, WebDir: settings.WebDir, WebAddr: addr, PublicURL: public, Env: os.Environ()}, nil
}

func startNative(cmd *cobra.Command, webDir, dbURL string, detach, skipMigrate bool) error {
	out := cmd.OutOrStdout()
	settings, err := nativeSettings(webDir)
	if err != nil {
		return err
	}
	if dbURL != "" {
		if err := config.WriteLocalDatabaseURL("", dbURL); err != nil {
			return err
		}
	}
	local, _ := config.ReadLocal("")
	if url, err := databaseURL(local); err != nil {
		return err
	} else if url == "" {
		return errNativeNoDatabase
	}
	layout := nativeLayout()
	if pid, ok := native.Running(layout); ok {
		fmt.Fprintf(out, "OpenNeko already runs (supervisor pid %d). `openneko status` shows it.\n", pid)
		return nil
	}
	if err := webfront.Check(settings.WebDir); err != nil {
		return fmt.Errorf("the web app: %w. Give its folder with --web-dir", err)
	}
	if err := layout.Prepare(); err != nil {
		return err
	}
	if err := os.WriteFile(layout.Seed(), assets.GraphJinSeed, 0o600); err != nil {
		return err
	}
	opts, err := nativeOptions(settings)
	if err != nil {
		return err
	}
	for _, step := range native.Steps(layout, opts, !skipMigrate) {
		fmt.Fprintf(out, "→ %s\n", step.Name)
		c := exec.CommandContext(cmd.Context(), step.Args[0], step.Args[1:]...)
		c.Env, c.Stdout, c.Stderr = step.Env, out, cmd.ErrOrStderr()
		if err := c.Run(); err != nil {
			return fmt.Errorf("%s: %w", step.Name, err)
		}
	}
	if err := installation.Save(config.Dir(""), settings); err != nil {
		return err
	}
	if !detach {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		fmt.Fprintf(out, "OpenNeko runs at %s. Logs are in %s. Press Ctrl-C to stop.\n", opts.PublicURL, layout.Logs())
		return runNativeSupervisor(ctx, out, layout, opts)
	}
	logFile, err := os.OpenFile(layout.Log("supervisor"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	child := exec.Command(opts.Exe, "native-run")
	child.Env, child.Stdout, child.Stderr = opts.Env, logFile, logFile
	child.SysProcAttr = native.Detach()
	if err := child.Start(); err != nil {
		return err
	}
	_ = child.Process.Release()
	probe, err := native.ProbeHost(opts.WebAddr)
	if err != nil {
		return err
	}
	if err := waitNativeWeb(cmd.Context(), "http://"+probe, nativeReadyWait); err != nil {
		fmt.Fprintf(out, "OpenNeko started, but web did not answer in %s: %v\nRun `openneko status` and `openneko logs web`.\n", nativeReadyWait, err)
		return WithExit(1, nil)
	}
	fmt.Fprintf(out, "OpenNeko runs at %s.\n`openneko status` checks it, `openneko logs` shows its logs and `openneko stop` stops it.\n", opts.PublicURL)
	return nil
}

func runNativeSupervisor(ctx context.Context, log io.Writer, layout native.Layout, opts native.Options) error {
	s := &native.Supervisor{Layout: layout, Procs: native.Procs(layout, opts), WebAddr: opts.WebAddr, Log: log}
	return s.Run(ctx)
}

func newNativeRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "native-run",
		Short:  "Run the native supervisor in the foreground",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			settings, ok := nativeInstall()
			if !ok {
				return errors.New("this installation is not native; run `openneko start --native` first")
			}
			opts, err := nativeOptions(settings)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runNativeSupervisor(ctx, cmd.OutOrStdout(), nativeLayout(), opts)
		},
	}
}

func waitNativeWeb(ctx context.Context, base string, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var last error
	for {
		if last = probeNative(ctx, base+"/api/v1/version"); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func probeNative(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}

func stopNative(out io.Writer) error {
	err := native.Stop(nativeLayout(), 30*time.Second)
	if errors.Is(err, native.ErrNotRunning) {
		fmt.Fprintln(out, "OpenNeko is not running.")
		return nil
	}
	if err == nil {
		fmt.Fprintln(out, "OpenNeko stopped.")
	}
	return err
}

func statusNative(ctx context.Context, out io.Writer) error {
	layout := nativeLayout()
	pid, ok := native.Running(layout)
	if !ok {
		writeStatus(out, down, []string{"  ·  supervisor: not running; `openneko start -d` starts it"}, "")
		return WithExit(1, nil)
	}
	st, err := native.ReadState(layout)
	if err != nil {
		writeStatus(out, degraded, []string{fmt.Sprintf("  ·  supervisor: pid %d; %v", pid, err)}, "")
		return WithExit(1, nil)
	}
	state, lines := classifyNative(st, time.Now())
	checks := []struct{ name, url string }{{"serve", "http://" + native.ServeAddr + "/healthz"}}
	if probe, err := native.ProbeHost(st.WebAddr); err == nil {
		checks = append(checks, struct{ name, url string }{"web", "http://" + probe + "/api/v1/version"})
	}
	for _, c := range checks {
		if err := probeNative(ctx, c.url); err != nil {
			lines = append(lines, fmt.Sprintf("  ·  %s: not answering (%v)", c.name, err))
			if state == serving {
				state = degraded
			}
		} else {
			lines = append(lines, fmt.Sprintf("  ·  %s: answering", c.name))
		}
	}
	writeStatus(out, state, lines, "")
	if state == serving {
		return nil
	}
	return WithExit(1, nil)
}

func classifyNative(st native.State, now time.Time) (health, []string) {
	state := serving
	lines := []string{fmt.Sprintf("  ·  supervisor: pid %d, up %s", st.PID, now.Sub(st.Started).Round(time.Second))}
	for _, name := range []string{"serve", "web"} {
		ps := st.Procs[name]
		switch {
		case ps == nil:
			lines = append(lines, fmt.Sprintf("  ·  %s: unknown to the supervisor", name))
			state = down
		case !native.Alive(ps.PID):
			line := fmt.Sprintf("  ·  %s: not running, %d restarts", name, ps.Restarts)
			if ps.LastExit != "" {
				line += "; last stop: " + ps.LastExit
			}
			lines = append(lines, line)
			state = down
		default:
			lines = append(lines, fmt.Sprintf("  ·  %s: pid %d, up %s, %d restarts", name, ps.PID, now.Sub(ps.Started).Round(time.Second), ps.Restarts))
		}
	}
	return state, lines
}

func logsNative(ctx context.Context, out io.Writer, services []string, follow bool, tail int) error {
	layout := nativeLayout()
	if len(services) == 0 {
		services = []string{"supervisor", "serve", "web"}
	}
	for _, s := range services {
		if s != "supervisor" && s != "serve" && s != "web" {
			return fmt.Errorf("unknown service %q; the native services are supervisor, serve and web", s)
		}
	}
	offsets := map[string]int64{}
	for _, s := range services {
		lines, end, err := lastLines(layout.Log(s), tail)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, l := range lines {
			fmt.Fprintf(out, "%-10s | %s\n", s, l)
		}
		offsets[s] = end
	}
	if !follow {
		return nil
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
		for _, s := range services {
			offsets[s] = copyNewLines(out, s, layout.Log(s), offsets[s])
		}
	}
}

func lastLines(path string, n int) ([]string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if n > 0 && len(lines) > n {
			lines = lines[1:]
		}
	}
	end, _ := f.Seek(0, io.SeekCurrent)
	return lines, end, sc.Err()
}

func copyNewLines(out io.Writer, name, path string, from int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return from
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() < from {
		from = 0
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return from
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return from
		}
		from += int64(len(line))
		fmt.Fprintf(out, "%-10s | %s", name, line)
	}
}
