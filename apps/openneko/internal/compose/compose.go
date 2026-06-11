// Package compose is a thin wrapper around `docker compose`. It materializes
// the embedded compose files to <cwd>/.openneko/runtime/ on first use, picks
// the right overlay per mode, and forwards I/O + signals to the child.
package compose

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

type Mode string

const (
	ModeProd Mode = "prod"
	ModeDev  Mode = "dev"
	ModeDemo Mode = "demo"
)

// Supervisor is the host-side controller; assets are the embedded files the
// supervisor materializes before running compose.
type Supervisor struct {
	// AssetsFS holds the embedded compose files. Layout:
	//   compose/core.yml
	//   compose/dev.yml
	//   compose/demo.yml
	//   compose/plugins.linux.yml
	AssetsFS fs.FS
	// RuntimeDir is where compose files are written. Defaults to
	// <cwd>/.openneko/runtime/.
	RuntimeDir string
	// GOOS lets tests stub the platform.
	GOOS string
}

func New(assets fs.FS) *Supervisor {
	return &Supervisor{
		AssetsFS: assets,
		GOOS:     runtime.GOOS,
	}
}

func (s *Supervisor) runtimeDir() (string, error) {
	if s.RuntimeDir != "" {
		return s.RuntimeDir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".openneko", "runtime"), nil
}

// Materialize writes the embedded compose files for the given mode into the
// runtime dir and returns the list of `-f` paths to pass to `docker compose`.
func (s *Supervisor) Materialize(mode Mode) ([]string, error) {
	rt, err := s.runtimeDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(rt, 0o755); err != nil {
		return nil, err
	}
	files := []string{"compose/core.yml"}
	switch mode {
	case ModeDev:
		files = append(files, "compose/dev.yml")
	case ModeDemo:
		files = append(files, "compose/demo.yml")
	case ModeProd, "":
		// core only
	default:
		return nil, fmt.Errorf("compose: unknown mode %q (want prod|dev|demo)", mode)
	}
	// SEC9: OpenShell is the only runtime — its overlay always applies.
	files = append(files, "compose/openshell.yml")
	var out []string
	for _, name := range files {
		raw, err := fs.ReadFile(s.AssetsFS, name)
		if err != nil {
			return nil, fmt.Errorf("compose: missing embedded asset %s: %w", name, err)
		}
		dst := filepath.Join(rt, filepath.Base(name))
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return nil, err
		}
		out = append(out, dst)
	}
	override := filepath.Join(config.Dir(""), "compose.override.yml")
	if _, err := os.Stat(override); err == nil {
		out = append(out, override)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return out, nil
}

// ProjectName returns the compose project name to use for the current
// invocation. On `start`, callers should pass the mode so containers/
// volumes/networks land as openneko-<mode>-*. start persists the chosen
// project name to .openneko/runtime/.project-name so stop/logs/status
// pick the same project up without needing --mode every time. Falls
// back to "openneko" when no .project-name marker exists.
func (s *Supervisor) ProjectName(modeIfStarting Mode) (string, error) {
	rt, err := s.runtimeDir()
	if err != nil {
		return "", err
	}
	marker := filepath.Join(rt, ".project-name")
	if modeIfStarting != "" {
		name := "openneko-" + string(modeIfStarting)
		_ = os.MkdirAll(rt, 0o755)
		_ = os.WriteFile(marker, []byte(name+"\n"), 0o644)
		return name, nil
	}
	if b, err := os.ReadFile(marker); err == nil {
		v := strings.TrimSpace(string(b))
		if v != "" {
			return v, nil
		}
	}
	return "openneko", nil
}

// Run shells out to `docker compose -p <project> -f <files…> <args…>`,
// forwarding I/O and signals. Returns the child's exit code. Pass
// projectName as the empty string to let docker compose default to the
// runtime dir name (not recommended).
func (s *Supervisor) Run(ctx context.Context, projectName string, files, args []string, stdout, stderr *os.File) (int, error) {
	dockerArgs := []string{"compose"}
	if projectName != "" {
		dockerArgs = append(dockerArgs, "-p", projectName)
	}
	for _, f := range files {
		dockerArgs = append(dockerArgs, "-f", f)
	}
	dockerArgs = append(dockerArgs, args...)
	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case sig := <-sigs:
			if cmd.Process != nil {
				_ = cmd.Process.Signal(sig)
			}
		case err := <-done:
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return exitErr.ExitCode(), nil
				}
				return 1, err
			}
			return 0, nil
		}
	}
}

// EnsureImage pulls image unless it's already present locally. Used to warm the
// agent sandbox image at install time so the gateway's first sandbox-create
// (i.e. the user's first chat) doesn't block on a multi-hundred-MB pull.
func (s *Supervisor) EnsureImage(ctx context.Context, image string, stdout, stderr *os.File) error {
	if exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil {
		return nil // already local
	}
	cmd := exec.CommandContext(ctx, "docker", "pull", image)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
