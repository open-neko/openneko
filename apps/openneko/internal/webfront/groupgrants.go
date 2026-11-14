package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

const groupRolePrefix = "og_"

func groupRoleName(slug string) string { return groupRolePrefix + slug }

func isGroupRole(name string) bool { return strings.HasPrefix(name, groupRolePrefix) }

func isNonDatabaseGrantSourceKind(kind string) bool {
	switch kind {
	case "api", "file", "code", "graphjin":
		return true
	default:
		return false
	}
}

func yamlMapDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

type groupGrantGroup struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type dataAccessRuleInput struct {
	GroupSlug   string   `json:"groupSlug"`
	GroupName   string   `json:"groupName"`
	Source      string   `json:"source"`
	TableSchema string   `json:"tableSchema"`
	TableName   string   `json:"tableName"`
	Columns     []string `json:"columns"`
	RowFilter   any      `json:"rowFilter"`
}

type groupGrantsRole struct {
	Role      string `json:"role"`
	GroupName string `json:"groupName"`
}

type groupTableGrant struct {
	Name    string
	Columns []string
	Filter  string
}

type groupGrantsModel struct {
	Roles         []groupGrantsRole
	Grants        map[string]map[string][]groupTableGrant
	APIOperations map[string][]string
}

type groupGrantsConfigResult struct {
	Content string
	Changed bool
}

type groupGrantsApplyResult struct {
	Enabled bool     `json:"enabled"`
	Changed bool     `json:"changed"`
	Roles   []string `json:"roles"`
}

type groupGrantsEnableResult struct {
	groupGrantsApplyResult
	SeededRules int `json:"seededRules"`
}

type groupGrantsDisableResult struct {
	Changed bool `json:"changed"`
}

type catalogTableColumns struct {
	Schema  string
	Table   string
	Columns []string
}

type groupGrantsDeps struct {
	ConfigFile string
	Restart    func(ctx context.Context, configFile string) error
	Catalog    func(ctx context.Context, sources []string) (map[string][]catalogTableColumns, error)
}

func toPlainFilterValue(v any) any {
	switch t := v.(type) {
	case *jsObj:
		if t == nil {
			return nil
		}
		out := make(map[string]any, len(t.vals))
		for k, val := range t.vals {
			out[k] = toPlainFilterValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = toPlainFilterValue(item)
		}
		return out
	default:
		return v
	}
}

// compileRowFilter is compileRowFilter() in packages/llm/src/graphjin/row-filter.ts.
func compileRowFilter(filter any) string {
	obj, ok := filter.(map[string]any)
	if !ok {
		return ""
	}
	if andList, ok := obj["and"].([]any); ok {
		parts := make([]string, len(andList))
		for i, item := range andList {
			parts[i] = compileRowFilter(item)
		}
		return "{ and: [" + strings.Join(parts, ", ") + "] }"
	}
	if orList, ok := obj["or"].([]any); ok {
		parts := make([]string, len(orList))
		for i, item := range orList {
			parts[i] = compileRowFilter(item)
		}
		return "{ or: [" + strings.Join(parts, ", ") + "] }"
	}
	column, _ := obj["column"].(string)
	op, _ := obj["op"].(string)
	value := obj["value"]
	var operand string
	switch v := value.(type) {
	case map[string]any:
		varName, _ := v["var"].(string)
		operand = "$" + varName
	case []any:
		items := make([]string, len(v))
		for i, item := range v {
			items[i] = jsStringify(item, false)
		}
		operand = "[" + strings.Join(items, ", ") + "]"
	default:
		operand = jsStringify(value, false)
	}
	return fmt.Sprintf("{ %s: { %s: %s } }", column, op, operand)
}

// buildGroupGrantsModel is buildGroupGrantsModel() in packages/llm/src/graphjin/group-grants.ts.
func buildGroupGrantsModel(groups []groupGrantGroup, rules []dataAccessRuleInput, apiOperationHolders map[string][]string) (groupGrantsModel, error) {
	grants := map[string]map[string][]groupTableGrant{}
	roleNames := map[string]string{}
	for _, rule := range rules {
		role := groupRoleName(rule.GroupSlug)
		roleNames[role] = rule.GroupName
		bySource := grants[rule.Source]
		if bySource == nil {
			bySource = map[string][]groupTableGrant{}
			grants[rule.Source] = bySource
		}
		name := rule.TableName
		if rule.TableSchema != "" {
			name = rule.TableSchema + "." + rule.TableName
		}
		grant := groupTableGrant{
			Name:    name,
			Columns: slices.Clone(rule.Columns),
		}
		if rule.RowFilter != nil {
			parsed, err := parseRowFilter(toPlainFilterValue(rule.RowFilter))
			if err != nil {
				return groupGrantsModel{}, err
			}
			grant.Filter = compileRowFilter(parsed)
		}
		bySource[role] = append(bySource[role], grant)
	}
	apiOperations := map[string][]string{}
	for operation, slugs := range apiOperationHolders {
		seen := map[string]bool{}
		var roles []string
		for _, slug := range slugs {
			r := groupRoleName(slug)
			if !seen[r] {
				seen[r] = true
				roles = append(roles, r)
			}
		}
		slices.Sort(roles)
		for _, role := range roles {
			if _, exists := roleNames[role]; !exists {
				groupName := role
				for _, g := range groups {
					if groupRoleName(g.Slug) == role {
						groupName = g.Name
						break
					}
				}
				roleNames[role] = groupName
			}
		}
		apiOperations[operation] = roles
	}
	roles := make([]groupGrantsRole, 0, len(roleNames))
	for role, groupName := range roleNames {
		roles = append(roles, groupGrantsRole{Role: role, GroupName: groupName})
	}
	slices.SortFunc(roles, func(a, b groupGrantsRole) int {
		return strings.Compare(a.Role, b.Role)
	})
	return groupGrantsModel{
		Roles:         roles,
		Grants:        grants,
		APIOperations: apiOperations,
	}, nil
}

func parseGroupGrantsYAML(raw string) (*yaml.Node, error) {
	var doc yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("GraphJin config is not valid YAML: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("GraphJin config root must be a YAML object")
	}
	return &doc, nil
}

func yamlMapEnsureMap(parent *yaml.Node, key string) (*yaml.Node, error) {
	node := yamlMapValue(parent, key)
	if node == nil || node.Tag == "!!null" {
		created := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlMapSet(parent, key, created)
		return created, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("GraphJin config %s must be a YAML object", key)
	}
	return node, nil
}

func yamlMapEnsureSeq(parent *yaml.Node, key string) (*yaml.Node, error) {
	node := yamlMapValue(parent, key)
	if node == nil || node.Tag == "!!null" {
		created := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		yamlMapSet(parent, key, created)
		return created, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("GraphJin config %s must be a YAML list", key)
	}
	return node, nil
}

func yamlStringSeqNode(items []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, item := range items {
		seq.Content = append(seq.Content, yamlStringNode(item))
	}
	return seq
}

func yamlScalarField(m *yaml.Node, key string) string {
	v := yamlMapValue(m, key)
	if v == nil || v.Kind != yaml.ScalarNode || v.Tag == "!!null" {
		return ""
	}
	return v.Value
}

func scopeFilterForSource(access *yaml.Node, mode string) string {
	switch mode {
	case "account":
		col := yamlScalarField(access, "namespace_column")
		if col == "" {
			col = "account_id"
		}
		return fmt.Sprintf("{ %s: { eq: $account_id } }", col)
	case "owner":
		col := yamlScalarField(access, "owner_column")
		if col == "" {
			col = "user_id"
		}
		return fmt.Sprintf("{ %s: { eq: $user_id } }", col)
	default:
		return ""
	}
}

func sameYAMLStructure(a, b string) bool {
	if a == b {
		return true
	}
	av, errA := yamlToJS([]byte(a))
	bv, errB := yamlToJS([]byte(b))
	if errA != nil || errB != nil {
		return false
	}
	return jsStringify(av, false) == jsStringify(bv, false)
}

// applyGroupGrantsToConfig is applyGroupGrantsToConfig() in packages/llm/src/graphjin/group-grants.ts.
func applyGroupGrantsToConfig(raw string, model groupGrantsModel, previousReadModes map[string]string) (groupGrantsConfigResult, error) {
	doc, err := parseGroupGrantsYAML(raw)
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	root := doc.Content[0]
	if yamlMapValue(root, "sources") == nil {
		return groupGrantsConfigResult{Content: raw, Changed: false}, nil
	}

	identity, err := yamlMapEnsureMap(root, "identity")
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	yamlMapSet(identity, "role_mode", yamlStringNode("union"))
	roleClaims, err := yamlMapEnsureSeq(identity, "role_claims")
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	for _, claim := range []string{"role", "roles"} {
		has := false
		for _, item := range roleClaims.Content {
			if item.Kind == yaml.ScalarNode && item.Value == claim {
				has = true
				break
			}
		}
		if !has {
			roleClaims.Content = append(roleClaims.Content, yamlStringNode(claim))
		}
	}
	yamlMapSet(identity, "group_claims", yamlStringSeqNode([]string{"groups"}))

	roles, err := yamlMapEnsureSeq(root, "roles")
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	var keptRoles []*yaml.Node
	for _, item := range roles.Content {
		if item.Kind == yaml.MappingNode && isGroupRole(yamlScalarField(item, "name")) {
			continue
		}
		keptRoles = append(keptRoles, item)
	}
	for _, role := range model.Roles {
		roleNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlMapSet(roleNode, "name", yamlStringNode(role.Role))
		yamlMapSet(roleNode, "comment", yamlStringNode("OpenNeko group "+role.GroupName))
		keptRoles = append(keptRoles, roleNode)
	}
	roles.Content = keptRoles
	if len(roles.Content) > 0 {
		roles.Style = 0
	}

	sources, err := yamlMapEnsureSeq(root, "sources")
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	for _, source := range sources.Content {
		if source.Kind != yaml.MappingNode {
			continue
		}
		name := yamlScalarField(source, "name")
		kind := strings.ToLower(yamlScalarField(source, "kind"))
		if kind == "" {
			kind = "database"
		}
		if !isNonDatabaseGrantSourceKind(kind) {
			access, err := yamlMapEnsureMap(source, "access")
			if err != nil {
				return groupGrantsConfigResult{}, err
			}
			access.Style = 0
			mode, hasPrev := previousReadModes[name]
			if !hasPrev {
				mode = yamlScalarField(access, "read")
			}
			scope := scopeFilterForSource(access, mode)
			yamlMapSet(access, "read", yamlStringNode("admin"))
			var keptGrants []*yaml.Node
			if existing := yamlMapValue(access, "grants"); existing != nil && existing.Kind == yaml.SequenceNode {
				for _, item := range existing.Content {
					if item.Kind == yaml.MappingNode && isGroupRole(yamlScalarField(item, "role")) {
						continue
					}
					keptGrants = append(keptGrants, item)
				}
			}
			byRole := model.Grants[name]
			roleKeys := make([]string, 0, len(byRole))
			for r := range byRole {
				roleKeys = append(roleKeys, r)
			}
			slices.Sort(roleKeys)
			var generated []*yaml.Node
			for _, role := range roleKeys {
				tables := slices.Clone(byRole[role])
				slices.SortFunc(tables, func(a, b groupTableGrant) int {
					return strings.Compare(a.Name, b.Name)
				})
				tablesSeq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
				for _, t := range tables {
					tableNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
					yamlMapSet(tableNode, "name", yamlStringNode(t.Name))
					yamlMapSet(tableNode, "columns", yamlStringSeqNode(t.Columns))
					filter := t.Filter
					if scope != "" && t.Filter != "" {
						filter = fmt.Sprintf("{ and: [%s, %s] }", scope, t.Filter)
					} else if scope != "" {
						filter = scope
					}
					if filter != "" {
						yamlMapSet(tableNode, "filter", yamlStringNode(filter))
					}
					tablesSeq.Content = append(tablesSeq.Content, tableNode)
				}
				grantNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				yamlMapSet(grantNode, "role", yamlStringNode(role))
				yamlMapSet(grantNode, "tables", tablesSeq)
				generated = append(generated, grantNode)
			}
			if len(keptGrants)+len(generated) == 0 {
				yamlMapDelete(access, "grants")
			} else {
				seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: append(keptGrants, generated...)}
				yamlMapSet(access, "grants", seq)
			}
		}

		specs := yamlMapValue(source, "specs")
		if specs == nil || specs.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(specs.Content); i += 2 {
			specName := specs.Content[i].Value
			spec := specs.Content[i+1]
			if spec.Kind != yaml.MappingNode {
				continue
			}
			operations := yamlMapValue(spec, "operations")
			if operations == nil || operations.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(operations.Content); j += 2 {
				operationName := operations.Content[j].Value
				operation := operations.Content[j+1]
				if operation.Kind != yaml.MappingNode {
					continue
				}
				current := yamlMapValue(operation, "allowed_roles")
				hasCurrentSeq := current != nil && current.Kind == yaml.SequenceNode
				var kept []string
				if hasCurrentSeq {
					for _, item := range current.Content {
						if item.Kind == yaml.ScalarNode && !isGroupRole(item.Value) {
							kept = append(kept, item.Value)
						}
					}
				}
				generated := model.APIOperations[fmt.Sprintf("%s:%s:%s", name, specName, operationName)]
				if !hasCurrentSeq && len(generated) == 0 {
					continue
				}
				yamlMapSet(operation, "allowed_roles", yamlStringSeqNode(append(kept, generated...)))
			}
		}
	}

	content, err := encodeYAML(doc)
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	if sameYAMLStructure(raw, content) {
		return groupGrantsConfigResult{Content: raw, Changed: false}, nil
	}
	return groupGrantsConfigResult{Content: content, Changed: true}, nil
}

// listConfigAPIOperations is listConfigApiOperations() in packages/llm/src/graphjin/group-grants.ts.
func listConfigAPIOperations(raw string) []string {
	doc, err := parseGroupGrantsYAML(raw)
	if err != nil {
		return []string{}
	}
	sources := yamlMapValue(doc.Content[0], "sources")
	if sources == nil || sources.Kind != yaml.SequenceNode {
		return []string{}
	}
	out := []string{}
	for _, source := range sources.Content {
		if source.Kind != yaml.MappingNode {
			continue
		}
		sourceName := yamlScalarField(source, "name")
		specs := yamlMapValue(source, "specs")
		if specs == nil || specs.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(specs.Content); i += 2 {
			specName := specs.Content[i].Value
			spec := specs.Content[i+1]
			if spec.Kind != yaml.MappingNode {
				continue
			}
			operations := yamlMapValue(spec, "operations")
			if operations == nil || operations.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(operations.Content); j += 2 {
				operationName := operations.Content[j].Value
				out = append(out, fmt.Sprintf("%s:%s:%s", sourceName, specName, operationName))
			}
		}
	}
	slices.Sort(out)
	return out
}

func listGraphjinAPIOperations(configFile string) []string {
	configFile = strings.TrimSpace(configFile)
	if configFile == "" {
		return []string{}
	}
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return []string{}
	}
	return listConfigAPIOperations(string(raw))
}

// readDatabaseSourceReadModes is readDatabaseSourceReadModes() in packages/llm/src/graphjin/group-grants.ts.
func readDatabaseSourceReadModes(raw string) map[string]string {
	modes := map[string]string{}
	doc, err := parseGroupGrantsYAML(raw)
	if err != nil {
		return modes
	}
	sources := yamlMapValue(doc.Content[0], "sources")
	if sources == nil || sources.Kind != yaml.SequenceNode {
		return modes
	}
	for _, source := range sources.Content {
		if source.Kind != yaml.MappingNode {
			continue
		}
		kind := strings.ToLower(yamlScalarField(source, "kind"))
		if kind == "" {
			kind = "database"
		}
		if isNonDatabaseGrantSourceKind(kind) {
			continue
		}
		name := yamlScalarField(source, "name")
		read := ""
		if access := yamlMapValue(source, "access"); access != nil && access.Kind == yaml.MappingNode {
			read = yamlScalarField(access, "read")
		}
		modes[name] = read
	}
	return modes
}

// removeGroupGrantsFromConfig is removeGroupGrantsFromConfig() in packages/llm/src/graphjin/group-grants.ts.
func removeGroupGrantsFromConfig(raw string, previousReadModes map[string]string) (groupGrantsConfigResult, error) {
	cleared, err := applyGroupGrantsToConfig(raw, groupGrantsModel{
		Roles:         []groupGrantsRole{},
		Grants:        map[string]map[string][]groupTableGrant{},
		APIOperations: map[string][]string{},
	}, previousReadModes)
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	doc, err := parseGroupGrantsYAML(cleared.Content)
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	root := doc.Content[0]
	if yamlMapValue(root, "sources") == nil {
		return groupGrantsConfigResult{Content: raw, Changed: false}, nil
	}
	identity, err := yamlMapEnsureMap(root, "identity")
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	yamlMapSet(identity, "role_mode", yamlStringNode("first"))
	yamlMapDelete(identity, "group_claims")
	sources, err := yamlMapEnsureSeq(root, "sources")
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	for _, source := range sources.Content {
		if source.Kind != yaml.MappingNode {
			continue
		}
		name := yamlScalarField(source, "name")
		previous, ok := previousReadModes[name]
		if !ok {
			continue
		}
		access, err := yamlMapEnsureMap(source, "access")
		if err != nil {
			return groupGrantsConfigResult{}, err
		}
		if previous != "" {
			yamlMapSet(access, "read", yamlStringNode(previous))
		} else {
			yamlMapDelete(access, "read")
		}
		if len(access.Content) == 0 {
			yamlMapDelete(source, "access")
		}
	}
	content, err := encodeYAML(doc)
	if err != nil {
		return groupGrantsConfigResult{}, err
	}
	if sameYAMLStructure(raw, content) {
		return groupGrantsConfigResult{Content: raw, Changed: false}, nil
	}
	return groupGrantsConfigResult{Content: content, Changed: true}, nil
}

func (a *API) defaultGraphjinEndpoint(ctx context.Context, orgID string) (string, error) {
	var graphqlURL *string
	err := a.DB.QueryRow(ctx, `select graphql_url from data_source where org_id = $1 and enabled = true
		order by is_default desc, created_at asc limit 1`, orgID).Scan(&graphqlURL)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (graphqlURL == nil || strings.TrimSpace(*graphqlURL) == "")) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	clean := strings.TrimRight(strings.TrimSpace(*graphqlURL), "/")
	if !strings.HasSuffix(clean, "/api/v1/graphql") {
		clean += "/api/v1/graphql"
	}
	return clean, nil
}

// fetchGraphjinColumnCatalog is fetchGraphjinColumnCatalog() in
// apps/worker/src/graphjin/group-grants-service.ts.
func (a *API) fetchGraphjinColumnCatalog(ctx context.Context, orgID, endpoint string, sources []string) (map[string][]catalogTableColumns, error) {
	wanted := map[string]bool{}
	for _, s := range sources {
		wanted[s] = true
	}
	type tableKey struct {
		source string
		key    string
	}
	bySourceOrder := map[string][]string{}
	bySourceTables := map[tableKey]*catalogTableColumns{}
	token, err := a.graphjinToken(orgID, "admin", 120*time.Second)
	if err != nil {
		return nil, err
	}
	const pageSize = 500
	client := &http.Client{Timeout: 60 * time.Second}
	for offset := 0; offset < 200_000; offset += pageSize {
		query := fmt.Sprintf(`query GroupGrantsCatalog { gj_catalog(where: { kind: { eq: "column" } }, limit: %d, offset: %d, order_by: { id: asc }) { database_name schema_name table_name column_name } }`, pageSize, offset)
		payload, _ := json.Marshal(map[string]string{"query": query})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		raw, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return nil, fmt.Errorf("GraphJin HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
		}
		var parsed struct {
			Data struct {
				Catalog []struct {
					DatabaseName *string `json:"database_name"`
					SchemaName   *string `json:"schema_name"`
					TableName    *string `json:"table_name"`
					ColumnName   *string `json:"column_name"`
				} `json:"gj_catalog"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, err
		}
		if len(parsed.Errors) > 0 {
			msgs := make([]string, len(parsed.Errors))
			for i, e := range parsed.Errors {
				msgs[i] = e.Message
			}
			return nil, fmt.Errorf("GraphJin catalog read failed: %s", strings.Join(msgs, "; "))
		}
		page := parsed.Data.Catalog
		for _, row := range page {
			if row.DatabaseName == nil || *row.DatabaseName == "" || row.TableName == nil || *row.TableName == "" || row.ColumnName == nil || *row.ColumnName == "" {
				continue
			}
			dbName := *row.DatabaseName
			if !wanted[dbName] {
				continue
			}
			schema := ""
			if row.SchemaName != nil {
				schema = *row.SchemaName
			}
			key := schema + "." + *row.TableName
			tk := tableKey{source: dbName, key: key}
			entry := bySourceTables[tk]
			if entry == nil {
				entry = &catalogTableColumns{Schema: schema, Table: *row.TableName}
				bySourceTables[tk] = entry
				bySourceOrder[dbName] = append(bySourceOrder[dbName], key)
			}
			if !slices.Contains(entry.Columns, *row.ColumnName) {
				entry.Columns = append(entry.Columns, *row.ColumnName)
			}
		}
		if len(page) < pageSize {
			break
		}
	}
	out := map[string][]catalogTableColumns{}
	for source, keys := range bySourceOrder {
		list := make([]catalogTableColumns, 0, len(keys))
		for _, k := range keys {
			list = append(list, *bySourceTables[tableKey{source: source, key: k}])
		}
		out[source] = list
	}
	return out, nil
}

func (a *API) defaultGroupGrantsDeps(orgID string) groupGrantsDeps {
	return groupGrantsDeps{
		ConfigFile: strings.TrimSpace(os.Getenv("OPENNEKO_GRAPHJIN_CONFIG")),
		Restart: func(ctx context.Context, configFile string) error {
			endpoint, err := a.defaultGraphjinEndpoint(ctx, orgID)
			if err != nil || endpoint == "" {
				return err
			}
			return requestGraphjinRestart(ctx, configFile, endpoint)
		},
		Catalog: func(ctx context.Context, sources []string) (map[string][]catalogTableColumns, error) {
			endpoint, err := a.defaultGraphjinEndpoint(ctx, orgID)
			if err != nil {
				return nil, err
			}
			if endpoint == "" {
				return nil, errors.New("no enabled GraphJin data source")
			}
			return a.fetchGraphjinColumnCatalog(ctx, orgID, endpoint, sources)
		},
	}
}

func (a *API) setGroupGrantsEnabled(ctx context.Context, orgID string, enabled bool, actorUserID *string, previousReadModes map[string]string) error {
	hasModes := previousReadModes != nil
	modesJSON := []byte("{}")
	if hasModes {
		modesJSON, _ = json.Marshal(previousReadModes)
	}
	_, err := a.DB.Exec(ctx, `
		insert into data_access_settings (org_id, group_grants_enabled, enabled_by_user_id, previous_read_modes, updated_at)
		values ($1, $2, $3, $4::jsonb, now())
		on conflict (org_id) do update set
			group_grants_enabled = excluded.group_grants_enabled,
			enabled_by_user_id = excluded.enabled_by_user_id,
			previous_read_modes = case when $5 then excluded.previous_read_modes else data_access_settings.previous_read_modes end,
			updated_at = now()`,
		orgID, enabled, actorUserID, string(modesJSON), hasModes)
	return err
}

func (a *API) recordPreviousReadModes(ctx context.Context, orgID string, modes map[string]string) error {
	if len(modes) == 0 {
		return nil
	}
	modesJSON, _ := json.Marshal(modes)
	_, err := a.DB.Exec(ctx, `
		update data_access_settings
		set previous_read_modes = $2::jsonb || previous_read_modes, updated_at = now()
		where org_id = $1`, orgID, string(modesJSON))
	return err
}

func (a *API) getPreviousReadModes(ctx context.Context, orgID string) (map[string]string, error) {
	var raw []byte
	err := a.DB.QueryRow(ctx, `select previous_read_modes from data_access_settings where org_id = $1`, orgID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || len(raw) == 0 {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	for k, v := range parsed {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, nil
}

func (a *API) seedEveryoneDataAccess(ctx context.Context, orgID string, catalog map[string][]catalogTableColumns) (int, error) {
	var everyoneID string
	err := a.DB.QueryRow(ctx, `select id::text from user_group where org_id = $1 and slug = 'everyone'`, orgID).Scan(&everyoneID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	existingRules, err := a.dataAccessRules(ctx, orgID, &everyoneID)
	if err != nil {
		return 0, err
	}
	existingSources := map[string]bool{}
	for _, r := range existingRules {
		existingSources[r.Source] = true
	}
	sources := make([]string, 0, len(catalog))
	for s := range catalog {
		sources = append(sources, s)
	}
	slices.Sort(sources)
	created := 0
	for _, source := range sources {
		if existingSources[source] {
			continue
		}
		for _, table := range catalog[source] {
			if len(table.Columns) == 0 || !sqlIdentifier.MatchString(table.Table) {
				continue
			}
			if _, err := a.upsertDataAccessRule(ctx, actor{OrgID: orgID}, everyoneID, source, table.Schema, table.Table, table.Columns, nil); err != nil {
				return created, err
			}
			created++
		}
	}
	return created, nil
}

func (a *API) groupHoldsItem(ctx context.Context, orgID, groupID, itemType, itemID string) (bool, error) {
	var slug string
	err := a.DB.QueryRow(ctx, `select slug from user_group where org_id = $1 and id = $2`, orgID, groupID).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if slug == "administrators" {
		return true, nil
	}
	rows, err := a.DB.Query(ctx, `select item_type, item_id from item_grant where org_id = $1 and group_id = $2 and item_type in ($3, 'pack')`, orgID, groupID, itemType)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var packGrants []string
	for rows.Next() {
		var t, id string
		if err := rows.Scan(&t, &id); err != nil {
			return false, err
		}
		if t == itemType && (id == itemID || id == "*") {
			return true, nil
		}
		if t == "pack" {
			packGrants = append(packGrants, id)
		}
	}
	if err := rows.Err(); err != nil || len(packGrants) == 0 {
		return false, err
	}
	containing, err := a.packsContaining(ctx, orgID, itemType, itemID)
	if err != nil {
		return false, err
	}
	for _, g := range packGrants {
		if g == "*" && len(containing) > 0 || slices.Contains(containing, g) {
			return true, nil
		}
	}
	return false, nil
}

func (a *API) loadGroupGrantInputs(ctx context.Context, orgID string, apiOperationIDs []string) ([]groupGrantGroup, []dataAccessRuleInput, map[string][]string, error) {
	rows, err := a.DB.Query(ctx, `select id::text, slug, name from user_group where org_id = $1 and slug <> 'administrators' order by slug`, orgID)
	if err != nil {
		return nil, nil, nil, err
	}
	var groups []groupGrantGroup
	for rows.Next() {
		var g groupGrantGroup
		if err := rows.Scan(&g.ID, &g.Slug, &g.Name); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}

	allRules, err := a.dataAccessRules(ctx, orgID, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	var rules []dataAccessRuleInput
	for _, r := range allRules {
		holds, err := a.groupHoldsItem(ctx, orgID, r.GroupID, "data_source", r.Source)
		if err != nil {
			return nil, nil, nil, err
		}
		if holds {
			var parsedFilter any
			if len(r.RowFilter) > 0 && string(r.RowFilter) != "null" {
				_ = json.Unmarshal(r.RowFilter, &parsedFilter)
			}
			rules = append(rules, dataAccessRuleInput{
				GroupSlug:   r.GroupSlug,
				GroupName:   r.GroupName,
				Source:      r.Source,
				TableSchema: r.TableSchema,
				TableName:   r.TableName,
				Columns:     r.Columns,
				RowFilter:   parsedFilter,
			})
		}
	}

	apiOperationHolders := map[string][]string{}
	for _, op := range apiOperationIDs {
		var holders []string
		for _, g := range groups {
			holds, err := a.groupHoldsItem(ctx, orgID, g.ID, "api_operation", op)
			if err != nil {
				return nil, nil, nil, err
			}
			if holds {
				holders = append(holders, g.Slug)
			}
		}
		if len(holders) > 0 {
			apiOperationHolders[op] = holders
		}
	}
	return groups, rules, apiOperationHolders, nil
}

func (a *API) resolveGroupGrantsDeps(orgID string, deps *groupGrantsDeps) groupGrantsDeps {
	if deps != nil {
		return *deps
	}
	if a.GroupGrantsDeps != nil {
		return a.GroupGrantsDeps(orgID)
	}
	return a.defaultGroupGrantsDeps(orgID)
}

func (a *API) enableGroupGrants(ctx context.Context, orgID string, actorUserID *string, deps *groupGrantsDeps) (groupGrantsEnableResult, error) {
	d := a.resolveGroupGrantsDeps(orgID, deps)
	if strings.TrimSpace(d.ConfigFile) == "" {
		return groupGrantsEnableResult{}, errors.New("GraphJin configuration is unavailable")
	}
	enabled, err := a.groupGrantsEnabled(ctx, orgID)
	if err != nil {
		return groupGrantsEnableResult{}, err
	}
	if enabled {
		applied, err := a.applyGroupGrants(ctx, orgID, &d)
		if err != nil {
			return groupGrantsEnableResult{}, err
		}
		return groupGrantsEnableResult{groupGrantsApplyResult: applied, SeededRules: 0}, nil
	}
	raw, err := os.ReadFile(d.ConfigFile)
	if err != nil {
		return groupGrantsEnableResult{}, err
	}
	modes := readDatabaseSourceReadModes(string(raw))
	sources := make([]string, 0, len(modes))
	for s := range modes {
		sources = append(sources, s)
	}
	slices.Sort(sources)
	catalog := map[string][]catalogTableColumns{}
	if len(sources) > 0 && d.Catalog != nil {
		catalog, err = d.Catalog(ctx, sources)
		if err != nil {
			return groupGrantsEnableResult{}, err
		}
	}
	seededRules, err := a.seedEveryoneDataAccess(ctx, orgID, catalog)
	if err != nil {
		return groupGrantsEnableResult{}, err
	}
	if err := a.setGroupGrantsEnabled(ctx, orgID, true, actorUserID, modes); err != nil {
		return groupGrantsEnableResult{}, err
	}
	applied, err := a.applyGroupGrants(ctx, orgID, &d)
	if err != nil {
		return groupGrantsEnableResult{}, err
	}
	return groupGrantsEnableResult{groupGrantsApplyResult: applied, SeededRules: seededRules}, nil
}

func (a *API) disableGroupGrants(ctx context.Context, orgID string, actorUserID *string, deps *groupGrantsDeps) (groupGrantsDisableResult, error) {
	d := a.resolveGroupGrantsDeps(orgID, deps)
	if strings.TrimSpace(d.ConfigFile) == "" {
		return groupGrantsDisableResult{}, errors.New("GraphJin configuration is unavailable")
	}
	if err := a.setGroupGrantsEnabled(ctx, orgID, false, actorUserID, nil); err != nil {
		return groupGrantsDisableResult{}, err
	}
	release, err := acquireGraphjinConfigLock(d.ConfigFile)
	if err != nil {
		return groupGrantsDisableResult{}, err
	}
	changed := false
	err = func() error {
		defer release()
		raw, err := os.ReadFile(d.ConfigFile)
		if err != nil {
			return err
		}
		modes, err := a.getPreviousReadModes(ctx, orgID)
		if err != nil {
			return err
		}
		restored, err := removeGroupGrantsFromConfig(string(raw), modes)
		if err != nil {
			return err
		}
		if restored.Changed {
			info, err := os.Stat(d.ConfigFile)
			if err != nil {
				return err
			}
			if err := writeFileAtomic(d.ConfigFile, []byte(restored.Content), info.Mode().Perm(), ".group-grants"); err != nil {
				return err
			}
			changed = true
		}
		return nil
	}()
	if err != nil {
		return groupGrantsDisableResult{}, err
	}
	if changed && d.Restart != nil {
		if err := d.Restart(ctx, d.ConfigFile); err != nil {
			return groupGrantsDisableResult{}, err
		}
	}
	return groupGrantsDisableResult{Changed: changed}, nil
}

func (a *API) applyGroupGrants(ctx context.Context, orgID string, deps *groupGrantsDeps) (groupGrantsApplyResult, error) {
	enabled, err := a.groupGrantsEnabled(ctx, orgID)
	if err != nil {
		return groupGrantsApplyResult{}, err
	}
	if !enabled {
		return groupGrantsApplyResult{Enabled: false, Changed: false, Roles: []string{}}, nil
	}
	d := a.resolveGroupGrantsDeps(orgID, deps)
	if strings.TrimSpace(d.ConfigFile) == "" {
		return groupGrantsApplyResult{}, errors.New("GraphJin configuration is unavailable")
	}
	release, err := acquireGraphjinConfigLock(d.ConfigFile)
	if err != nil {
		return groupGrantsApplyResult{}, err
	}
	var result groupGrantsApplyResult
	err = func() error {
		defer release()
		rawBytes, err := os.ReadFile(d.ConfigFile)
		if err != nil {
			return err
		}
		raw := string(rawBytes)
		groups, rules, apiHolders, err := a.loadGroupGrantInputs(ctx, orgID, listConfigAPIOperations(raw))
		if err != nil {
			return err
		}
		model, err := buildGroupGrantsModel(groups, rules, apiHolders)
		if err != nil {
			return err
		}
		stored, err := a.getPreviousReadModes(ctx, orgID)
		if err != nil {
			return err
		}
		unseen := map[string]string{}
		for source, mode := range readDatabaseSourceReadModes(raw) {
			if _, ok := stored[source]; !ok && mode != "admin" {
				unseen[source] = mode
			}
		}
		if len(unseen) > 0 {
			if err := a.recordPreviousReadModes(ctx, orgID, unseen); err != nil {
				return err
			}
		}
		mergedModes := map[string]string{}
		for k, v := range unseen {
			mergedModes[k] = v
		}
		for k, v := range stored {
			mergedModes[k] = v
		}
		patched, err := applyGroupGrantsToConfig(raw, model, mergedModes)
		if err != nil {
			return err
		}
		if patched.Changed {
			info, err := os.Stat(d.ConfigFile)
			if err != nil {
				return err
			}
			if err := writeFileAtomic(d.ConfigFile, []byte(patched.Content), info.Mode().Perm(), ".group-grants"); err != nil {
				return err
			}
		}
		roles := make([]string, len(model.Roles))
		for i, r := range model.Roles {
			roles[i] = r.Role
		}
		result = groupGrantsApplyResult{
			Enabled: true,
			Changed: patched.Changed,
			Roles:   roles,
		}
		return nil
	}()
	if err != nil {
		return groupGrantsApplyResult{}, err
	}
	if result.Changed && d.Restart != nil {
		if err := d.Restart(ctx, d.ConfigFile); err != nil {
			return groupGrantsApplyResult{}, err
		}
	}
	return result, nil
}

func (a *API) scheduleGroupGrantsApply(ctx context.Context, orgIDs ...string) {
	orgID := ""
	if len(orgIDs) > 0 {
		orgID = strings.TrimSpace(orgIDs[0])
	}
	if orgID == "" && a.DB != nil {
		orgID, _ = a.OrgID(ctx)
	}
	if orgID == "" {
		orgID = "default"
	}
	if a.GroupGrantsScheduled != nil {
		a.GroupGrantsScheduled(orgID)
	}
	a.groupGrantsMu.Lock()
	if a.groupGrantsTimers == nil {
		a.groupGrantsTimers = map[string]*time.Timer{}
	}
	if existing := a.groupGrantsTimers[orgID]; existing != nil {
		existing.Stop()
	}
	delay := a.GroupGrantsDelay
	if delay <= 0 {
		delay = 30 * time.Second
	}
	run := a.GroupGrantsRun
	if run == nil {
		run = func(org string) error {
			if a.DB == nil {
				return nil
			}
			_, err := a.applyGroupGrants(context.Background(), org, nil)
			return err
		}
	}
	a.groupGrantsTimers[orgID] = time.AfterFunc(delay, func() {
		a.groupGrantsMu.Lock()
		delete(a.groupGrantsTimers, orgID)
		a.groupGrantsMu.Unlock()
		if err := run(orgID); err != nil {
			log.Printf("[group-grants] apply failed for %s: %v", orgID, err)
		}
	})
	a.groupGrantsMu.Unlock()
}

func (a *API) stopGroupGrantsTimers() {
	a.groupGrantsMu.Lock()
	defer a.groupGrantsMu.Unlock()
	for orgID, t := range a.groupGrantsTimers {
		t.Stop()
		delete(a.groupGrantsTimers, orgID)
	}
}
