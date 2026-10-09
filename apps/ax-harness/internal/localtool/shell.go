package localtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
)

// Terminal limits match Hermes: 180 s default, 600 s maximum, and 50,000
// characters of combined output.
const (
	defaultShellTimeout = 180
	maxShellTimeout     = 600
	maxShellOutput      = 50_000
)

// Shell runs commands in the run workspace without the host's credentials.
type Shell struct {
	dir   string
	strip map[string]bool
}

// OpenShell checks the workspace and records the variables to remove from
// every command's environment.
func OpenShell(dir string, strip []string) (*Shell, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("workspace path must be absolute")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("workspace unavailable")
	}
	set := make(map[string]bool, len(strip))
	for _, name := range strip {
		set[name] = true
	}
	return &Shell{dir: dir, strip: set}, nil
}

func (s *Shell) Capability() agent.Capability {
	return agent.Capability{Name: "terminal", Version: "1", Origin: "workspace", Effect: "durable",
		Description: "Run a shell command with /bin/sh in the run workspace. Returns the exit code and the combined output, capped at 50,000 characters. Default timeout 180 seconds, maximum 600.",
		InputSchema: json.RawMessage(`{"type":"object","required":["command"],"properties":{"command":{"type":"string","minLength":1,"maxLength":16384},"timeout_seconds":{"type":"integer","minimum":1,"maximum":600}},"additionalProperties":false}`),
		Call:        s.run}
}

type shellResult struct {
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

func (s *Shell) run(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &input); err != nil || strings.TrimSpace(input.Command) == "" || len(input.Command) > 16384 ||
		input.TimeoutSeconds < 0 || input.TimeoutSeconds > maxShellTimeout {
		return nil, fmt.Errorf("invalid terminal command")
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = defaultShellTimeout
	}
	cmd := exec.Command("/bin/sh", "-c", input.Command)
	cmd.Dir = s.dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !s.strip[name] {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	output := &capturedOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("terminal command could not start")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(time.Duration(input.TimeoutSeconds) * time.Second)
	defer timer.Stop()
	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-timer.C:
		timedOut = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr = <-done
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return nil, ctx.Err()
	}
	// Background children keep the group alive after the shell exits.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	result := shellResult{TimedOut: timedOut}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
	case errors.As(waitErr, &exitErr):
		result.ExitCode = exitErr.ExitCode()
	default:
		result.ExitCode = -1
	}
	result.Output, result.Truncated = output.text()
	return json.Marshal(result)
}

// capturedOutput keeps the first and last halves of the output budget.
type capturedOutput struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	dropped int
}

const outputHalf = maxShellOutput / 2

func (c *capturedOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if room := outputHalf*4 - len(c.head); room > 0 {
		take := min(room, len(p))
		c.head = append(c.head, p[:take]...)
		p = p[take:]
	}
	c.tail = append(c.tail, p...)
	if extra := len(c.tail) - outputHalf*4; extra > 0 {
		c.dropped += extra
		c.tail = append([]byte(nil), c.tail[extra:]...)
	}
	return n, nil
}

// text returns at most maxShellOutput characters, head and tail around a marker.
func (c *capturedOutput) text() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	all := strings.ToValidUTF8(string(c.head)+string(c.tail), "�")
	runes := []rune(all)
	if c.dropped == 0 && len(runes) <= maxShellOutput {
		return all, false
	}
	return string(runes[:outputHalf]) + "\n[... output truncated ...]\n" + string(runes[len(runes)-outputHalf:]), true
}
