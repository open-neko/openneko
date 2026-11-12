// Package gjconfig holds the edits that OpenNeko makes to the customer
// GraphJin config file: the system block, the JWT secret and the write policy.
package gjconfig

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

const Placeholder = "REPLACE_WITH_PER_ORG_SECRET_B64"

var jwtSecretRE = regexp.MustCompile(`(?m)(^auth:\s*\n\s+type:\s*jwt\s*\n\s+jwt:\s*\n(?:\s+#.*\n)*\s+secret:\s*")([^"]*)(")`)

// HasJWTSecret reports whether the config holds an auth.jwt.secret line.
func HasJWTSecret(raw string) bool { return jwtSecretRE.MatchString(raw) }

func SigningSecretB64(configDir, orgID string) (string, error) {
	key, err := config.SigningSecret(configDir, "graphjin:"+orgID)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

type Result struct {
	Content                     string
	System, Secret, WritePolicy bool
}

func (r Result) Changed() bool { return r.System || r.Secret || r.WritePolicy }

func Reconcile(raw, secret string) (Result, error) {
	r := Result{Content: raw}
	var err error
	if r.Content, r.System, err = editYAML(r.Content, migrateSystemSource); err != nil {
		return r, err
	}
	r.Content, r.Secret = PatchJWTSecret(r.Content, secret)
	if r.Content, r.WritePolicy, err = editYAML(r.Content, reconcileWritePolicy); err != nil {
		return r, err
	}
	return r, nil
}

func ReconcileFile(path, secret string) (Result, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	r, err := Reconcile(string(raw), secret)
	if err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	if !r.Changed() {
		return r, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return r, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return r, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(r.Content); err != nil {
		tmp.Close()
		return r, err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return r, err
	}
	if err := tmp.Close(); err != nil {
		return r, err
	}
	return r, os.Rename(tmp.Name(), path)
}

func PatchJWTSecret(raw, secret string) (string, bool) {
	if content := strings.ReplaceAll(raw, Placeholder, secret); content != raw {
		return content, true
	}
	m := jwtSecretRE.FindStringSubmatchIndex(raw)
	if m == nil || raw[m[4]:m[5]] == secret {
		return raw, false
	}
	return raw[:m[4]] + secret + raw[m[5]:], true
}

func editYAML(raw string, edit func(root *yaml.Node) (bool, error)) (string, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return raw, false, fmt.Errorf("GraphJin config is not valid YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return raw, false, fmt.Errorf("GraphJin config root must be a YAML object")
	}
	changed, err := edit(doc.Content[0])
	if err != nil || !changed {
		return raw, false, err
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return raw, false, err
	}
	if err := enc.Close(); err != nil {
		return raw, false, err
	}
	return b.String(), true, nil
}

func yamlLookup(m *yaml.Node, key string) (keyNode, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func yamlIsNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

func yamlSet(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, yamlScalar(key), value)
}

func yamlScalar(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func yamlBool(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(b)}
}

func yamlMap() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }

func yamlMapIn(m *yaml.Node, key, where string) (*yaml.Node, bool, error) {
	_, v := yamlLookup(m, key)
	if yamlIsNull(v) {
		v = yamlMap()
		yamlSet(m, key, v)
		return v, true, nil
	}
	if v.Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("GraphJin config %s must be a YAML object", where)
	}
	return v, false, nil
}

func yamlIsTrue(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.ScalarNode || n.ShortTag() != "!!bool" {
		return false
	}
	var b bool
	return n.Decode(&b) == nil && b
}

func yamlEnsureTrue(m *yaml.Node, path []string) (bool, error) {
	changed := false
	for i, key := range path[:len(path)-1] {
		next, made, err := yamlMapIn(m, key, strings.Join(path[:i+1], "."))
		if err != nil {
			return false, err
		}
		changed, m = changed || made, next
	}
	last := path[len(path)-1]
	if _, v := yamlLookup(m, last); yamlIsTrue(v) {
		return changed, nil
	}
	yamlSet(m, last, yamlBool(true))
	return true, nil
}

var jwtRoles = []struct{ name, comment string }{
	{"member", "OpenNeko organization member (JWT role minted by the worker)"},
	{"service", "OpenNeko worker service identity (JWT role minted by the worker)"},
}

func reconcileWritePolicy(root *yaml.Node) (bool, error) {
	if k, _ := yamlLookup(root, "sources"); k == nil {
		return false, nil
	}
	changed := false
	for _, path := range [][]string{{"mcp", "allow_mutations"}, {"system", "capabilities", "raw_graphql.mutate"}} {
		made, err := yamlEnsureTrue(root, path)
		if err != nil {
			return false, err
		}
		changed = changed || made
	}

	_, roles := yamlLookup(root, "roles")
	if yamlIsNull(roles) {
		roles = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		yamlSet(root, "roles", roles)
		changed = true
	}
	if roles.Kind != yaml.SequenceNode {
		return false, fmt.Errorf("GraphJin config roles must be a YAML list")
	}
	listed := map[string]bool{}
	for _, item := range roles.Content {
		if item.Kind == yaml.MappingNode {
			if _, n := yamlLookup(item, "name"); n != nil {
				listed[strings.ToLower(strings.TrimSpace(n.Value))] = true
			}
		}
	}
	for _, role := range jwtRoles {
		if listed[role.name] {
			continue
		}
		entry := yamlMap()
		yamlSet(entry, "name", yamlScalar(role.name))
		yamlSet(entry, "comment", yamlScalar(role.comment))
		roles.Content = append(roles.Content, entry)
		changed = true
	}
	return changed, nil
}

var systemDefaults = []struct {
	section string
	values  [][2]string
}{
	{"capabilities", [][2]string{
		{"catalog.read", "true"}, {"security.read", "true"}, {"config.read", "true"},
		{"config.write", "true"}, {"runtime.read", "true"}, {"raw_graphql.query", "true"},
		{"raw_graphql.mutate", "false"}, {"schema.reload", "false"}, {"schema.write", "false"},
		{"dev_tools.read", "false"}, {"legacy_discovery.read", "false"},
	}},
	{"root_access", [][2]string{
		{"gj_catalog", "authenticated"}, {"gj_config", "admin"},
		{"gj_security", "admin"}, {"gj_runtime", "admin"},
	}},
}

func migrateSystemSource(root *yaml.Node) (bool, error) {
	_, sources := yamlLookup(root, "sources")
	if yamlIsNull(sources) {
		return false, nil
	}
	if sources.Kind != yaml.SequenceNode {
		return false, fmt.Errorf("GraphJin config sources must be a YAML list")
	}
	kept := sources.Content[:0:0]
	for _, s := range sources.Content {
		if s.Kind == yaml.MappingNode {
			if _, k := yamlLookup(s, "kind"); k != nil && strings.ToLower(strings.TrimSpace(k.Value)) == "graphjin" {
				continue
			}
		}
		kept = append(kept, s)
	}
	if len(kept) == len(sources.Content) {
		return false, nil
	}
	sources.Content = kept

	system, _, err := yamlMapIn(root, "system", "system")
	if err != nil {
		return false, err
	}
	for _, d := range systemDefaults {
		section, _, err := yamlMapIn(system, d.section, "system."+d.section)
		if err != nil {
			return false, err
		}
		for _, kv := range d.values {
			if _, v := yamlLookup(section, kv[0]); !yamlIsNull(v) {
				continue
			}
			if kv[1] == "true" || kv[1] == "false" {
				yamlSet(section, kv[0], yamlBool(kv[1] == "true"))
			} else {
				yamlSet(section, kv[0], yamlScalar(kv[1]))
			}
		}
	}
	return true, nil
}
