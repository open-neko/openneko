package gjhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	SubscriptionsRole   = "neko_subscriptions"
	SubscriptionsConfig = "subscriptions.yml"
)

const subscriptionsTemplate = `app_name: OpenNeko subscriptions
production: false
log_format: plain
log_level: info
http_compress: true
reload_on_config_change: false
auth:
  type: none
auth_fail_block: false
cors_allowed_origins:
  - http://127.0.0.1
  - http://worker
  - http://web
default_limit: 100
enable_tracing: false
mcp:
  disable: true
db_schema_poll_duration: 10s
subs_poll_duration: 5s
databases:
  neko:
    type: postgres
    connection_string: %s
    dbname: %s
    pool_size: 5
    max_retries: 3
`

// RenderSubscriptionsConfig builds the subscriptions GraphJin config for a
// neko_subscriptions database URL.
func RenderSubscriptionsConfig(url string) (string, error) {
	parsed, err := pgx.ParseConfig(url)
	if err != nil {
		return "", fmt.Errorf("subscriptions database URL: %w", err)
	}
	if parsed.User != SubscriptionsRole {
		return "", fmt.Errorf("the subscriptions database URL must log in as %s, not %q", SubscriptionsRole, parsed.User)
	}
	if parsed.Database == "" {
		return "", fmt.Errorf("the subscriptions database URL names no database")
	}
	quote := func(v string) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return fmt.Sprintf(subscriptionsTemplate, quote(url), quote(parsed.Database)), nil
}

// SubscriptionsCommand runs this executable as the subscriptions child. It
// drops GJ_* and GO_ENV, which are meant for the customer GraphJin.
func SubscriptionsCommand(configFile string) func(socket string) *exec.Cmd {
	self := SelfCommand(configFile)
	return func(socket string) *exec.Cmd {
		cmd := self(socket)
		var env []string
		for _, kv := range cmd.Env {
			name, _, _ := strings.Cut(kv, "=")
			if upper := strings.ToUpper(name); !strings.HasPrefix(upper, "GJ_") && upper != "GO_ENV" {
				env = append(env, kv)
			}
		}
		cmd.Env = env
		return cmd
	}
}

// Subscriptions runs the GraphJin that serves the worker's workflow output
// subscriptions. It renders its config in Dir from the URL in URLFile and
// restarts the child when that URL changes.
type Subscriptions struct {
	URLFile string
	Dir     string
	Addr    string
	Log     io.Writer
	Poll    time.Duration
	// Command overrides the child; tests use it.
	Command func(configFile string) func(socket string) *exec.Cmd
}

func (s *Subscriptions) Run(ctx context.Context) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return err
	}
	if s.Log == nil {
		s.Log = io.Discard
	}
	poll := s.Poll
	if poll == 0 {
		poll = 2 * time.Second
	}
	file := filepath.Join(s.Dir, SubscriptionsConfig)
	_ = os.Remove(file)
	lastErr := ""
	refresh := func() {
		changed, err := s.render(file)
		if err != nil {
			if msg := err.Error(); msg != lastErr {
				lastErr = msg
				fmt.Fprintf(s.Log, "subscriptions GraphJin: %v\n", err)
			}
			return
		}
		lastErr = ""
		if changed {
			token := make([]byte, 8)
			_, _ = rand.Read(token)
			_ = writeAtomic(filepath.Join(s.Dir, RestartFile), []byte(hex.EncodeToString(token)))
		}
	}
	refresh()
	go func() {
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refresh()
			}
		}
	}()
	command := SubscriptionsCommand
	if s.Command != nil {
		command = s.Command
	}
	return (&Supervisor{ConfigFile: file, Addr: s.Addr, Log: s.Log, Poll: poll / 2, Command: command(file)}).Run(ctx)
}

func (s *Subscriptions) render(file string) (bool, error) {
	raw, err := os.ReadFile(s.URLFile)
	if err != nil {
		return false, err
	}
	conf, err := RenderSubscriptionsConfig(strings.TrimSpace(string(raw)))
	if err != nil {
		return false, err
	}
	if old, err := os.ReadFile(file); err == nil && string(old) == conf {
		return false, nil
	}
	return true, writeAtomic(file, []byte(conf))
}
