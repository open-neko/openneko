package webfront

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

var (
	packTemplate       = regexp.MustCompile(`^\{\{([^}]+)}}$`)
	packHTTPToken      = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$")
	packSecretRef      = regexp.MustCompile(`^\{\{secret\.[a-z][a-z0-9_.-]+}}$`)
	graphqlName        = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)
	packReadinessKnown = []string{"db-connect", "db-read-only", "graphjin-reload", "analytics-smoke", "queries"}
	packSourceAllowed  = []string{"name", "kind", "type", "host", "port", "dbname", "user", "password", "base_url", "openapi", "auth", "read_only", "capabilities", "operations"}
)

// ianaTimezone is new Intl.DateTimeFormat("en", { timeZone }) not throwing.
func ianaTimezone(name string) bool {
	if name == "" || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// packValue is packValue() of apps/worker/src/packs/declarative.ts.
func packValue(value any, inputs map[string]any, secrets map[string]string) (any, error) {
	switch t := value.(type) {
	case string:
		m := packTemplate.FindStringSubmatch(t)
		if m == nil {
			if strings.Contains(t, "{{") {
				return nil, errors.New("pack templates must occupy the complete value")
			}
			return t, nil
		}
		key := strings.TrimSpace(m[1])
		var resolved any
		var ok bool
		if name, isSecret := strings.CutPrefix(key, "secret."); isSecret {
			resolved, ok = secrets[name]
		} else {
			resolved, ok = inputs[key]
		}
		if !ok {
			return nil, fmt.Errorf("missing pack template input %s", key)
		}
		return resolved, nil
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			v, err := packValue(item, inputs, secrets)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for _, k := range sortedKeys(t) {
			v, err := packValue(t[k], inputs, secrets)
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	}
	return value, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// packVariablesCheck is packVariables() for its errors only.
func packVariablesCheck(value any, inputs map[string]any) error {
	if value == nil {
		return nil
	}
	definition, err := packValue(value, inputs, nil)
	if err != nil {
		return err
	}
	_, err = resolveWatcherVariables(jsValueOf(definition), time.Now())
	return err
}

func packAPIAuthCheck(auth any) error {
	m, _ := auth.(map[string]any)
	only := func(keys ...string) bool {
		for k := range m {
			if !slices.Contains(keys, k) {
				return false
			}
		}
		return true
	}
	if _, ok := m["token"].(string); ok && m["type"] == "bearer" && only("type", "token") {
		return nil
	}
	name, nameOK := m["key_name"].(string)
	_, valueOK := m["key_value"].(string)
	in, hasIn := m["key_in"]
	if m["type"] == "api_key" && valueOK && nameOK && packHTTPToken.MatchString(name) && (!hasIn || in == "header" || in == "query") && only("type", "key_name", "key_value", "key_in") {
		return nil
	}
	return errors.New("custom API sources support bearer or api_key authentication only")
}

func specMethods(spec *packs.Artifact) map[string]string {
	methods := map[string]string{}
	if spec == nil {
		return methods
	}
	paths, _ := spec.Record()["paths"].(map[string]any)
	for _, p := range sortedKeys(paths) {
		ops, _ := paths[p].(map[string]any)
		for _, method := range sortedKeys(ops) {
			if op, ok := ops[method].(map[string]any); ok {
				if id, ok := op["operationId"].(string); ok {
					methods[id] = strings.ToLower(method)
				}
			}
		}
	}
	return methods
}

func findSpec(bundle *packs.Bundle, openapi any) *packs.Artifact {
	for i := range bundle.Artifacts {
		if a := &bundle.Artifacts[i]; a.Kind == packs.KindSpec && a.Path == packString(openapi) {
			return a
		}
	}
	return nil
}

func hasArtifact(bundle *packs.Bundle, match func(packs.Artifact) bool) bool {
	return slices.ContainsFunc(bundle.Artifacts, match)
}

type packExposure struct{ mutation, as string }

// declarativePackChecks runs the checks of declarativeGraphjinUpdate() with
// no retired sources, no API documents and authenticated callers.
func declarativePackChecks(bundle *packs.Bundle, inputs map[string]any, secrets map[string]string, bindings map[string]string) error {
	h := bundle.Manifest.Health
	var checks []string
	checks = append(checks, h.RequiredPreflight...)
	checks = append(checks, h.PostInstall...)
	checks = append(checks, h.PostWriteCanary...)
	for _, k := range h.ReadinessKeys {
		checks = append(checks, h.Readiness[k]...)
	}
	for _, check := range checks {
		if slices.Contains([]string{"db-connect", "analytics-smoke", "queries"}, check) &&
			!hasArtifact(bundle, func(a packs.Artifact) bool { return a.Kind == packs.KindSavedQuery }) {
			return fmt.Errorf("%s requires a saved query", check)
		}
		if bundle.Manifest.Artifacts.GraphJin == nil {
			return fmt.Errorf("%s requires GraphJin artifacts", check)
		}
		if !slices.Contains(packReadinessKnown, check) {
			return fmt.Errorf("unsupported pack readiness check %s", check)
		}
		if strings.HasPrefix(check, "db-") && !hasArtifact(bundle, func(a packs.Artifact) bool {
			return a.Kind == packs.KindSource && a.Record()["kind"] == "database"
		}) {
			return fmt.Errorf("%s requires a database source", check)
		}
	}
	for _, art := range bundle.Artifacts {
		value := art.Record()
		switch art.Kind {
		case packs.KindMetric, packs.KindWatcher:
			variables := value["variables"]
			if art.Kind == packs.KindMetric {
				execution, _ := value["execution"].(map[string]any)
				variables = execution["variables"]
			}
			if err := packVariablesCheck(variables, inputs); err != nil {
				return err
			}
			signals, _ := value["readinessSignals"].([]any)
			for _, s := range signals {
				if _, ok := h.Readiness[packString(s)]; !ok {
					return fmt.Errorf("unknown pack readiness signal %s", packString(s))
				}
			}
		case packs.KindWorkflow:
			if schedule, ok := value["schedule"].(map[string]any); ok {
				timezone, ok := inputs[packString(schedule["timezoneInput"])]
				if !ok || timezone == nil {
					timezone = schedule["timezoneInput"]
				}
				if !ianaTimezone(packString(timezone)) {
					return fmt.Errorf("workflow %s timezone must resolve to an IANA timezone", art.Key)
				}
			}
		case packs.KindSavedQuery:
			if _, err := readPackQuery(packString(art.Content)); err != nil {
				return err
			}
		case packs.KindSpec:
			if err := localRefs(art.Content); err != nil {
				return err
			}
		}
	}

	exposures := map[string]map[string]packExposure{}
	for _, art := range bundle.Artifacts {
		source := art.Record()
		ops, declared := source["operations"]
		if art.Kind != packs.KindSource || source["kind"] != "api" || !declared {
			continue
		}
		name := packString(source["name"])
		overrides, ok := ops.(map[string]any)
		if !ok {
			return fmt.Errorf("API source %s operations must be an object", name)
		}
		spec := findSpec(bundle, source["openapi"])
		if spec == nil {
			return fmt.Errorf("API source %s must reference a bundled OpenAPI spec", name)
		}
		methods := specMethods(spec)
		own := map[string]packExposure{}
		for _, id := range sortedKeys(overrides) {
			override, ok := overrides[id].(map[string]any)
			if !ok {
				return fmt.Errorf("API operation %s override must be an object", id)
			}
			if methods[id] != "get" {
				return fmt.Errorf("API read override %s must reference a GET operation", id)
			}
			if len(override) != 1 || override["expose_top_level"] != true {
				return fmt.Errorf("API read override %s may only set expose_top_level: true", id)
			}
			own[id] = packExposure{}
		}
		exposures[name] = own
	}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindAction {
			continue
		}
		adapter, _ := art.Record()["adapter"].(map[string]any)
		if adapter["kind"] != "graphjin_api_operation" {
			continue
		}
		sourceName := packString(adapter["source"])
		var source map[string]any
		for _, s := range bundle.Artifacts {
			if s.Kind == packs.KindSource && packString(s.Record()["name"]) == sourceName {
				source = s.Record()
				break
			}
		}
		if source == nil {
			return fmt.Errorf("action %s references missing API source %s", art.Key, sourceName)
		}
		openapi := packString(source["openapi"])
		sourceSpec := strings.TrimSuffix(path.Base(openapi), path.Ext(openapi))
		specName := sourceSpec
		if v, ok := adapter["spec"]; ok {
			specName = packString(v)
		}
		if specName != sourceSpec {
			return fmt.Errorf("action %s references spec %s outside source %s", art.Key, specName, sourceName)
		}
		methods := specMethods(findSpec(bundle, source["openapi"]))
		declared, ok := adapter["operations"].(map[string]any)
		if !ok {
			declared = map[string]any{"default": adapter}
		}
		own := exposures[sourceName]
		if own == nil {
			own = map[string]packExposure{}
		}
		for _, k := range sortedKeys(declared) {
			op, ok := declared[k].(map[string]any)
			if !ok {
				continue
			}
			id, root := "", ""
			if v, ok := op["operationId"]; ok && v != nil {
				id = packString(v)
			}
			if v, ok := op["mutationRoot"]; ok && v != nil {
				root = packString(v)
			}
			if id == "" || !graphqlName.MatchString(root) {
				return fmt.Errorf("action %s has an invalid GraphJin API operation", art.Key)
			}
			method, ok := methods[id]
			if !ok {
				return fmt.Errorf("action %s references missing OpenAPI operation %s", art.Key, id)
			}
			if method == "get" {
				return fmt.Errorf("action %s cannot expose read operation %s as a mutation", art.Key, id)
			}
			exposure := packExposure{mutation: "yes", as: root}
			if current, ok := own[id]; ok && current != exposure {
				return fmt.Errorf("API operation %s has conflicting action exposure", id)
			}
			own[id] = exposure
		}
		exposures[sourceName] = own
	}

	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindSource {
			continue
		}
		authored := art.Record()
		for _, k := range sortedKeys(authored) {
			if !slices.Contains(packSourceAllowed, k) {
				return fmt.Errorf("unsupported custom source property %s", k)
			}
		}
		if bindings[art.Key] != "" {
			for k := range authored {
				if k != "name" && k != "kind" && k != "read_only" {
					return errors.New("a source binding cannot also declare connection settings")
				}
			}
			continue
		}
		auth, _ := authored["auth"].(map[string]any)
		for _, field := range []struct {
			m   map[string]any
			key string
		}{{authored, "password"}, {auth, "token"}, {auth, "key_value"}} {
			v, ok := field.m[field.key]
			if !ok {
				continue
			}
			if s, isString := v.(string); !isString || !packSecretRef.MatchString(s) {
				return errors.New("pack credentials must use declared secret references")
			}
		}
		withAuth := authored
		for _, c := range bundle.Manifest.OAuth {
			if c.Scope == "user" && auth != nil && auth["token"] == "{{secret."+c.AccessToken+"}}" {
				bound := map[string]any{}
				for k, v := range auth {
					bound[k] = v
				}
				bound["token"] = "request-bound"
				withAuth = map[string]any{}
				for k, v := range authored {
					withAuth[k] = v
				}
				withAuth["auth"] = bound
				break
			}
		}
		resolved, err := packValue(withAuth, inputs, secrets)
		if err != nil {
			return err
		}
		source := resolved.(map[string]any)
		if source["kind"] == "database" {
			if source["read_only"] == false {
				return errors.New("custom pack database sources must be read-only")
			}
			if !packTruthy(source["host"]) || !packTruthy(source["dbname"]) || !packTruthy(source["type"]) {
				return errors.New("database source requires an explicit connection; select an existing-source binding for a source without connection settings")
			}
			if !slices.Contains([]string{"postgres", "mysql", "mariadb"}, packString(source["type"])) {
				return errors.New("unsupported database type")
			}
			continue
		}
		if findSpec(bundle, source["openapi"]) == nil {
			return fmt.Errorf("API source %s must reference a bundled OpenAPI spec", packString(source["name"]))
		}
		base, err := url.Parse(packString(source["base_url"]))
		if err != nil || base.Scheme == "" || (base.Host == "" && base.Opaque == "") {
			return errors.New("Invalid URL")
		}
		if base.Scheme != "http" && base.Scheme != "https" || base.User != nil && (base.User.Username() != "" || hasPassword(base.User)) {
			return errors.New("API base URL must be HTTP(S) without credentials")
		}
		if a, ok := source["auth"]; ok && a != nil {
			if err := packAPIAuthCheck(a); err != nil {
				return err
			}
		}
		requested, hasCaps := source["capabilities"].(map[string]any)
		if _, present := source["capabilities"]; present && source["capabilities"] != nil {
			bad := !hasCaps
			for k, v := range requested {
				if _, isBool := v.(bool); !isBool || !slices.Contains([]string{"api.read", "api.write", "api.delete"}, k) {
					bad = true
				}
			}
			if bad {
				return errors.New("custom API source capabilities must be boolean api.read, api.write, or api.delete values")
			}
		}
		write, remove := requested["api.write"] == true, requested["api.delete"] == true
		if (write || remove) && source["read_only"] != false {
			return errors.New("custom API sources requesting write or delete capability must set read_only to false")
		}
		if source["read_only"] == false && !write && !remove {
			return errors.New("custom API sources may set read_only to false only when requesting write or delete capability")
		}
	}
	return nil
}

func hasPassword(u *url.Userinfo) bool {
	p, ok := u.Password()
	return ok && p != ""
}

func localRefs(value any) error {
	switch t := value.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			if k == "$ref" {
				if s, ok := t[k].(string); !ok || !strings.HasPrefix(s, "#/") {
					return errors.New("pack OpenAPI references must be local")
				}
			}
			if err := localRefs(t[k]); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range t {
			if err := localRefs(item); err != nil {
				return err
			}
		}
	}
	return nil
}

// readPackQuery is readQuery(): one query operation, plus fragments.
func readPackQuery(query string) (*ast.QueryDocument, error) {
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil {
		var gqlErr *gqlerror.Error
		if errors.As(err, &gqlErr) {
			return nil, fmt.Errorf("Syntax Error: %s.", strings.TrimSuffix(gqlErr.Message, "."))
		}
		return nil, err
	}
	if len(doc.Operations) != 1 || doc.Operations[0].Operation != ast.Query {
		return nil, errors.New("custom pack saved queries must contain one read-only operation")
	}
	return doc, nil
}

func packQueryTables(bundle *packs.Bundle, bindings map[string]string) ([]map[string]string, error) {
	tables, _, err := bindPackQueries(bundle, bindings)
	return tables, err
}

// bindPackQueries is bindPackQueries(): the pack-owned table alias for each
// root field of each saved query, in document order, and the bound saved
// queries by artifact path. The printed text is gqlparser's, not graphql-js's.
func bindPackQueries(bundle *packs.Bundle, bindings map[string]string) ([]map[string]string, map[string]string, error) {
	tables := []map[string]string{}
	queries := map[string]string{}
	var sources, databases []packs.Artifact
	for _, a := range bundle.Artifacts {
		if a.Kind == packs.KindSource {
			sources = append(sources, a)
			if a.Record()["kind"] == "database" {
				databases = append(databases, a)
			}
		}
	}
	if len(databases) == 0 {
		return tables, queries, nil
	}
	names := map[string]string{}
	defaultDatabase := ""
	for i, a := range databases {
		name := packString(a.Record()["name"])
		if bound, ok := bindings[a.Key]; ok {
			names[name] = bound
		} else {
			names[name] = name
		}
		if i == 0 && len(sources) == 1 {
			defaultDatabase = name
		}
	}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindSavedQuery {
			continue
		}
		doc, err := readPackQuery(packString(art.Content))
		if err != nil {
			return nil, nil, err
		}
		roots := map[*ast.Field]bool{}
		var collect func(set ast.SelectionSet, seen []string) error
		collect = func(set ast.SelectionSet, seen []string) error {
			for _, sel := range set {
				switch s := sel.(type) {
				case *ast.Field:
					roots[s] = true
				case *ast.InlineFragment:
					if err := collect(s.SelectionSet, seen); err != nil {
						return err
					}
				case *ast.FragmentSpread:
					fragment := doc.Fragments.ForName(s.Name)
					if fragment == nil || slices.Contains(seen, s.Name) {
						return errors.New("invalid saved query fragment")
					}
					if err := collect(fragment.SelectionSet, append(slices.Clone(seen), s.Name)); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := collect(doc.Operations[0].SelectionSet, nil); err != nil {
			return nil, nil, err
		}
		var visit func(set ast.SelectionSet) error
		visit = func(set ast.SelectionSet) error {
			for _, sel := range set {
				switch s := sel.(type) {
				case *ast.Field:
					var database []*ast.Directive
					for _, d := range s.Directives {
						if d.Name == "database" {
							database = append(database, d)
						}
					}
					if len(database) > 1 {
						return errors.New("duplicate database directive")
					}
					if len(database) > 0 || roots[s] {
						logical := ""
						var arg *ast.Value
						if len(database) > 0 {
							if a := database[0].Arguments.ForName("name"); a != nil {
								arg = a.Value
							}
						}
						switch {
						case arg != nil && (arg.Kind == ast.StringValue || arg.Kind == ast.BlockValue || arg.Kind == ast.EnumValue):
							logical = arg.Raw
						case len(database) == 0:
							logical = defaultDatabase
						}
						if logical == "" || names[logical] == "" {
							return fmt.Errorf(`saved query %s must select a declared database with @database(name: "source_name")`, art.Key)
						}
						if roots[s] {
							name := "pack_" + packs.SHA256([]byte(bundle.Manifest.Metadata.ID + ":" + logical + ":" + names[logical] + ":" + s.Name))[:20]
							if !slices.ContainsFunc(tables, func(t map[string]string) bool { return t["name"] == name }) {
								tables = append(tables, map[string]string{"name": name, "table": s.Name, "source": names[logical]})
							}
						}
						kept := slices.DeleteFunc(slices.Clone(s.Directives), func(d *ast.Directive) bool { return d.Name == "database" })
						s.Directives = append(kept, &ast.Directive{Name: "database", Arguments: ast.ArgumentList{
							{Name: "name", Value: &ast.Value{Kind: ast.StringValue, Raw: names[logical]}},
						}})
						if roots[s] {
							if s.Alias == "" {
								s.Alias = s.Name
							}
							s.Name = "pack_" + packs.SHA256([]byte(bundle.Manifest.Metadata.ID + ":" + logical + ":" + names[logical] + ":" + s.Name))[:20]
						}
					}
					if err := visit(s.SelectionSet); err != nil {
						return err
					}
				case *ast.InlineFragment:
					if err := visit(s.SelectionSet); err != nil {
						return err
					}
				}
			}
			return nil
		}
		type definition struct {
			start int
			set   ast.SelectionSet
		}
		var defs []definition
		for _, op := range doc.Operations {
			defs = append(defs, definition{op.Position.Start, op.SelectionSet})
		}
		for _, f := range doc.Fragments {
			defs = append(defs, definition{f.Position.Start, f.SelectionSet})
		}
		slices.SortFunc(defs, func(a, b definition) int { return a.start - b.start })
		for _, d := range defs {
			if err := visit(d.set); err != nil {
				return nil, nil, err
			}
		}
		var printed strings.Builder
		formatter.NewFormatter(&printed, formatter.WithIndent("  ")).FormatQueryDocument(doc)
		queries[art.Path] = printed.String()
	}
	return tables, queries, nil
}
