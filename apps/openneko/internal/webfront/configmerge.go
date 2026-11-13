package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// This file is mergeGraphjinSourceConfigUpdate() of
// packages/llm/src/graphjin/persist-source-config.ts,
// describeGraphjinConfigChange() of config-describe.ts and
// checkGraphjinConfigCandidate() of config-file-check.ts.

const graphjinCheckDir = ".openneko-check"

var (
	diskSecretParts = []string{"password", "secret", "token", "passphrase", "private_key", "client_key",
		"api_key", "apikey", "authorization", "cookie"}
	tableListPatch = regexp.MustCompile(`^(public_tables|admin_tables|blocked_tables)_(add|remove)$`)
	tableGroups    = []string{"public_tables", "admin_tables", "blocked_tables"}
)

func isDiskSecretKey(key string) bool {
	k := strings.ReplaceAll(strings.ToLower(key), "-", "_")
	if k == "connection_string" || k == "key_value" {
		return true
	}
	for _, part := range diskSecretParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

func secretRefSegment(s string) string {
	return strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(jsTrim(s))
}

// sealForDisk swaps each secret value for a gjsecret:// reference named by
// its path, as the approval worker writes the file.
func sealForDisk(v any, path []string, parentKey string) any {
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			named := strconv.Itoa(i)
			if o, ok := item.(*jsObj); ok {
				if name, ok := o.vals["name"].(string); ok {
					named = name
				}
			}
			out[i] = sealForDisk(item, append(slices.Clone(path), secretRefSegment(named)), "")
		}
		return out
	case *jsObj:
		out := &jsObj{}
		for _, k := range t.ordered() {
			out.set(k, sealForDisk(t.vals[k], append(slices.Clone(path), secretRefSegment(k)), k))
		}
		return out
	case string:
		if isDiskSecretKey(parentKey) && jsTrim(t) != "" && !strings.HasPrefix(jsTrim(t), "gjsecret://") && !strings.Contains(t, "${") {
			parts := slices.DeleteFunc(slices.Clone(path), func(s string) bool { return s == "" })
			return "gjsecret://" + strings.Join(parts, "/")
		}
	}
	return v
}

func yamlScalar(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

// yamlStringNode prefers double quotes where a quote is needed, as the
// yaml package does.
func yamlStringNode(s string) *yaml.Node {
	n := yamlScalar("!!str", s)
	if out, err := yaml.Marshal(n); err == nil && strings.HasPrefix(string(out), "'") {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

func jsToYAMLNode(v any) *yaml.Node {
	switch t := v.(type) {
	case nil:
		return yamlScalar("!!null", "null")
	case bool:
		return yamlScalar("!!bool", strconv.FormatBool(t))
	case float64:
		if t == float64(int64(t)) {
			return yamlScalar("!!int", jsNumberText(t))
		}
		return yamlScalar("!!float", jsNumberText(t))
	case string:
		return yamlStringNode(t)
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range t {
			n.Content = append(n.Content, jsToYAMLNode(item))
		}
		return n
	case *jsObj:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range t.ordered() {
			n.Content = append(n.Content, yamlStringNode(k), jsToYAMLNode(t.vals[k]))
		}
		return n
	}
	return yamlScalar("!!null", "null")
}

func yamlMapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return yamlTarget(m.Content[i+1])
		}
	}
	return nil
}

func yamlMapSet(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, yamlStringNode(key), value)
}

func isYAMLNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

func yamlItemName(n *yaml.Node) (string, bool) {
	if n.Kind != yaml.MappingNode {
		return "", false
	}
	v := yamlMapValue(n, "name")
	if v == nil || v.Kind != yaml.ScalarNode {
		return "", false
	}
	return v.Value, true
}

func yamlSourceByName(seq *yaml.Node, name string) *yaml.Node {
	for _, item := range seq.Content {
		item = yamlTarget(item)
		if v, ok := yamlItemName(item); ok && v == name {
			return item
		}
	}
	return nil
}

func mergeYAMLMap(target *yaml.Node, patch *jsObj) {
	for _, k := range patch.ordered() {
		value := patch.vals[k]
		current := yamlMapValue(target, k)
		if o, ok := value.(*jsObj); ok && current != nil && current.Kind == yaml.MappingNode {
			mergeYAMLMap(current, o)
			continue
		}
		yamlMapSet(target, k, jsToYAMLNode(value))
	}
}

func yamlStringItems(n *yaml.Node) []string {
	out := []string{}
	if n == nil || n.Kind != yaml.SequenceNode {
		return out
	}
	for _, item := range n.Content {
		out = append(out, yamlTarget(item).Value)
	}
	return out
}

func withoutFold(values []string, name string) []string {
	return slices.DeleteFunc(slices.Clone(values), func(v string) bool {
		return strings.EqualFold(jsTrim(v), jsTrim(name))
	})
}

// applyAccessPatch mirrors GraphJin's table patch: removes first, and an
// added table leaves the other lists.
func applyAccessPatch(source *yaml.Node, access *jsObj) error {
	plain := &jsObj{}
	found := yamlMapValue(source, "access")
	accessNode := found
	if accessNode == nil || accessNode.Kind != yaml.MappingNode {
		accessNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	lists := map[string][]string{}
	for _, group := range tableGroups {
		lists[group] = yamlStringItems(yamlMapValue(accessNode, group))
	}
	keys := access.ordered()
	slices.SortStableFunc(keys, func(x, y string) int {
		return boolInt(strings.HasSuffix(y, "_remove")) - boolInt(strings.HasSuffix(x, "_remove"))
	})
	touched := false
	for _, key := range keys {
		m := tableListPatch.FindStringSubmatch(key)
		if m == nil {
			plain.set(key, access.vals[key])
			continue
		}
		names, ok := access.vals[key].([]any)
		if !ok {
			return fmt.Errorf("GraphJin access.%s must be a list", key)
		}
		touched = true
		for _, item := range names {
			name := jsTemplate(item)
			if m[2] == "remove" {
				lists[m[1]] = withoutFold(lists[m[1]], name)
				continue
			}
			for _, group := range tableGroups {
				lists[group] = withoutFold(lists[group], name)
			}
			lists[m[1]] = append(lists[m[1]], jsTrim(name))
		}
	}
	if touched {
		for _, group := range tableGroups {
			if len(lists[group]) > 0 || yamlMapValue(accessNode, group) != nil {
				plain.set(group, stringsToAny(lists[group]))
			}
		}
	}
	if found == nil || found.Kind != yaml.MappingNode {
		yamlMapSet(source, "access", accessNode)
	}
	mergeYAMLMap(accessNode, plain)
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func persistSourceName(patch *jsObj) (string, error) {
	name := trimmedText(patch.vals["name"])
	if name == "" {
		return "", errors.New("GraphJin source persistence needs a source name")
	}
	return name, nil
}

// mergeGraphjinConfigUpdate applies an approved update to the config file
// document, as the approval worker writes it.
func mergeGraphjinConfigUpdate(raw []byte, update *jsObj) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("GraphJin config is not valid YAML: %s", strings.TrimPrefix(err.Error(), "yaml: "))
	}
	if doc.Kind == 0 || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("GraphJin config root must be a YAML object")
	}
	root := doc.Content[0]
	return &doc, mergeGraphjinRoot(root, update)
}

func mergeGraphjinRoot(root *yaml.Node, update *jsObj) error {
	touches := false
	for _, key := range []string{"update_sources", "remove_sources", "source_patches"} {
		if list, ok := update.vals[key].([]any); ok && len(list) > 0 {
			touches = true
		}
	}
	sources := yamlMapValue(root, "sources")
	if isYAMLNull(sources) {
		sources = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if touches {
			yamlMapSet(root, "sources", sources)
		}
	}
	if sources.Kind != yaml.SequenceNode {
		return errors.New("GraphJin config sources must be a YAML list")
	}
	if list, ok := update.vals["update_sources"].([]any); ok {
		for _, value := range list {
			o, ok := value.(*jsObj)
			if !ok {
				return errors.New("GraphJin update_sources contains an invalid source")
			}
			if _, err := persistSourceName(o); err != nil {
				return err
			}
			name, _ := o.vals["name"].(string)
			patch := sealForDisk(o, []string{"sources", secretRefSegment(name)}, "").(*jsObj)
			name = trimmedText(patch.vals["name"])
			if existing := yamlSourceByName(sources, name); existing != nil {
				mergeYAMLMap(existing, patch)
			} else {
				sources.Style = 0
				sources.Content = append(sources.Content, jsToYAMLNode(patch))
			}
		}
	}
	if list, ok := update.vals["remove_sources"].([]any); ok {
		names := map[string]bool{}
		for i, value := range list {
			s, ok := value.(string)
			if !ok || jsTrim(s) == "" {
				return fmt.Errorf("GraphJin remove_sources[%d] must be a source name", i)
			}
			names[strings.ToLower(jsTrim(s))] = true
		}
		sources.Content = slices.DeleteFunc(sources.Content, func(item *yaml.Node) bool {
			item = yamlTarget(item)
			if item.Kind != yaml.MappingNode {
				return false
			}
			name := ""
			if v := yamlMapValue(item, "name"); v != nil {
				name = v.Value
			}
			return names[strings.ToLower(jsTrim(name))]
		})
	}
	if list, ok := update.vals["source_patches"].([]any); ok {
		for _, value := range list {
			o, ok := value.(*jsObj)
			if !ok {
				return errors.New("GraphJin source_patches contains an invalid source")
			}
			patch := &jsObj{}
			for _, k := range o.ordered() {
				if k != "access" {
					patch.set(k, o.vals[k])
				}
			}
			name, err := persistSourceName(patch)
			if err != nil {
				return err
			}
			existing := yamlSourceByName(sources, name)
			if existing == nil {
				return fmt.Errorf("GraphJin source %s is absent from the durable config", name)
			}
			mergeYAMLMap(existing, patch)
			if access, ok := o.vals["access"].(*jsObj); ok {
				if err := applyAccessPatch(existing, access); err != nil {
					return err
				}
			}
		}
	}
	return persistRoles(root, update.vals["roles"])
}

func persistRoles(root *yaml.Node, v any) error {
	roles, ok := v.([]any)
	if !ok || len(roles) == 0 {
		return nil
	}
	node := yamlMapValue(root, "roles")
	if isYAMLNull(node) {
		node = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		yamlMapSet(root, "roles", node)
	}
	if node.Kind != yaml.SequenceNode {
		return errors.New("GraphJin config roles must be a YAML list")
	}
	for _, value := range roles {
		role, ok := value.(*jsObj)
		if !ok {
			return errors.New("GraphJin roles contains an invalid role")
		}
		name := trimmedText(role.vals["name"])
		if name == "" {
			return errors.New("GraphJin role persistence needs a role name")
		}
		index := slices.IndexFunc(node.Content, func(item *yaml.Node) bool {
			item = yamlTarget(item)
			if item.Kind != yaml.MappingNode {
				return false
			}
			current := ""
			if v := yamlMapValue(item, "name"); v != nil {
				current = v.Value
			}
			return strings.EqualFold(jsTrim(current), name)
		})
		if index >= 0 {
			node.Content[index] = jsToYAMLNode(role)
		} else {
			node.Style = 0
			node.Content = append(node.Content, jsToYAMLNode(role))
		}
	}
	return nil
}

func encodeYAML(n *yaml.Node) (string, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func lowerKeys(v any) any {
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = lowerKeys(item)
		}
		return out
	case *jsObj:
		out := &jsObj{}
		for _, k := range t.ordered() {
			out.set(strings.ToLower(k), t.vals[k])
		}
		return out
	}
	return v
}

func touchedConfigNames(update *jsObj) (sources, roles map[string]bool) {
	sources, roles = map[string]bool{}, map[string]bool{}
	for _, key := range []string{"update_sources", "source_patches"} {
		list, _ := update.vals[key].([]any)
		for _, entry := range list {
			if o, ok := entry.(*jsObj); ok {
				if name, ok := o.vals["name"].(string); ok {
					sources[strings.ToLower(jsTrim(name))] = true
				}
			}
		}
	}
	removals, _ := update.vals["remove_sources"].([]any)
	for _, entry := range removals {
		if s, ok := entry.(string); ok {
			sources[strings.ToLower(jsTrim(s))] = true
		}
	}
	list, _ := update.vals["roles"].([]any)
	for _, entry := range list {
		if o, ok := entry.(*jsObj); ok {
			if name, ok := o.vals["name"].(string); ok {
				roles[strings.ToLower(jsTrim(name))] = true
			}
		}
	}
	return sources, roles
}

func namedItems(v any, wanted map[string]bool) []any {
	out := []any{}
	list, _ := v.([]any)
	for _, item := range list {
		if o, ok := item.(*jsObj); ok && wanted[strings.ToLower(cfgText(o, "name"))] {
			out = append(out, o)
		}
	}
	return out
}

func orEmptyList(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}

// describeGraphjinConfigChange gives the sources and roles that a change
// touches, before and after, as sentences and as redacted YAML. The update
// may hold a resolved password: the merge seals it and the result is
// redacted again.
func describeGraphjinConfigChange(currentSources, currentRoles any, update *jsObj) (*jsObj, error) {
	current := jsObjOf("sources", redactSourceConfig(orEmptyList(parseJSONColumn(currentSources))),
		"roles", lowerKeys(orEmptyList(parseJSONColumn(currentRoles))))
	root := jsToYAMLNode(current)
	if err := mergeGraphjinRoot(root, update); err != nil {
		return nil, err
	}
	merged, _ := yamlNodeToJS(root).(*jsObj)
	next := jsObjOf("sources", redactSourceConfig(orEmptyList(merged.vals["sources"])),
		"roles", redactSourceConfig(orEmptyList(merged.vals["roles"])))
	touchedSources, touchedRoles := touchedConfigNames(update)
	pick := func(config *jsObj) (sources, roles []any) {
		return namedItems(config.vals["sources"], touchedSources), namedItems(config.vals["roles"], touchedRoles)
	}
	yamlOf := func(sources, roles []any) (string, error) {
		if len(sources)+len(roles) == 0 {
			return "", nil
		}
		out := &jsObj{}
		if len(sources) > 0 {
			out.set("sources", sources)
		}
		if len(roles) > 0 {
			out.set("roles", roles)
		}
		return encodeYAML(jsToYAMLNode(out))
	}
	beforeSources, beforeRoles := pick(current)
	afterSources, afterRoles := pick(next)
	beforeYAML, err := yamlOf(beforeSources, beforeRoles)
	if err != nil {
		return nil, err
	}
	afterYAML, err := yamlOf(afterSources, afterRoles)
	if err != nil {
		return nil, err
	}
	return jsObjOf("before", describeGraphjinConfig(beforeSources, beforeRoles),
		"after", describeGraphjinConfig(afterSources, afterRoles),
		"yaml", jsObjOf("before", beforeYAML, "after", afterYAML)), nil
}

// checkGraphjinConfigCandidate writes the config as the approval worker
// would after this update, and asks serve to load it with the pinned
// GraphJin loader. The candidate is deleted before this returns.
func checkGraphjinConfigCandidate(ctx context.Context, configFile, socketPath string, update *jsObj) (*jsObj, error) {
	configFile, socketPath = strings.TrimSpace(configFile), strings.TrimSpace(socketPath)
	if configFile == "" {
		return jsObjOf("checked", false, "reason", "OPENNEKO_GRAPHJIN_CONFIG is not set"), nil
	}
	if socketPath == "" {
		return jsObjOf("checked", false, "reason", "OPENNEKO_CONFIG_CHECK_SOCKET is not set"), nil
	}
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	doc, err := mergeGraphjinConfigUpdate(raw, update)
	if err != nil {
		return nil, err
	}
	merged, err := encodeYAML(doc)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	root := filepath.Join(filepath.Dir(configFile), graphjinCheckDir)
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := writeNew(filepath.Join(dir, filepath.Base(configFile)), []byte(merged), 0o600); err != nil {
		return nil, err
	}
	valid, errs, err := postConfigCandidate(ctx, socketPath, id)
	if err != nil {
		return nil, err
	}
	return jsObjOf("checked", true, "valid", valid, "errors", errs), nil
}

func postConfigCandidate(ctx context.Context, socketPath, candidate string) (bool, []any, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}}
	body := jsStringify(jsObjOf("candidate", candidate), false)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://serve/v1/graphjin/config-check", strings.NewReader(body))
	if err != nil {
		return false, nil, err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, nil, errors.New("config check timed out")
		}
		return false, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, nil, fmt.Errorf("config check returned %d: %s", resp.StatusCode, cutUTF16(strings.TrimFunc(string(raw), isJSSpace), 200))
	}
	var parsed struct {
		Valid  any `json:"valid"`
		Errors any `json:"errors"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return false, nil, errors.New("config check returned no JSON")
	}
	errs := []any{}
	if list, ok := parsed.Errors.([]any); ok {
		for _, item := range list {
			errs = append(errs, jsTemplate(item))
		}
	}
	return parsed.Valid == true, errs, nil
}
