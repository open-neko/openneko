// Package sandbox reaches the external sandbox service that runs the agent's
// terminal commands. recipes/sandbox is its first implementation.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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

// Client sends one run's terminal commands to the sandbox.
type Client struct {
	http      *http.Client
	base      string
	key       string
	run       string
	workspace string
}

// NewClient accepts unix:///path, http:// and https:// URLs.
func NewClient(cfg Config, workspace string) (*Client, error) {
	httpClient, base, err := transport(cfg.URL)
	if err != nil {
		return nil, err
	}
	var id [12]byte
	_, _ = rand.Read(id[:])
	return &Client{http: httpClient, base: base, key: cfg.Key, run: "run-" + hex.EncodeToString(id[:]), workspace: workspace}, nil
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

// Exec runs one command in the sandbox.
func (c *Client) Exec(ctx context.Context, command, skillDir string, env map[string]string, timeoutSeconds, maxOutput int) (localtool.ExecResult, error) {
	body, _ := json.Marshal(map[string]any{
		"workspace": c.workspace, "command": command, "skill_dir": skillDir, "env": env,
		"timeout_seconds": timeoutSeconds, "max_output": maxOutput,
	})
	resp, err := c.request(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(c.run)+"/exec", body)
	if err != nil {
		if ctx.Err() != nil {
			return localtool.ExecResult{}, ctx.Err()
		}
		return localtool.ExecResult{}, fmt.Errorf("terminal unavailable: the sandbox did not answer")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return localtool.ExecResult{}, fmt.Errorf("terminal refused the command: %s", bytes.TrimSpace(msg))
	}
	var result localtool.ExecResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&result); err != nil {
		return localtool.ExecResult{}, fmt.Errorf("terminal answered badly: %w", err)
	}
	return result, nil
}

// Close ends the run: the sandbox kills its commands and removes its home.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := c.request(ctx, http.MethodDelete, "/v1/runs/"+url.PathEscape(c.run), nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	c.http.CloseIdleConnections()
	return nil
}
