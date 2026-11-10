package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/db"
)

const graphjinSecretPlaceholder = "REPLACE_WITH_PER_ORG_SECRET_B64"

var graphjinJWTSecretRE = regexp.MustCompile(`(?m)(^auth:\s*\n\s+type:\s*jwt\s*\n\s+jwt:\s*\n(?:\s+#.*\n)*\s+secret:\s*")([^"]*)(")`)

// Same lock key as getOrgId() in @neko/db and the AdventureWorks seed.
const orgBootstrapLock = "select pg_advisory_xact_lock(hashtext('openneko.organization.bootstrap'))"

func bootstrapOrg(ctx context.Context, admin db.ConnConfig) (id string, created bool, err error) {
	conn, err := pgx.Connect(ctx, admin.DSN())
	if err != nil {
		return "", false, err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, orgBootstrapLock); err != nil {
		return "", false, err
	}
	err = tx.QueryRow(ctx, "select id::text from organization order by created_at asc, id asc limit 1").Scan(&id)
	if err == pgx.ErrNoRows {
		id, err = newUUID()
		if err != nil {
			return "", false, err
		}
		if _, err := tx.Exec(ctx, "insert into organization (id, name) values ($1, $2)", id, "My Workspace"); err != nil {
			return "", false, err
		}
		created = true
	} else if err != nil {
		return "", false, err
	}
	return id, created, tx.Commit(ctx)
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func graphjinSigningSecretB64(configDir, orgID string) (string, error) {
	key, err := config.SigningSecret(configDir, "graphjin:"+orgID)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

type graphjinReconcile struct {
	content                     string
	system, secret, writePolicy bool
}

func (r graphjinReconcile) changed() bool { return r.system || r.secret || r.writePolicy }

func reconcileGraphjinConfig(raw, secret string) (graphjinReconcile, error) {
	r := graphjinReconcile{content: raw}
	var err error
	if r.content, r.system, err = editGraphjinYAML(r.content, migrateGraphjinSystemSource); err != nil {
		return r, err
	}
	r.content, r.secret = patchGraphjinJWTSecret(r.content, secret)
	if r.content, r.writePolicy, err = editGraphjinYAML(r.content, reconcileGraphjinWritePolicy); err != nil {
		return r, err
	}
	return r, nil
}

func reconcileGraphjinConfigFile(path, secret string) (graphjinReconcile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return graphjinReconcile{}, err
	}
	r, err := reconcileGraphjinConfig(string(raw), secret)
	if err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	if !r.changed() {
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
	if _, err := tmp.WriteString(r.content); err != nil {
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

func patchGraphjinJWTSecret(raw, secret string) (string, bool) {
	if content := strings.ReplaceAll(raw, graphjinSecretPlaceholder, secret); content != raw {
		return content, true
	}
	m := graphjinJWTSecretRE.FindStringSubmatchIndex(raw)
	if m == nil || raw[m[4]:m[5]] == secret {
		return raw, false
	}
	return raw[:m[4]] + secret + raw[m[5]:], true
}

func editGraphjinYAML(raw string, edit func(root *yaml.Node) (bool, error)) (string, bool, error) {
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

var openNekoJWTRoles = []struct{ name, comment string }{
	{"member", "OpenNeko organization member (JWT role minted by the worker)"},
	{"service", "OpenNeko worker service identity (JWT role minted by the worker)"},
}

func reconcileGraphjinWritePolicy(root *yaml.Node) (bool, error) {
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
	for _, role := range openNekoJWTRoles {
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

var graphjinSystemDefaults = []struct {
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

func migrateGraphjinSystemSource(root *yaml.Node) (bool, error) {
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
	for _, d := range graphjinSystemDefaults {
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

func prepareGraphjinAuth(ctx context.Context, out io.Writer, admin db.ConnConfig, configDir, gjConfig string) error {
	orgID, created, err := bootstrapOrg(ctx, admin)
	if err != nil {
		return fmt.Errorf("find or create the organization: %w", err)
	}
	if created {
		fmt.Fprintf(out, "created organization %s\n", orgID)
	}
	if gjConfig == "" {
		return nil
	}
	secret, err := graphjinSigningSecretB64(configDir, orgID)
	if err != nil {
		return err
	}
	r, err := reconcileGraphjinConfigFile(gjConfig, secret)
	if err != nil {
		return err
	}
	if r.system {
		fmt.Fprintf(out, "moved the old GraphJin system source to the system block in %s\n", gjConfig)
	}
	if r.secret {
		fmt.Fprintf(out, "wrote the GraphJin JWT secret for organization %s to %s\n", orgID, gjConfig)
	}
	if r.writePolicy {
		fmt.Fprintf(out, "turned on GraphJin mutations and listed the worker roles in %s\n", gjConfig)
	}
	return nil
}
