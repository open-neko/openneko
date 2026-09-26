// Package batchshell runs a pinned batch bundle in an OpenShell sandbox with
// no provider, broker token, or network grant. The host resolves query files.
package batchshell

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/open-neko/harness/internal/batch"
)

const RemoteWorkDir = "/sandbox/batch/work"
const RemoteCacheDir = RemoteWorkDir + "/graphjin-cache"

type Options struct {
	CLI, Gateway, Image, BundleRoot, BundleSHA256, CPU, Memory, RunID string
}

type Runner struct {
	opts                                 Options
	workDir, script, name, stage, policy string
	created                              bool
}

var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

// New snapshots the entire trusted workflow script bundle before uploading it. A pinned
// top-level script hash alone cannot protect imported vendor code.
func New(cfg batch.Config, opts Options) (*Runner, error) {
	if !filepath.IsAbs(opts.CLI) || !filepath.IsAbs(opts.BundleRoot) || !filepath.IsAbs(cfg.Script) || !sha256Hex.MatchString(opts.BundleSHA256) ||
		!filepath.IsAbs(cfg.WorkDir) || opts.Gateway == "" || opts.Image == "" || cfg.ScriptCacheDir != RemoteCacheDir || (opts.RunID != "" && !runIDPattern.MatchString(opts.RunID)) {
		return nil, fmt.Errorf("invalid isolated batch binding")
	}
	if info, err := os.Stat(opts.CLI); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("OpenShell CLI unavailable")
	}
	rel, err := filepath.Rel(opts.BundleRoot, cfg.Script)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("batch script outside pinned bundle")
	}
	stage, digest, err := snapshot(opts.BundleRoot)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(digest, opts.BundleSHA256) {
		os.RemoveAll(stage)
		return nil, fmt.Errorf("batch bundle changed")
	}
	stagedScript, err := os.ReadFile(filepath.Join(stage, "bundle", rel))
	if err != nil {
		os.RemoveAll(stage)
		return nil, fmt.Errorf("batch script missing from bundle")
	}
	scriptDigest := sha256.Sum256(stagedScript)
	if !strings.EqualFold(hex.EncodeToString(scriptDigest[:]), cfg.ScriptSHA256) {
		os.RemoveAll(stage)
		return nil, fmt.Errorf("batch script changed")
	}
	if opts.CPU == "" {
		opts.CPU = "1"
	}
	if opts.Memory == "" {
		opts.Memory = "1Gi"
	}
	policy := filepath.Join(stage, "policy.json")
	policyData, _ := json.Marshal(map[string]any{
		"version":           1,
		"filesystem_policy": map[string]any{"include_workdir": true, "read_only": []string{"/usr", "/lib", "/proc", "/dev/urandom", "/etc", "/app"}, "read_write": []string{"/sandbox", "/tmp", "/dev/null"}},
		"landlock":          map[string]string{"compatibility": "best_effort"},
		"process":           map[string]string{"run_as_user": "sandbox", "run_as_group": "sandbox"},
		"network_policies":  map[string]any{},
	})
	if err := os.WriteFile(policy, policyData, 0600); err != nil {
		os.RemoveAll(stage)
		return nil, err
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		os.RemoveAll(stage)
		return nil, err
	}
	name := "hb-" + hex.EncodeToString(id)
	if opts.RunID != "" {
		sum := sha256.Sum256([]byte(opts.RunID))
		name = "hb-" + hex.EncodeToString(sum[:8])
	}
	return &Runner{opts: opts, workDir: cfg.WorkDir,
		script: "/sandbox/batch/bundle/" + filepath.ToSlash(rel),
		name:   name, stage: stage, policy: policy}, nil
}

// Step mirrors only trusted response files into the sandbox and only request
// files plus final outputs back. Receipts remain host-owned.
func (r *Runner) Step(ctx context.Context, cfg batch.Config, output io.Writer) error {
	if cfg.WorkDir != r.workDir || cfg.ScriptCacheDir != RemoteCacheDir {
		return fmt.Errorf("batch compartment changed")
	}
	if !r.created {
		if r.opts.RunID != "" {
			if err := r.reap(ctx); err != nil {
				return err
			}
		}
		r.created = true // On ambiguous create failure Close still attempts deletion.
		create := []string{"sandbox", "create", "--name", r.name, "--from", r.opts.Image,
			"--cpu", r.opts.CPU, "--memory", r.opts.Memory, "--no-tty", "--no-auto-providers"}
		if r.opts.RunID != "" {
			create = append(create, "--label", "openneko.batch_run="+r.opts.RunID)
		}
		create = append(create,
			"--policy", r.policy, "--detach", "--", "/bin/sleep", "infinity")
		if err := r.call(ctx, nil, create...); err != nil {
			return err
		}
		if err := r.call(ctx, nil, "sandbox", "upload", r.name, filepath.Join(r.stage, "bundle"), "/sandbox/batch", "--no-git-ignore"); err != nil {
			return err
		}
		if err := r.call(ctx, nil, "sandbox", "exec", "-n", r.name, "--no-tty", "--",
			"mkdir", "-p", RemoteCacheDir+"/requests", RemoteCacheDir+"/responses"); err != nil {
			return err
		}
	}
	responses := filepath.Join(cfg.WorkDir, "graphjin-cache", "responses")
	entries, err := os.ReadDir(responses)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		if err := r.call(ctx, nil, "sandbox", "upload", r.name, responses, RemoteCacheDir, "--no-git-ignore"); err != nil {
			return err
		}
	}
	runErr := r.call(ctx, output, "sandbox", "exec", "-n", r.name, "--no-tty", "--timeout", "1200", "--",
		"env", "OPENNEKO_QUERY_CACHE_DIR="+RemoteCacheDir, "PYTHONDONTWRITEBYTECODE=1",
		"python3", r.script, "--target-day", cfg.TargetDay, "--work-dir", RemoteWorkDir,
		"--output", RemoteWorkDir+"/union_final.csv", "--summary", RemoteWorkDir+"/summary.json", "--max-runtime", "1200")
	if err := r.call(ctx, nil, "sandbox", "download", r.name, RemoteCacheDir+"/requests",
		filepath.Join(cfg.WorkDir, "graphjin-cache", "requests")); err != nil {
		return err
	}
	if runErr == nil {
		for _, file := range []string{"union_final.csv", "summary.json"} {
			if err := r.call(ctx, nil, "sandbox", "download", r.name, RemoteWorkDir+"/"+file,
				filepath.Join(cfg.WorkDir, file)); err != nil {
				return err
			}
		}
	}
	return runErr
}

// A restarted host owner removes only its own named sandbox before replaying
// saved query responses. The caller must hold the run's database ownership.
func (r *Runner) reap(ctx context.Context) error {
	var raw bytes.Buffer
	if err := r.call(ctx, &raw, "sandbox", "list", "-o", "json", "--limit", "500"); err != nil {
		return err
	}
	var boxes []struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	if json.Unmarshal(raw.Bytes(), &boxes) != nil {
		return fmt.Errorf("invalid batch sandbox inventory")
	}
	for _, box := range boxes {
		if box.Name != r.name {
			continue
		}
		if box.Labels["openneko.batch_run"] != r.opts.RunID {
			return fmt.Errorf("batch sandbox ownership mismatch")
		}
		return r.call(ctx, nil, "sandbox", "delete", r.name)
	}
	if len(boxes) >= 500 {
		return fmt.Errorf("batch sandbox inventory incomplete")
	}
	return nil
}

// Close is mandatory even after cancellation or an ambiguous create result.
func (r *Runner) Close() error {
	defer os.RemoveAll(r.stage)
	if !r.created {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := r.call(ctx, nil, "sandbox", "delete", r.name)
	if err == nil {
		r.created = false
	}
	return err
}

func (r *Runner) call(ctx context.Context, output io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.opts.CLI, append([]string{"--gateway", r.opts.Gateway}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+dir)
	}
	var buffer limitedBuffer
	if output == nil {
		output = &buffer
	}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("OpenShell %s failed: %w: %s", args[1], err, buffer.String())
	}
	return nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 8192 {
		_, _ = b.Buffer.Write(p[:min(n, 8192-b.Len())])
	}
	return n, nil
}

func snapshot(root string) (string, string, error) {
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("batch bundle unavailable")
	}
	stage, err := os.MkdirTemp("", "harness-batch-")
	if err != nil {
		return "", "", err
	}
	dest := filepath.Join(stage, "bundle")
	if err := os.Mkdir(dest, 0700); err != nil {
		os.RemoveAll(stage)
		return "", "", err
	}
	hash := sha256.New()
	count, total := 0, int64(0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			return os.Mkdir(filepath.Join(dest, rel), 0700)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return fmt.Errorf("invalid batch bundle member")
		}
		count++
		total += info.Size()
		if count > 128 || total > 32<<20 {
			return fmt.Errorf("batch bundle too large")
		}
		data, err := os.ReadFile(path)
		if err != nil || int64(len(data)) != info.Size() {
			return fmt.Errorf("batch bundle changed during snapshot")
		}
		if err := os.WriteFile(filepath.Join(dest, rel), data, 0600); err != nil {
			return err
		}
		member := sha256.Sum256(data)
		io.WriteString(hash, filepath.ToSlash(rel)+"\x00")
		hash.Write(member[:])
		return nil
	})
	if err != nil {
		os.RemoveAll(stage)
		return "", "", fmt.Errorf("batch bundle snapshot failed: %w", err)
	}
	if count == 0 {
		os.RemoveAll(stage)
		return "", "", fmt.Errorf("batch bundle empty")
	}
	return stage, hex.EncodeToString(hash.Sum(nil)), nil
}

// BundleSHA256 returns the digest to pin in a trusted launch configuration.
func BundleSHA256(root string) (string, error) {
	stage, digest, err := snapshot(root)
	if stage != "" {
		_ = os.RemoveAll(stage)
	}
	return digest, err
}
