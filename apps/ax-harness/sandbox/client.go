// Package sandbox reaches the external sandbox service that runs the agent's
// terminal commands. recipes/sandbox is its first implementation. The
// sandbox shares no folder with OpenNeko and gets no secret.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/internal/localtool"
)

// Config names the sandbox. An empty URL turns the terminal off.
type Config struct {
	URL string
	Key string
}

func ConfigFromEnv(getenv func(string) string) Config {
	return Config{URL: strings.TrimSpace(getenv("OPENNEKO_SANDBOX_URL")), Key: getenv("OPENNEKO_SANDBOX_KEY")}
}

func (c Config) Enabled() bool { return c.URL != "" }

// Check reports a URL the client cannot use.
func (c Config) Check() error {
	if !c.Enabled() {
		return nil
	}
	_, _, err := transport(c.URL)
	return err
}

// Client sends one run's terminal commands to the sandbox. The sandbox keeps
// its own copy of the workspace: each command carries the files that changed
// here, and its answer carries the files that changed there.
type Client struct {
	http      *http.Client
	base      string
	key       string
	run       string
	workspace string

	mu      sync.Mutex
	started bool
	synced  map[string]fileState
	dirs    map[string]bool
	data    *scriptData
}

// NewClient accepts unix:///path, http:// and https:// URLs.
func NewClient(cfg Config, workspace string) (*Client, error) {
	httpClient, base, err := transport(cfg.URL)
	if err != nil {
		return nil, err
	}
	var id [12]byte
	_, _ = rand.Read(id[:])
	return &Client{http: httpClient, base: base, key: cfg.Key, run: "run-" + hex.EncodeToString(id[:]), workspace: filepath.Clean(workspace), synced: map[string]fileState{}, dirs: map[string]bool{}}, nil
}

func transport(raw string) (*http.Client, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, "", fmt.Errorf("invalid OPENNEKO_SANDBOX_URL")
	}
	switch parsed.Scheme {
	case "unix":
		if parsed.Path == "" {
			return nil, "", fmt.Errorf("OPENNEKO_SANDBOX_URL needs a socket path")
		}
		socket := parsed.Path
		dial := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}}
		return &http.Client{Transport: dial}, "http://sandbox", nil
	case "http", "https":
		if parsed.Host == "" {
			return nil, "", fmt.Errorf("OPENNEKO_SANDBOX_URL needs a host")
		}
		return &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}, strings.TrimRight(parsed.Scheme+"://"+parsed.Host+parsed.Path, "/"), nil
	}
	return nil, "", fmt.Errorf("OPENNEKO_SANDBOX_URL must start with unix://, http:// or https://")
}

func (c *Client) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	return c.http.Do(req)
}

// Exec runs one command in the sandbox. skillDir and the env values are
// paths in the workspace.
func (c *Client) Exec(ctx context.Context, command, skillDir string, env map[string]string, timeoutSeconds, maxOutput int) (localtool.ExecResult, error) {
	return c.ExecIn(ctx, "", command, skillDir, env, timeoutSeconds, maxOutput)
}

// ExecIn runs one command from dir, a folder in the workspace. An empty dir
// is the workspace root.
func (c *Client) ExecIn(ctx context.Context, dir, command, skillDir string, env map[string]string, timeoutSeconds, maxOutput int) (localtool.ExecResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body := execBody{Root: c.workspace, Command: command, TimeoutSeconds: timeoutSeconds, MaxOutput: maxOutput}
	if dir != "" {
		rel, err := c.relative(dir)
		if err != nil {
			return localtool.ExecResult{}, fmt.Errorf("terminal refused the command: the working folder is outside the workspace")
		}
		body.Dir = rel
	}
	if skillDir != "" {
		rel, err := c.relative(skillDir)
		if err != nil {
			return localtool.ExecResult{}, fmt.Errorf("terminal refused the command: the skill folder is outside the workspace")
		}
		body.SkillDir = rel
	}
	for name, value := range env {
		var parts []string
		for _, part := range filepath.SplitList(value) {
			rel, err := c.relative(part)
			if err != nil {
				return localtool.ExecResult{}, fmt.Errorf("terminal refused the command: %s points outside the workspace", name)
			}
			parts = append(parts, rel)
		}
		if body.Env == nil {
			body.Env = map[string]string{}
		}
		body.Env[name] = strings.Join(parts, ":")
	}
	if c.data != nil {
		return c.execWithData(ctx, body)
	}
	return c.execOnce(ctx, body)
}

func (c *Client) execOnce(ctx context.Context, body execBody) (localtool.ExecResult, error) {
	answer, err := c.send(ctx, body)
	if errors.Is(err, errLostRun) && c.started {
		c.started, c.synced, c.dirs = false, map[string]fileState{}, map[string]bool{}
		answer, err = c.send(ctx, body)
	}
	if err != nil {
		return localtool.ExecResult{}, err
	}
	c.started = true
	if err := c.apply(answer); err != nil {
		return localtool.ExecResult{}, fmt.Errorf("terminal output files could not be saved: %w", err)
	}
	result := answer.ExecResult
	if note := skippedNote(answer.Skipped); note != "" {
		result.Output += note
	}
	return result, nil
}

var errLostRun = errors.New("the sandbox lost the run")

func (c *Client) send(ctx context.Context, body execBody) (execAnswer, error) {
	dirs, put, remove, err := c.changes()
	if err != nil {
		return execAnswer{}, fmt.Errorf("terminal refused the command: %w", err)
	}
	body.Fresh, body.Dirs, body.Put, body.Delete = !c.started, dirs, put, remove
	raw, err := json.Marshal(body)
	if err != nil {
		return execAnswer{}, err
	}
	resp, err := c.request(ctx, http.MethodPost, "/v2/runs/"+url.PathEscape(c.run)+"/exec", raw)
	if err != nil {
		if ctx.Err() != nil {
			return execAnswer{}, ctx.Err()
		}
		return execAnswer{}, fmt.Errorf("terminal unavailable: the sandbox did not answer")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusConflict:
		return execAnswer{}, errLostRun
	case http.StatusNotFound:
		return execAnswer{}, fmt.Errorf("terminal unavailable: the sandbox is older than this release; rebuild it from recipes/sandbox")
	default:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return execAnswer{}, fmt.Errorf("terminal refused the command: %s", bytes.TrimSpace(msg))
	}
	var answer execAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&answer); err != nil {
		return execAnswer{}, fmt.Errorf("terminal answered badly: %w", err)
	}
	for _, dir := range dirs {
		c.dirs[dir] = true
	}
	for _, file := range put {
		c.synced[file.Path] = file.state
	}
	for _, name := range remove {
		delete(c.synced, name)
	}
	return answer, nil
}

// Close ends the run: the sandbox kills its commands and removes its home.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := c.request(ctx, http.MethodDelete, "/v2/runs/"+url.PathEscape(c.run), nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	c.http.CloseIdleConnections()
	return nil
}

// Probe asks the sandbox to check the key, as checkSandbox() in
// packages/llm/src/work/sandbox-service.ts does.
func Probe(ctx context.Context, cfg Config) error {
	httpClient, base, err := transport(cfg.URL)
	if err != nil {
		return err
	}
	c := &Client{http: httpClient, base: base, key: cfg.Key}
	res, err := c.request(ctx, http.MethodGet, "/v2/check", nil)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the sandbox did not answer")
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("the sandbox refused the key")
	case http.StatusNotFound:
		return fmt.Errorf("the sandbox is older than this release; rebuild it from recipes/sandbox")
	}
	return fmt.Errorf("the sandbox answered HTTP %d", res.StatusCode)
}
