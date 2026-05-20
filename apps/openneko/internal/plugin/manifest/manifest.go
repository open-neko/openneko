package manifest

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	Filename  = "openneko.plugins.json"
	PathEnv   = "OPENNEKO_PLUGINS_MANIFEST_PATH"
	SchemaURL = "https://open-neko.github.io/plugins/manifest.schema.json"
)

type EnvRequirement struct {
	Key         string `json:"key"`
	Required    *bool  `json:"required,omitempty"`
	Secret      *bool  `json:"secret,omitempty"`
	Description string `json:"description"`
}

type Permissions struct {
	Network []string         `json:"network"`
	Env     []EnvRequirement `json:"env"`
}

// DefaultMode is either a scalar string ("auto"|"ask"|"deny") or a per-scope
// object {external?, internal?}. Stored as raw JSON to preserve either shape.
type ActionDeclaration struct {
	Kind        string          `json:"kind"`
	Description string          `json:"description"`
	DefaultMode json.RawMessage `json:"default_mode,omitempty"`
}

type ActionCapability struct {
	Kinds []ActionDeclaration `json:"kinds"`
}

type AuthCapability struct {
	ProviderLabel string `json:"providerLabel,omitempty"`
}

type Capabilities struct {
	Action *ActionCapability `json:"action,omitempty"`
	Auth   *AuthCapability   `json:"auth,omitempty"`
}

type Entry struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Integrity    string            `json:"integrity"`
	Permissions  Permissions       `json:"permissions"`
	Capabilities Capabilities      `json:"capabilities"`
	Env          map[string]string `json:"env,omitempty"`
	Marketplace  string            `json:"marketplace,omitempty"`
}

type Manifest struct {
	Schema  string  `json:"schema"`
	Plugins []Entry `json:"plugins"`
}

func Empty() Manifest {
	return Manifest{Schema: SchemaURL, Plugins: []Entry{}}
}

func PathFor(repoRoot string) string {
	if override := os.Getenv(PathEnv); override != "" {
		return override
	}
	return filepath.Join(repoRoot, Filename)
}

// Read returns (nil, nil) when the manifest file does not exist.
func Read(repoRoot string) (*Manifest, error) {
	file := PathFor(repoRoot)
	raw, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.Schema == "" || m.Plugins == nil {
		return nil, errors.New("manifest is malformed")
	}
	return &m, nil
}

func Write(repoRoot string, m Manifest) error {
	file := PathFor(repoRoot)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(file, b, 0o644)
}

func Upsert(m Manifest, entry Entry) Manifest {
	others := make([]Entry, 0, len(m.Plugins))
	for _, p := range m.Plugins {
		if p.Name != entry.Name {
			others = append(others, p)
		}
	}
	others = append(others, entry)
	return Manifest{Schema: m.Schema, Plugins: others}
}

func RemoveByName(m Manifest, name string) Manifest {
	kept := make([]Entry, 0, len(m.Plugins))
	for _, p := range m.Plugins {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	return Manifest{Schema: m.Schema, Plugins: kept}
}
