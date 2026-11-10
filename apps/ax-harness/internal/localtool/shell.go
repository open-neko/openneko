package localtool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
)

const (
	defaultShellTimeout = 180
	maxShellTimeout     = 600
	defaultShellOutput  = 50_000
	maxShellOutput      = 1_000_000
)

// Shell sends the agent's terminal commands to an Executor, the sandbox
// service. Commands never run in this process.
type Shell struct {
	dir       string
	skills    string
	env       []string
	executor  Executor
	timeout   int
	maxOutput int
}

type ExecResult struct {
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

// Executor runs one run's terminal commands. env carries only RunPathVars.
type Executor interface {
	Exec(ctx context.Context, command, skillDir string, env map[string]string, timeoutSeconds, maxOutput int) (ExecResult, error)
	Close() error
}

// RunPathVars name the run's folders, which skills and
// workflow scripts read. They hold paths, never secrets.
var RunPathVars = []string{"RUN_DIR", "ARTIFACT_DIR", "PYTHONPATH"}

func OpenShell(dir string, executor Executor) (*Shell, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("workspace path must be absolute")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("workspace unavailable")
	}
	if executor == nil {
		return nil, fmt.Errorf("the terminal needs a sandbox")
	}
	return &Shell{dir: dir, executor: executor, timeout: defaultShellTimeout, maxOutput: defaultShellOutput}, nil
}

func (s *Shell) Close() error {
	return s.executor.Close()
}

// SetLimits sets the default command timeout and the output cap.
func (s *Shell) SetLimits(timeoutSeconds, maxOutput int) error {
	if timeoutSeconds < 1 || timeoutSeconds > maxShellTimeout || maxOutput < 1_000 || maxOutput > maxShellOutput {
		return fmt.Errorf("invalid terminal limits")
	}
	s.timeout, s.maxOutput = timeoutSeconds, maxOutput
	return nil
}

// SetEnviron replaces the process environment as the source of RunPathVars,
// for a host that runs the harness in-process.
func (s *Shell) SetEnviron(environ []string) {
	s.env = append([]string{}, environ...)
}

func (s *Shell) environ() []string {
	if s.env != nil {
		return s.env
	}
	return os.Environ()
}

// SetSkillsRoot names the staged skills, so a command can find its skill's folder.
func (s *Shell) SetSkillsRoot(dir string) {
	s.skills = dir
}

var skillDirPath = regexp.MustCompile(`\$\{?SKILL_DIR\}?(/[^\s"'$;|&()<>]+)`)

// skillDir returns the one staged skill that holds every $SKILL_DIR path in the command.
// SKILL_DIR differs per skill, so it cannot be set once for the run.
func (s *Shell) skillDir(command string) string {
	if s.skills == "" || envValue(s.environ(), "SKILL_DIR") != "" {
		return ""
	}
	found := ""
	for _, match := range skillDirPath.FindAllStringSubmatch(command, -1) {
		hits, _ := filepath.Glob(filepath.Join(s.skills, "*", filepath.Clean(match[1])))
		dirs := map[string]bool{}
		for _, hit := range hits {
			rel, err := filepath.Rel(s.skills, hit)
			if err == nil && filepath.IsLocal(rel) {
				dirs[filepath.Join(s.skills, strings.SplitN(rel, string(filepath.Separator), 2)[0])] = true
			}
		}
		if len(dirs) != 1 {
			return ""
		}
		for dir := range dirs {
			if found != "" && found != dir {
				return ""
			}
			found = dir
		}
	}
	return found
}

func (s *Shell) Capability() agent.Capability {
	return agent.Capability{Name: "terminal", Version: "1", Origin: "workspace", Effect: "durable",
		Description: fmt.Sprintf("Run a shell command with /bin/sh in the run workspace. Returns the exit code and the combined output, capped at %d characters. Default timeout %d seconds, maximum %d.", s.maxOutput, s.timeout, maxShellTimeout),
		InputSchema: json.RawMessage(`{"type":"object","required":["command"],"properties":{"command":{"type":"string","minLength":1,"maxLength":16384},"timeout_seconds":{"type":"integer","minimum":1,"maximum":600}},"additionalProperties":false}`),
		Call:        s.run}
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
		input.TimeoutSeconds = s.timeout
	}
	result, err := s.executor.Exec(ctx, input.Command, s.skillDir(input.Command), s.runPaths(), input.TimeoutSeconds, s.maxOutput)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (s *Shell) runPaths() map[string]string {
	out := map[string]string{}
	for _, name := range RunPathVars {
		if value := envValue(s.environ(), name); value != "" {
			out[name] = value
		}
	}
	return out
}

func envValue(environ []string, name string) string {
	for _, entry := range environ {
		if key, value, ok := strings.Cut(entry, "="); ok && key == name {
			return value
		}
	}
	return ""
}
