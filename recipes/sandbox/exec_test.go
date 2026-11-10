package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandRunsInItsFolderAndReportsTheExitCode(t *testing.T) {
	dir := t.TempDir()
	result, err := runCommand(context.Background(), command{Dir: dir, Script: "pwd; echo oops >&2; exit 3", TimeoutSeconds: 10, MaxOutput: 10_000})
	resolved, _ := filepath.EvalSymlinks(dir)
	if err != nil || result.ExitCode != 3 || !strings.Contains(result.Output, "oops") || !strings.Contains(result.Output, resolved) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCommandTimeoutKillsChildProcesses(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	result, err := runCommand(context.Background(), command{Dir: dir, Script: "sleep 30 & echo $! > child.pid; wait", TimeoutSeconds: 1, MaxOutput: 10_000})
	if err != nil || !result.TimedOut || time.Since(start) > 10*time.Second {
		t.Fatalf("result=%+v err=%v elapsed=%s", result, err, time.Since(start))
	}
	data, err := os.ReadFile(filepath.Join(dir, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	time.Sleep(100 * time.Millisecond)
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("child process %d survived the timeout", pid)
	}
}

func TestCommandOutputIsCapped(t *testing.T) {
	result, err := runCommand(context.Background(), command{Dir: t.TempDir(), Script: "echo START; head -c 300000 /dev/zero | tr '\\0' 'a'; echo; echo END", TimeoutSeconds: 10, MaxOutput: 50_000})
	if err != nil || !result.Truncated || len([]rune(result.Output)) > 50_100 ||
		!strings.HasPrefix(result.Output, "START") || !strings.Contains(result.Output, "END") || !strings.Contains(result.Output, "output truncated") {
		t.Fatalf("truncated=%v length=%d err=%v", result.Truncated, len(result.Output), err)
	}
}

func TestCommandStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := runCommand(ctx, command{Dir: t.TempDir(), Script: "sleep 30", TimeoutSeconds: 60, MaxOutput: 10_000}); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("cancellation did not stop the command: err=%v elapsed=%s", err, time.Since(start))
	}
}
