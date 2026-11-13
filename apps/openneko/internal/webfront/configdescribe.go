package webfront

import (
	"strconv"
	"strings"
)

// This file is describeGraphjinConfig() in
// packages/llm/src/graphjin/config-describe.ts.

const describedTablesShown = 12

var databaseProductNames = map[string]string{
	"postgres": "PostgreSQL", "mysql": "MySQL", "mariadb": "MariaDB", "mssql": "SQL Server",
	"oracle": "Oracle", "sqlite": "SQLite", "snowflake": "Snowflake",
}

var accessModeDefaults = map[string][3]string{
	"database": {"account", "blocked", "blocked"},
	"api":      {"authenticated", "blocked", "blocked"},
}

func cfgField(item *jsObj, key string) any {
	if v, ok := item.vals[key]; ok {
		return v
	}
	lower := strings.ToLower(key)
	for _, k := range item.ordered() {
		if strings.ToLower(k) == lower {
			return item.vals[k]
		}
	}
	return nil
}

func cfgItem(item *jsObj, key string) *jsObj {
	if o, ok := cfgField(item, key).(*jsObj); ok {
		return o
	}
	return &jsObj{}
}

func cfgText(item *jsObj, key string) string {
	switch t := cfgField(item, key).(type) {
	case string:
		return strings.TrimFunc(t, isJSSpace)
	case float64:
		return jsNumberText(t)
	}
	return ""
}

func cfgNames(v any) []string {
	list, _ := v.([]any)
	out := []string{}
	for _, entry := range list {
		if s := strings.TrimFunc(jsTemplate(entry), isJSSpace); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func cfgList(values []string) string {
	if len(values) <= describedTablesShown {
		return strings.Join(values, ", ")
	}
	return strings.Join(values[:describedTablesShown], ", ") + " and " + strconv.Itoa(len(values)-describedTablesShown) + " more"
}

func cfgWho(mode string, access *jsObj) string {
	switch mode {
	case "public":
		return "everyone, also people who are not signed in"
	case "authenticated":
		return "everyone who is signed in"
	case "account":
		return "everyone who is signed in, only rows where " + orDefault(cfgText(access, "namespace_column"), "account_id") + " is their organization"
	case "owner":
		return "everyone who is signed in, only rows where " + orDefault(cfgText(access, "owner_column"), "user_id") + " is their own user"
	case "admin":
		return "admins only"
	case "blocked":
		return "nobody"
	}
	return `the "` + mode + `" rule`
}

func cfgAccessSentences(kind string, access *jsObj) []string {
	defaults, ok := accessModeDefaults[kind]
	if !ok {
		defaults = accessModeDefaults["api"]
	}
	read := orDefault(cfgText(access, "read"), defaults[0])
	write := orDefault(cfgText(access, "write"), defaults[1])
	remove := orDefault(cfgText(access, "delete"), defaults[2])
	out := []string{"Who may read: " + cfgWho(read, access) + "."}
	if write == remove {
		return append(out, "Who may change or delete: "+cfgWho(write, access)+".")
	}
	return append(out, "Who may change: "+cfgWho(write, access)+".", "Who may delete: "+cfgWho(remove, access)+".")
}

func cfgTableSentences(access *jsObj) []string {
	var out []string
	counted := func(label string, list []string) {
		if len(list) > 0 {
			out = append(out, label+" ("+strconv.Itoa(len(list))+"): "+cfgList(list)+".")
		}
	}
	counted("Hidden from everyone", cfgNames(cfgField(access, "blocked_tables")))
	counted("Admins only", cfgNames(cfgField(access, "admin_tables")))
	counted("Open to everyone, also without sign-in", cfgNames(cfgField(access, "public_tables")))
	if len(out) > 0 {
		out = append(out, "Every other table follows the read rule.")
	} else {
		out = append(out, "Every table that the login can read follows the read rule.")
	}
	grants, _ := cfgField(access, "grants").([]any)
	for _, g := range grants {
		grant, ok := g.(*jsObj)
		if !ok {
			continue
		}
		role := cfgText(grant, "role")
		tableList, _ := cfgField(grant, "tables").([]any)
		var tables []string
		for _, t := range tableList {
			if table, ok := t.(*jsObj); ok {
				if name := cfgText(table, "name"); name != "" {
					tables = append(tables, name)
				}
			}
		}
		if role != "" && len(tables) > 0 {
			out = append(out, "Role "+role+" may also read: "+cfgList(tables)+".")
		}
	}
	return out
}

func cfgDatabaseSentences(source *jsObj) []string {
	typ := orDefault(strings.ToLower(cfgText(source, "type")), "postgres")
	product, ok := databaseProductNames[typ]
	if !ok {
		product = typ
	}
	var place []string
	if v := cfgText(source, "dbname"); v != "" {
		place = append(place, "database "+v)
	}
	if v := cfgText(source, "host"); v != "" {
		place = append(place, "on "+v)
	}
	if v := cfgText(source, "port"); v != "" && v != "0" {
		place = append(place, "port "+v)
	}
	first := "A " + product + " source"
	if len(place) > 0 {
		first += ": " + strings.Join(place, ", ")
	}
	if user := cfgText(source, "user"); user != "" {
		first += ", with the login " + user
	}
	out := []string{first + "."}
	if cfgField(source, "enable_tls") == true {
		out = append(out, "The connection uses TLS.")
	}
	if cfgField(source, "read_only") == true {
		out = append(out, "OpenNeko only reads it. GraphJin blocks every change to this database.")
	} else {
		out = append(out, "GraphJin does not block changes to this database.")
	}
	access := cfgItem(source, "access")
	return append(append(out, cfgAccessSentences("database", access)...), cfgTableSentences(access)...)
}

func cfgWriteOperations(source *jsObj) []string {
	var out []string
	specs := cfgItem(source, "specs")
	for _, spec := range specs.ordered() {
		config, ok := specs.vals[spec].(*jsObj)
		if !ok {
			continue
		}
		operations, ok := cfgField(config, "operations").(*jsObj)
		if !ok {
			continue
		}
		for _, operation := range operations.ordered() {
			settings, ok := operations.vals[operation].(*jsObj)
			if !ok || cfgField(settings, "expose_mutation") != true {
				continue
			}
			who := "no role"
			if roles := cfgNames(cfgField(settings, "allowed_roles")); len(roles) > 0 {
				who = "the roles " + strings.Join(roles, ", ")
			}
			out = append(out, "The write call "+spec+"/"+operation+" is open to "+who+".")
		}
	}
	return out
}

func cfgAPISentences(source *jsObj) []string {
	out := []string{"An API source, described by OpenAPI documents."}
	if specs, ok := cfgField(source, "specs").(*jsObj); ok && len(specs.ordered()) > 0 {
		out[0] = "An API source, described by the OpenAPI documents " + strings.Join(specs.ordered(), ", ") + "."
	}
	capabilities := cfgItem(source, "capabilities")
	if cfgField(source, "read_only") == true || capabilities.vals["api.write"] != true {
		out = append(out, "OpenNeko only reads it. GraphJin blocks every write call.")
	} else {
		if capabilities.vals["api.delete"] == true {
			out = append(out, "Write and delete calls are on, for the calls named below.")
		} else {
			out = append(out, "Write calls are on, for the calls named below. Delete calls are off.")
		}
		out = append(out, cfgWriteOperations(source)...)
	}
	return append(out, cfgAccessSentences("api", cfgItem(source, "access"))...)
}

func cfgFileSentences(source *jsObj) []string {
	backend := orDefault(cfgText(source, "backend"), "local")
	where := "in OpenNeko's own file storage"
	if backend != "local" {
		store := "S3"
		if backend == "gcs" {
			store = "Google Cloud Storage"
		}
		where = "in the " + store + " bucket " + orDefault(cfgText(source, "bucket"), "(no bucket set)")
		if prefix := cfgText(source, "prefix"); prefix != "" {
			where += ", under " + prefix
		}
	}
	capabilities := cfgItem(source, "capabilities")
	writes := capabilities.vals["files.write"] == true || capabilities.vals["files.delete"] == true
	access := cfgItem(source, "access")
	change := "Nobody can change or delete files through OpenNeko."
	if writes && cfgField(source, "read_only") != true {
		change = "GraphJin lets callers change files here."
	}
	return []string{
		"A file source " + where + ".",
		"Who may list and read files: " + cfgWho(orDefault(cfgText(access, "read"), "authenticated"), access) + ".",
		change,
	}
}

func describeGraphjinSource(source *jsObj) *jsObj {
	kind := orDefault(cfgText(source, "kind"), "database")
	var sentences []string
	switch kind {
	case "database":
		sentences = cfgDatabaseSentences(source)
	case "api":
		sentences = cfgAPISentences(source)
	case "file":
		sentences = cfgFileSentences(source)
	default:
		sentences = []string{"A " + kind + " source. OpenNeko does not describe this kind in detail."}
	}
	if cfgField(source, "default") == true {
		sentences = append(sentences, "It is the default source.")
	}
	list := make([]any, len(sentences))
	for i, sentence := range sentences {
		list[i] = sentence
	}
	return jsObjOf("name", cfgText(source, "name"), "kind", kind, "sentences", list)
}

func describeGraphjinRole(role *jsObj) *jsObj {
	sentence := "No rule gives a person this role."
	if match := cfgText(role, "match"); match != "" {
		sentence = "A person gets this role when " + match + "."
	}
	if comment := cfgText(role, "comment"); comment != "" {
		sentence += " Note: " + comment
	}
	return jsObjOf("name", cfgText(role, "name"), "sentence", sentence)
}

// describeGraphjinConfig gives plain sentences per source and role. The
// input must already be redacted.
func describeGraphjinConfig(sources, roles any) *jsObj {
	describe := func(v any, one func(*jsObj) *jsObj) []any {
		list, _ := parseJSONColumn(v).([]any)
		out := []any{}
		for _, item := range list {
			if o, ok := item.(*jsObj); ok {
				if d := one(o); d.vals["name"] != "" {
					out = append(out, d)
				}
			}
		}
		return out
	}
	return jsObjOf("sources", describe(sources, describeGraphjinSource), "roles", describe(roles, describeGraphjinRole))
}
