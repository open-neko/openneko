// Package processshell executes a bounded task in a separate OpenShell sandbox.
// The compartment has no model provider, broker token, or network grant. Its
// only inputs are a frozen task directory and argv; declared outputs are
// published atomically after the sandbox has exited successfully.
package processshell

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const remoteTask = "/sandbox/task"

type Options struct {
	CLI, Gateway, Image string
	// InputRoot is run-owned staging. OutputRoot must not exist; its parent is
	// host-owned. The caller must hold the run's durable dispatch lease.
	InputRoot, OutputRoot, RunID string
	OperationID                  int
	CPU, Memory                  string
	TimeoutSeconds               int
}

type Request struct {
	Argv    []string
	Outputs []string
}

type Result struct {
	Output          string
	OutputTruncated bool
	InputDigest     string
	Files           []string
}

type Runner struct {
	opts    Options
	stage   string
	policy  string
	name    string
	digest  string
	created bool
	used    bool
	closed  bool
}

func New(opts Options) (*Runner, error) {
	if !filepath.IsAbs(opts.CLI) || !filepath.IsAbs(opts.InputRoot) || !filepath.IsAbs(opts.OutputRoot) ||
		opts.Gateway == "" || opts.Image == "" || opts.RunID == "" || len(opts.RunID) > 128 || opts.OperationID < 1 || opts.OperationID > 32 ||
		opts.InputRoot == opts.OutputRoot || opts.TimeoutSeconds < 0 || opts.TimeoutSeconds > 1200 {
		return nil, fmt.Errorf("invalid process compartment binding")
	}
	if info, err := os.Stat(opts.CLI); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("OpenShell CLI unavailable")
	}
	for _, c := range opts.RunID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return nil, fmt.Errorf("invalid process run ID")
		}
	}
	if _, err := os.Lstat(opts.OutputRoot); !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("process output root already exists or cannot be checked")
	}
	parent, err := os.OpenRoot(filepath.Dir(opts.OutputRoot))
	if err != nil {
		return nil, fmt.Errorf("process output parent unavailable: %w", err)
	}
	_ = parent.Close()
	stage, digest, err := snapshot(opts.InputRoot)
	if err != nil {
		return nil, err
	}
	if opts.CPU == "" {
		opts.CPU = "1"
	}
	if opts.Memory == "" {
		opts.Memory = "512Mi"
	}
	if opts.TimeoutSeconds == 0 {
		opts.TimeoutSeconds = 120
	}
	policy := filepath.Join(stage, "policy.json")
	policyData, _ := json.Marshal(map[string]any{
		"version": 1,
		"filesystem_policy": map[string]any{
			"include_workdir": true,
			"read_only":       []string{"/usr", "/lib", "/proc", "/dev/urandom", "/etc", "/app"},
			"read_write":      []string{"/sandbox", "/tmp", "/dev/null"},
		},
		"landlock":         map[string]string{"compatibility": "best_effort"},
		"process":          map[string]string{"run_as_user": "sandbox", "run_as_group": "sandbox"},
		"network_policies": map[string]any{},
	})
	if err := os.WriteFile(policy, policyData, 0600); err != nil {
		_ = os.RemoveAll(stage)
		return nil, err
	}
	id := sha256.Sum256([]byte(opts.RunID + ":" + strconv.Itoa(opts.OperationID)))
	return &Runner{opts: opts, stage: stage, policy: policy, name: "hp-" + hex.EncodeToString(id[:8]), digest: digest}, nil
}

func validPath(name string) bool {
	return len(name) > 0 && len(name) <= 1024 && filepath.IsLocal(name) && filepath.Clean(name) == name && name != "."
}

func validate(req Request, inputStage string) error {
	if len(req.Argv) == 0 || len(req.Argv) > 64 || len(req.Outputs) == 0 || len(req.Outputs) > 16 {
		return fmt.Errorf("invalid process arguments or declared outputs")
	}
	total := 0
	for _, arg := range req.Argv {
		total += len(arg)
		if arg == "" || strings.ContainsRune(arg, 0) || total > 16<<10 {
			return fmt.Errorf("invalid process argument")
		}
	}
	seen := map[string]bool{}
	for _, name := range req.Outputs {
		if !validPath(name) || seen[name] {
			return fmt.Errorf("invalid process output path")
		}
		seen[name] = true
		if _, err := os.Lstat(filepath.Join(inputStage, "task", name)); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("process output would replace an input")
		}
	}
	return nil
}

// Run owns sandbox teardown even if creation, execution, publication or the
// caller's context fails. A deletion failure is always returned to the caller.
func (r *Runner) Run(ctx context.Context, req Request) (result Result, err error) {
	if r.used || r.closed {
		return Result{}, fmt.Errorf("process runner already used")
	}
	r.used = true
	defer func() {
		cleanupErr := r.Close()
		err = errors.Join(err, cleanupErr)
	}()
	if err := validate(req, r.stage); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := r.reap(ctx); err != nil {
		return Result{}, err
	}
	r.created = true // An ambiguous create still requires deletion.
	create := []string{"sandbox", "create", "--name", r.name, "--from", r.opts.Image,
		"--cpu", r.opts.CPU, "--memory", r.opts.Memory, "--no-tty", "--no-auto-providers",
		"--label", "openneko.process_run=" + r.opts.RunID,
		"--label", "openneko.process_operation=" + strconv.Itoa(r.opts.OperationID),
		"--policy", r.policy, "--detach", "--", "/bin/sleep", "infinity"}
	if _, _, err := r.call(ctx, 8192, create...); err != nil {
		return Result{}, err
	}
	if _, _, err := r.call(ctx, 8192, "sandbox", "upload", r.name, filepath.Join(r.stage, "task"), "/sandbox", "--no-git-ignore"); err != nil {
		return Result{}, err
	}
	args := []string{"sandbox", "exec", "-n", r.name, "--no-tty", "--workdir", remoteTask,
		"--timeout", strconv.Itoa(r.opts.TimeoutSeconds), "--", "env", "-i",
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/sandbox"}
	args = append(args, req.Argv...)
	output, truncated, err := r.call(ctx, 64<<10, args...)
	if err != nil {
		return Result{Output: output, OutputTruncated: truncated, InputDigest: r.digest}, err
	}
	// Download only predeclared outputs, not an unbounded directory. A fresh
	// staging directory on the destination filesystem makes publication atomic.
	publication, err := os.MkdirTemp(filepath.Dir(r.opts.OutputRoot), ".harness-process-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(publication)
	var totalOutput, downloaded int64
	for _, name := range req.Outputs {
		local := filepath.Join(publication, name)
		if err := os.MkdirAll(filepath.Dir(local), 0700); err != nil {
			return Result{}, err
		}
		// Refuse a symlink or oversized file before asking the gateway to
		// transfer it. Recheck the downloaded bytes before publication.
		check := `p="$1"; while :; do test ! -L "$p" || exit 1; case "$p" in */*) p="${p%/*}";; *) break;; esac; done; test -f "$1" || exit 1; stat -c%s -- "$1"`
		sizeText, _, err := r.call(ctx, 8192, "sandbox", "exec", "-n", r.name, "--no-tty", "--workdir", remoteTask,
			"--timeout", "10", "--", "sh", "-c", check, "sh", name)
		if err != nil {
			return Result{}, fmt.Errorf("process output failed size or type check: %w", err)
		}
		size, parseErr := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
		if parseErr != nil || size < 0 || size > 16<<20 || totalOutput+size > 32<<20 {
			return Result{}, fmt.Errorf("process output exceeds size limit")
		}
		totalOutput += size
		if _, _, err := r.call(ctx, 8192, "sandbox", "download", r.name, remoteTask+"/"+filepath.ToSlash(name), local); err != nil {
			return Result{}, err
		}
		info, statErr := os.Lstat(local)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
			return Result{}, fmt.Errorf("invalid process output file")
		}
		downloaded += info.Size()
		if downloaded > 32<<20 {
			return Result{}, fmt.Errorf("downloaded process outputs exceed size limit")
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// The files are still hidden in the temporary publication directory.
	// A failed teardown must not expose an artifact to Work's file route.
	if err := r.Close(); err != nil {
		return Result{}, fmt.Errorf("process sandbox teardown failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if _, statErr := os.Lstat(r.opts.OutputRoot); !errors.Is(statErr, os.ErrNotExist) {
		return Result{}, fmt.Errorf("process output root changed before publication")
	}
	if err := os.Rename(publication, r.opts.OutputRoot); err != nil {
		return Result{}, fmt.Errorf("process output publication failed: %w", err)
	}
	return Result{Output: output, OutputTruncated: truncated, InputDigest: r.digest, Files: append([]string(nil), req.Outputs...)}, nil
}

// A retry may remove only a sandbox with the exact run and operation labels.
// The caller must hold the run's durable lease before creating this runner.
func (r *Runner) reap(ctx context.Context) error {
	raw, truncated, err := r.call(ctx, 1<<20, "sandbox", "list", "-o", "json", "--limit", "500")
	if err != nil {
		return fmt.Errorf("process sandbox inventory unavailable: %w", err)
	}
	if truncated {
		return fmt.Errorf("process sandbox inventory exceeded limit")
	}
	var boxes []struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	if json.Unmarshal([]byte(raw), &boxes) != nil {
		return fmt.Errorf("invalid process sandbox inventory")
	}
	for _, box := range boxes {
		if box.Name != r.name {
			continue
		}
		if box.Labels["openneko.process_run"] != r.opts.RunID ||
			box.Labels["openneko.process_operation"] != strconv.Itoa(r.opts.OperationID) {
			return fmt.Errorf("process sandbox ownership mismatch")
		}
		_, _, err := r.call(ctx, 8192, "sandbox", "delete", r.name)
		return err
	}
	if len(boxes) >= 500 {
		return fmt.Errorf("process sandbox inventory incomplete")
	}
	return nil
}

// Close releases a runner that was constructed but never run. Run calls it
// automatically on every exit path, including cancellation.
func (r *Runner) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	defer os.RemoveAll(r.stage)
	if !r.created {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _, err := r.call(ctx, 8192, "sandbox", "delete", r.name)
	return err
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	available := max(0, b.limit-b.Len())
	if n > available {
		b.truncated = true
	}
	if available > 0 {
		_, _ = b.Buffer.Write(p[:min(n, available)])
	}
	return n, nil
}

func (r *Runner) call(ctx context.Context, limit int, args ...string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, r.opts.CLI, append([]string{"--gateway", r.opts.Gateway}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+dir)
	}
	buffer := &limitedBuffer{limit: limit}
	cmd.Stdout, cmd.Stderr = buffer, buffer
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return buffer.String(), buffer.truncated, errors.Join(ctxErr, err)
		}
		return buffer.String(), buffer.truncated, fmt.Errorf("OpenShell %s failed: %w: %s", args[1], err, buffer.String())
	}
	return buffer.String(), buffer.truncated, nil
}

func snapshot(root string) (string, string, error) {
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("process input root unavailable")
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		return "", "", err
	}
	defer opened.Close()
	stage, err := os.MkdirTemp("", "harness-process-")
	if err != nil {
		return "", "", err
	}
	dest := filepath.Join(stage, "task")
	if err := os.Mkdir(dest, 0700); err != nil {
		_ = os.RemoveAll(stage)
		return "", "", err
	}
	hash := sha256.New()
	count, total := 0, int64(0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(filepath.Join(dest, rel), 0700)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return fmt.Errorf("invalid process input member")
		}
		count++
		total += info.Size()
		if count > 128 || total > 32<<20 {
			return fmt.Errorf("process input exceeds limit")
		}
		file, err := opened.Open(rel)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 4<<20+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) != info.Size() || len(data) > 4<<20 {
			return fmt.Errorf("process input changed during snapshot")
		}
		if err := os.WriteFile(filepath.Join(dest, rel), data, 0600); err != nil {
			return err
		}
		member := sha256.Sum256(data)
		_, _ = io.WriteString(hash, filepath.ToSlash(rel)+"\x00")
		_, _ = hash.Write(member[:])
		return nil
	})
	if err != nil {
		_ = os.RemoveAll(stage)
		return "", "", fmt.Errorf("process input snapshot failed: %w", err)
	}
	return stage, hex.EncodeToString(hash.Sum(nil)), nil
}
