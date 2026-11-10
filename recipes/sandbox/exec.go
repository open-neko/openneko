package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type execResult struct {
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

type command struct {
	Dir            string
	Script         string
	Env            []string
	TimeoutSeconds int
	MaxOutput      int
	Credential     *syscall.Credential
}

var runPathVars = []string{"RUN_DIR", "ARTIFACT_DIR", "PYTHONPATH"}

// runCommand kills the command's process group when it ends, times out or ctx ends;
// background children would otherwise outlive the command.
func runCommand(ctx context.Context, c command) (execResult, error) {
	cmd := exec.Command("/bin/sh", "-c", c.Script)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: c.Credential}
	output := &capturedOutput{max: c.MaxOutput}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return execResult{}, fmt.Errorf("terminal command could not start: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(time.Duration(c.TimeoutSeconds) * time.Second)
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
		return execResult{}, ctx.Err()
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	result := execResult{TimedOut: timedOut}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
	case errors.As(waitErr, &exitErr):
		result.ExitCode = exitErr.ExitCode()
	default:
		result.ExitCode = -1
	}
	result.Output, result.Truncated = output.text()
	return result, nil
}

type capturedOutput struct {
	mu      sync.Mutex
	max     int
	head    []byte
	tail    []byte
	dropped int
}

func (c *capturedOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	half := c.max / 2
	if room := half*4 - len(c.head); room > 0 {
		take := min(room, len(p))
		c.head = append(c.head, p[:take]...)
		p = p[take:]
	}
	c.tail = append(c.tail, p...)
	if extra := len(c.tail) - half*4; extra > 0 {
		c.dropped += extra
		c.tail = append([]byte(nil), c.tail[extra:]...)
	}
	return n, nil
}

func (c *capturedOutput) text() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	half := c.max / 2
	runes := []rune(strings.ToValidUTF8(string(c.head)+string(c.tail), "�"))
	if c.dropped == 0 && len(runes) <= c.max {
		return string(runes), false
	}
	return string(runes[:half]) + "\n[... output truncated ...]\n" + string(runes[len(runes)-half:]), true
}
