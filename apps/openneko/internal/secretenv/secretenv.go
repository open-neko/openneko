// Package secretenv reads a secret setting from a value handed over at
// start, from the variable, or from the file that NAME_FILE names.
package secretenv

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
)

// StdinEnv tells a process that its secrets come on stdin as one JSON line.
const StdinEnv = "OPENNEKO_SECRETS_ON_STDIN"

var (
	mu     sync.RWMutex
	handed = map[string]string{}
)

// Get returns the handed value of name, else the variable, else the trimmed
// content of the file that name_FILE names. A missing file reads as unset,
// because Docker Compose mounts no file for an empty secret.
func Get(name string) string {
	mu.RLock()
	v, ok := handed[name]
	mu.RUnlock()
	if ok {
		return v
	}
	return Lookup(os.Environ(), name)
}

// Lookup reads name from environ, else from the file that name_FILE names.
func Lookup(environ []string, name string) string {
	if v := envValue(environ, name); v != "" {
		return v
	}
	path := strings.TrimSpace(envValue(environ, name+"_FILE"))
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "openneko: read %s_FILE (%s): %v\n", name, path, err)
		}
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func envValue(environ []string, name string) string {
	for i := len(environ) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(environ[i], "="); ok && strings.EqualFold(k, name) && (k == name || runtime.GOOS == "windows") {
			return v
		}
	}
	return ""
}

// Hand keeps values for Get. A handed name wins over its variable.
func Hand(values map[string]string) {
	mu.Lock()
	defer mu.Unlock()
	for k, v := range values {
		handed[k] = v
	}
}

// ReadHanded reads the one JSON line that the parent wrote on stdin when
// StdinEnv is 1, and hands its values over. It also sets each URL value in
// the process environment, where the readers of that URL look; the OS
// does not show a variable that a process sets after its start.
func ReadHanded(r io.Reader) error {
	if os.Getenv(StdinEnv) != "1" {
		return nil
	}
	line, err := bufio.NewReader(r).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read the secrets on stdin: %w", err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(line, &values); err != nil {
		return fmt.Errorf("read the secrets on stdin: %w", err)
	}
	clear(line)
	Hand(values)
	for k, v := range values {
		if SecretURL(v) {
			if err := os.Setenv(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// SecretName tells whether a variable can hold a secret value: a key, a
// token, a password, a login, a database URL, a webhook URL, or an OTLP
// header.
func SecretName(name string) bool {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "OTEL_") && strings.Contains(upper, "HEADERS") {
		return true
	}
	for _, part := range []string{"KEY", "TOKEN", "SECRET", "PASS", "CREDENTIAL", "DATABASE_URL", "DSN", "AUTH", "WEBHOOK_URL"} {
		if strings.Contains(upper, part) {
			return true
		}
	}
	return false
}

// Secret tells whether a variable holds a secret: by its name, or because
// its value is a URL with a login or a credential in its query.
func Secret(name, value string) bool {
	return SecretName(name) || SecretURL(value)
}

var secretQueryParts = []string{"TOKEN", "KEY", "SECRET", "SIG", "PASS", "AUTH", "CREDENTIAL"}

// SecretURL tells whether value is a URL with a login, or with a query
// parameter that can hold a credential.
func SecretURL(value string) bool {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Scheme == "" || (u.Host == "" && u.User == nil) {
		return false
	}
	if u.User != nil {
		return true
	}
	for name := range u.Query() {
		upper := strings.ToUpper(name)
		for _, part := range secretQueryParts {
			if strings.Contains(upper, part) {
				return true
			}
		}
	}
	return false
}

// Split moves the secret variables of env into a map.
func Split(env []string) ([]string, map[string]string) {
	clean := make([]string, 0, len(env))
	secrets := map[string]string{}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if Secret(name, value) {
			secrets[name] = value
			continue
		}
		clean = append(clean, kv)
	}
	return clean, secrets
}

// HandedWith returns the handed values that keep picks.
func HandedWith(keep func(name, value string) bool) map[string]string {
	mu.RLock()
	defer mu.RUnlock()
	out := map[string]string{}
	for k, v := range handed {
		if keep(k, v) {
			out[k] = v
		}
	}
	return out
}

// Line is the JSON line that ReadHanded reads.
func Line(values map[string]string) []byte {
	raw, _ := json.Marshal(values)
	return append(raw, '\n')
}
