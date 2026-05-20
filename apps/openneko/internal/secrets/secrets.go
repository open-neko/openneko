package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

const StoreFilename = "secrets.json"

type Store map[string]map[string]string

func Path(overrideDir string) string {
	return filepath.Join(config.Dir(overrideDir), StoreFilename)
}

func Read(overrideDir string) (Store, error) {
	file := Path(overrideDir)
	raw, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Store{}, nil
		}
		return nil, err
	}
	var parsed map[string]map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("secrets store at %s is invalid JSON: %w", file, err)
	}
	out := Store{}
	for pkg, env := range parsed {
		m := map[string]string{}
		for k, v := range env {
			if s, ok := v.(string); ok {
				m[k] = s
			}
		}
		out[pkg] = m
	}
	return out, nil
}

func ReadSoft(overrideDir string, warn func(string)) Store {
	s, err := Read(overrideDir)
	if err != nil {
		if warn != nil {
			warn(fmt.Sprintf("secrets store unreadable; treating as empty: %v", err))
		}
		return Store{}
	}
	return s
}

func Write(store Store, overrideDir string) error {
	file := Path(overrideDir)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	pkgs := make([]string, 0, len(store))
	for k := range store {
		pkgs = append(pkgs, k)
	}
	sort.Strings(pkgs)

	var buf bytes.Buffer
	buf.WriteString("{")
	for i, pkg := range pkgs {
		if i > 0 {
			buf.WriteString(",")
		}
		buf.WriteString("\n  ")
		writeQuoted(&buf, pkg)
		buf.WriteString(": {")
		inner := store[pkg]
		keys := make([]string, 0, len(inner))
		for k := range inner {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for j, k := range keys {
			if j > 0 {
				buf.WriteString(",")
			}
			buf.WriteString("\n    ")
			writeQuoted(&buf, k)
			buf.WriteString(": ")
			writeQuoted(&buf, inner[k])
		}
		if len(keys) > 0 {
			buf.WriteString("\n  ")
		}
		buf.WriteString("}")
	}
	if len(pkgs) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString("}\n")

	if err := os.WriteFile(file, buf.Bytes(), 0o600); err != nil {
		return err
	}
	_ = os.Chmod(file, 0o600)
	return nil
}

func writeQuoted(buf *bytes.Buffer, s string) {
	b, _ := json.Marshal(s)
	buf.Write(b)
}

var envKeyRX = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func IsValidEnvKey(key string) bool {
	return envKeyRX.MatchString(key)
}

func Set(store Store, plugin, key, value string) (Store, error) {
	if !IsValidEnvKey(key) {
		return nil, fmt.Errorf(`env key %q must be UPPER_SNAKE_CASE`, key)
	}
	if store == nil {
		store = Store{}
	}
	existing := store[plugin]
	next := make(map[string]string, len(existing)+1)
	maps.Copy(next, existing)
	next[key] = value
	out := Store{}
	for p, m := range store {
		if p == plugin {
			continue
		}
		out[p] = m
	}
	out[plugin] = next
	return out, nil
}

func Unset(store Store, plugin, key string) (Store, bool) {
	existing, ok := store[plugin]
	if !ok {
		return store, false
	}
	if _, ok := existing[key]; !ok {
		return store, false
	}
	next := map[string]string{}
	for k, v := range existing {
		if k == key {
			continue
		}
		next[k] = v
	}
	out := Store{}
	for p, m := range store {
		if p == plugin {
			continue
		}
		out[p] = m
	}
	if len(next) > 0 {
		out[plugin] = next
	}
	return out, true
}

func ListKeysForPlugin(store Store, plugin string) []string {
	keys := make([]string, 0, len(store[plugin]))
	for k := range store[plugin] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func AllValues(store Store) []string {
	set := map[string]struct{}{}
	for _, env := range store {
		for _, v := range env {
			if v != "" {
				set[v] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
