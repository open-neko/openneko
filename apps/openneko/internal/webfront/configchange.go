package webfront

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// This file is packages/llm/src/graphjin/config-change.ts, with the file
// source checks of file-source.ts and file-source-assets.ts.

const passwordRequiredMessage = "OpenNeko needs a login with a password. Save the password for this login first."

var errPasswordRequired = errors.New(passwordRequiredMessage)

var (
	presignDuration = regexp.MustCompile(`^([1-9][0-9]*)(s|m|h)$`)
	s3Bucket        = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	gcsBucket       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$`)
	ipv4Bucket      = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)
	checksumPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// jsStr is String(v ?? "").
func jsStr(v any) string {
	if v == nil {
		return ""
	}
	if _, ok := v.(jsUndefined); ok {
		return ""
	}
	return jsTemplate(v)
}

// trimmedText is `typeof v === "string" ? v.trim() : ""`.
func trimmedText(v any) string {
	s, _ := v.(string)
	return jsTrim(s)
}

// selectedText is a one-item list's item, or the value, as trimmed text.
func selectedText(v any, fallback string) string {
	if list, ok := v.([]any); ok && len(list) == 1 {
		v = list[0]
	}
	if s, ok := v.(string); ok {
		return jsTrim(s)
	}
	return fallback
}

func tableList(v any, field string) ([]string, error) {
	if v == nil {
		return []string{}, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of table names", field)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, item := range list {
		name := trimmedText(item)
		if !sourceTableName.MatchString(name) {
			return nil, fmt.Errorf("%s has an invalid table name \"%s\"", field, jsTemplate(item))
		}
		if seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name)
	}
	return out, nil
}

func assertDisjointTables(fields []string, lists ...[]string) error {
	owner := map[string]string{}
	for i, names := range lists {
		for _, name := range names {
			if other, ok := owner[strings.ToLower(name)]; ok {
				return fmt.Errorf("table \"%s\" is in both %s and %s", name, other, fields[i])
			}
			owner[strings.ToLower(name)] = fields[i]
		}
	}
	return nil
}

func stringsToAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// assertDatabaseSourcesStayReadOnly rejects a change that opens a write
// path on a database source, or on a source of unknown kind.
func assertDatabaseSourcesStayReadOnly(update *jsObj, currentSources any) error {
	existing := map[string]*jsObj{}
	if list, ok := currentSources.([]any); ok {
		for _, item := range list {
			if source, ok := item.(*jsObj); ok {
				if name, ok := source.vals["name"].(string); ok {
					existing[strings.ToLower(jsTrim(name))] = source
				}
			}
		}
	}
	var entries []any
	for _, key := range []string{"update_sources", "source_patches"} {
		if list, ok := update.vals[key].([]any); ok {
			entries = append(entries, list...)
		}
	}
	for _, item := range entries {
		entry, ok := item.(*jsObj)
		if !ok {
			continue
		}
		name := jsTrim(jsStr(entry.vals["name"]))
		kind := "database"
		if v, ok := entry.vals["kind"]; ok && v != nil {
			kind = jsTemplate(v)
		} else if current := existing[strings.ToLower(name)]; current != nil && current.vals["kind"] != nil {
			kind = jsTemplate(current.vals["kind"])
		}
		if kind == "api" || kind == "file" {
			continue
		}
		var violations []string
		if v, ok := entry.vals["read_only"].(bool); ok && !v {
			violations = append(violations, "read_only: false")
		}
		access, _ := entry.vals["access"].(*jsObj)
		if access == nil {
			access = &jsObj{}
		}
		for _, mode := range []string{"write", "delete"} {
			if v, ok := access.vals[mode]; ok && v != "blocked" {
				violations = append(violations, fmt.Sprintf("access.%s: %s", mode, jsTemplate(v)))
			}
		}
		capabilities, _ := entry.vals["capabilities"].(*jsObj)
		if capabilities == nil {
			capabilities = &jsObj{}
		}
		for _, key := range []string{"api.write", "api.delete"} {
			if capabilities.vals[key] == true {
				violations = append(violations, "capabilities."+key+": true")
			}
		}
		if _, ok := entry.vals["specs"]; ok {
			violations = append(violations, "specs (API operations)")
		}
		if len(violations) > 0 {
			return fmt.Errorf("database source \"%s\" stays read-only; rejected %s", name, strings.Join(violations, ", "))
		}
	}
	return nil
}

type configUpdateOptions struct {
	managedOpenAPISpecsDir string
	managedLocalFilesRoot  string
}

func lowerRoles(v any) []any {
	out := []any{}
	list, _ := v.([]any)
	for _, role := range list {
		if s := strings.ToLower(jsTrim(jsTemplate(role))); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func operationPatch(payload *jsObj, expose bool, roles []any) *jsObj {
	patch := jsObjOf("expose_mutation", expose, "allowed_roles", roles)
	if as := trimmedText(payload.vals["exposeAs"]); as != "" {
		patch.set("expose_as", as)
	}
	return patch
}

func specsPatch(spec, operation string, patch *jsObj) *jsObj {
	return jsObjOf(spec, jsObjOf("operations", jsObjOf(operation, patch)))
}

// buildGraphjinConfigUpdate turns a typed proposal into GraphJin's additive
// patch. resolve returns a stored secret's value; the model never sends one.
func buildGraphjinConfigUpdate(payload *jsObj, resolve func(string) (string, error), opts configUpdateOptions) (*jsObj, error) {
	action := jsStr(payload.vals["action"])
	update := &jsObj{}
	source := func(field string) string { return jsTrim(jsStr(payload.vals[field])) }
	switch action {
	case "add_role":
		name, match := source("name"), source("match")
		if name == "" || match == "" {
			return nil, errors.New("add_role needs name + match")
		}
		role := jsObjOf("name", name, "match", match)
		if comment := trimmedText(payload.vals["comment"]); comment != "" {
			role.set("comment", comment)
		}
		update.set("roles", []any{role})
		return update, nil
	case "set_source_access":
		name := source("source")
		if name == "" {
			return nil, errors.New("set_source_access needs source")
		}
		access := &jsObj{}
		for _, mode := range []string{"read", "write", "delete"} {
			if v := payload.vals[mode]; jsTruthy(v) {
				access.set(mode, jsTemplate(v))
			}
		}
		if len(access.keys) == 0 {
			return nil, errors.New("set_source_access needs at least one access field")
		}
		update.set("source_patches", []any{jsObjOf("name", name, "access", access)})
		return update, nil
	case "set_source_tables":
		name := source("source")
		if name == "" {
			return nil, errors.New("set_source_tables needs source")
		}
		blocked, err := tableList(payload.vals["blockedTables"], "blockedTables")
		if err != nil {
			return nil, err
		}
		admin, err := tableList(payload.vals["adminTables"], "adminTables")
		if err != nil {
			return nil, err
		}
		open, err := tableList(payload.vals["openTables"], "openTables")
		if err != nil {
			return nil, err
		}
		if err := assertDisjointTables([]string{"blockedTables", "adminTables", "openTables"}, blocked, admin, open); err != nil {
			return nil, err
		}
		if len(blocked)+len(admin)+len(open) == 0 {
			return nil, errors.New("set_source_tables needs blockedTables, adminTables or openTables")
		}
		access := &jsObj{}
		if len(blocked) > 0 {
			access.set("blocked_tables_add", stringsToAny(blocked))
		}
		if len(admin) > 0 {
			access.set("admin_tables_add", stringsToAny(admin))
		}
		if len(open) > 0 {
			access.set("blocked_tables_remove", stringsToAny(open))
			access.set("admin_tables_remove", stringsToAny(open))
		}
		update.set("source_patches", []any{jsObjOf("name", name, "access", access)})
		return update, nil
	case "set_source_capabilities":
		name := source("source")
		if name == "" {
			return nil, errors.New("set_source_capabilities needs source")
		}
		capabilities := &jsObj{}
		if v, ok := payload.vals["apiWrite"].(bool); ok {
			capabilities.set("api.write", v)
		}
		if v, ok := payload.vals["apiDelete"].(bool); ok {
			capabilities.set("api.delete", v)
		}
		if len(capabilities.keys) == 0 {
			return nil, errors.New("set_source_capabilities needs apiWrite or apiDelete")
		}
		update.set("update_sources", []any{jsObjOf("name", name, "capabilities", capabilities)})
		return update, nil
	case "expose_api_operation":
		name, spec, operation := source("source"), source("spec"), source("operation")
		if name == "" || spec == "" || operation == "" {
			return nil, errors.New("expose_api_operation needs source, spec, and operation")
		}
		expose := payload.vals["exposeMutation"] == true
		roles := lowerRoles(payload.vals["allowedRoles"])
		if expose && len(roles) == 0 {
			return nil, errors.New("expose_api_operation needs allowedRoles when exposeMutation is true")
		}
		update.set("update_sources", []any{jsObjOf("name", name, "specs", specsPatch(spec, operation, operationPatch(payload, expose, roles)))})
		return update, nil
	case "enable_api_writes":
		name, spec, operation := source("source"), source("spec"), source("operation")
		if name == "" || spec == "" || operation == "" {
			return nil, errors.New("enable_api_writes needs source, spec, and operation")
		}
		roles := lowerRoles(payload.vals["allowedRoles"])
		if len(roles) == 0 {
			return nil, errors.New("enable_api_writes needs allowedRoles")
		}
		write := "authenticated"
		if v := payload.vals["write"]; v != nil {
			write = jsTemplate(v)
		}
		if write != "authenticated" && write != "admin" {
			return nil, errors.New("enable_api_writes write must be authenticated or admin")
		}
		capabilities := jsObjOf("api.write", true)
		if payload.vals["apiDelete"] == true {
			capabilities.set("api.delete", true)
		}
		// Packs ship API sources with read_only: true, and GraphJin checks
		// that flag before capabilities, access and allowed_roles.
		update.set("update_sources", []any{jsObjOf("name", name, "read_only", false, "capabilities", capabilities,
			"specs", specsPatch(spec, operation, operationPatch(payload, true, roles)))})
		update.set("source_patches", []any{jsObjOf("name", name, "access", jsObjOf("write", write))})
		return update, nil
	case "register_source":
		return registerSourceUpdate(payload, resolve, opts)
	}
	return nil, fmt.Errorf("unknown source config action \"%s\"", action)
}

func registerSourceUpdate(payload *jsObj, resolve func(string) (string, error), opts configUpdateOptions) (*jsObj, error) {
	name := jsTrim(jsStr(payload.vals["name"]))
	kind := selectedText(payload.vals["kind"], "database")
	if name == "" {
		return nil, errors.New("register_source needs name")
	}
	if kind != "database" && kind != "api" && kind != "file" {
		return nil, fmt.Errorf("register_source kind must be database, api, or file (received \"%s\")", kind)
	}
	source := jsObjOf("name", name, "kind", kind)
	copyText := func(payloadKey, configKey string) string {
		value := trimmedText(payload.vals[payloadKey])
		if value != "" {
			source.set(configKey, value)
		}
		return value
	}
	tableAccess := &jsObj{}
	switch kind {
	case "database":
		// GraphJin pins read_only at startup, so a later patch cannot lift it.
		source.set("read_only", true)
		typ := strings.ToLower(selectedText(payload.vals["type"], "postgres"))
		if !containsString(databaseSourceTypes, typ) {
			return nil, fmt.Errorf("register_source database type must be %s (received \"%s\")", strings.Join(databaseSourceTypes, ", "), typ)
		}
		source.set("type", typ)
		blocked, err := tableList(payload.vals["blockedTables"], "blockedTables")
		if err != nil {
			return nil, err
		}
		admin, err := tableList(payload.vals["adminTables"], "adminTables")
		if err != nil {
			return nil, err
		}
		if err := assertDisjointTables([]string{"blockedTables", "adminTables"}, blocked, admin); err != nil {
			return nil, err
		}
		if len(blocked) > 0 {
			tableAccess.set("blocked_tables", stringsToAny(blocked))
		}
		if len(admin) > 0 {
			tableAccess.set("admin_tables", stringsToAny(admin))
		}
		copyText("host", "host")
		if port := payload.vals["port"]; jsTruthy(port) {
			source.set("port", numberOf(port))
		}
		copyText("dbname", "dbname")
		copyText("user", "user")
		ref := trimmedText(payload.vals["secretRef"])
		if ref == "" {
			return nil, errPasswordRequired
		}
		if resolve == nil {
			return nil, errors.New("register_source secret resolution is unavailable")
		}
		password, err := resolve(ref)
		if err != nil {
			return nil, err
		}
		if password == "" {
			return nil, errPasswordRequired
		}
		source.set("password", password)
	case "api":
		if trimmedText(payload.vals["specAssetId"]) == "" {
			return nil, errors.New("register_source api needs a managed specAssetId")
		}
		dir := opts.managedOpenAPISpecsDir
		if dir == "" {
			dir = "/config/specs"
		}
		source.set("specs_dir", dir)
	case "file":
		if err := fileSourceFields(payload, name, source, copyText, opts); err != nil {
			return nil, err
		}
	}
	if kind == "file" {
		source.set("access", jsObjOf("read", "authenticated", "write", "blocked", "delete", "blocked"))
	} else {
		mode := func(key, fallback string) string {
			if v := payload.vals[key]; v != nil {
				return jsTemplate(v)
			}
			return fallback
		}
		access := jsObjOf("read", mode("read", "authenticated"), "write", mode("write", "blocked"), "delete", mode("delete", "blocked"))
		for _, k := range tableAccess.keys {
			access.set(k, tableAccess.vals[k])
		}
		source.set("access", access)
	}
	return jsObjOf("update_sources", []any{source}), nil
}

func fileSourceFields(payload *jsObj, name string, source *jsObj, copyText func(string, string) string, opts configUpdateOptions) error {
	if !managedSourceName.MatchString(name) {
		return errors.New("register_source file name must start with a letter and contain only letters, digits, and underscores (64 characters maximum)")
	}
	backend := selectedText(payload.vals["backend"], "")
	if backend != "" {
		source.set("backend", backend)
	}
	if backend != "local" && backend != "s3" && backend != "gcs" {
		return errors.New("register_source file needs backend local, s3, or gcs")
	}
	if trimmedText(payload.vals["root"]) != "" {
		return errors.New("register_source local file roots are managed by OpenNeko and cannot be supplied in a proposal")
	}
	if backend == "local" {
		if _, err := parseManagedFileSourceManifest(payload.vals["localFiles"], name); err != nil {
			return err
		}
		if opts.managedLocalFilesRoot == "" {
			return errors.New("register_source local file needs OpenNeko managed storage")
		}
		source.set("root", opts.managedLocalFilesRoot)
	} else {
		bucket := copyText("bucket", "bucket")
		if bucket == "" {
			return fmt.Errorf("register_source %s file needs bucket", backend)
		}
		if err := validateObjectStoreBucket(backend, bucket); err != nil {
			return err
		}
		prefix := copyText("prefix", "prefix")
		if strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "\x00") {
			return errors.New("register_source file prefix must be a relative object-key prefix")
		}
		if backend == "s3" {
			copyText("region", "region")
		}
		if endpoint := trimmedText(payload.vals["endpoint"]); endpoint != "" {
			if backend != "s3" {
				return errors.New("register_source file endpoint is supported only for S3")
			}
			clean, err := validateObjectStoreEndpoint(endpoint)
			if err != nil {
				return err
			}
			source.set("endpoint", clean)
		}
		if base := trimmedText(payload.vals["publicBaseUrl"]); base != "" {
			clean, err := validatePublicFileBaseURL(base)
			if err != nil {
				return err
			}
			source.set("public_base_url", clean)
		}
		if ttl := trimmedText(payload.vals["presignTtl"]); ttl != "" {
			if err := validateFilePresignTTL(ttl); err != nil {
				return err
			}
			source.set("presign_ttl", ttl)
		}
	}
	source.set("read_only", true)
	source.set("max_list_page_size", 500.0)
	source.set("capabilities", jsObjOf("files.list", true, "files.read", true, "files.write", false, "files.delete", false, "files.watch", false))
	return nil
}

func validateObjectStoreBucket(backend, bucket string) error {
	var valid bool
	if backend == "s3" {
		valid = s3Bucket.MatchString(bucket) && !strings.Contains(bucket, "..") && !ipv4Bucket.MatchString(bucket)
	} else {
		valid = gcsBucket.MatchString(bucket) && !strings.Contains(bucket, "..") && !strings.HasPrefix(bucket, "goog")
	}
	if !valid {
		return fmt.Errorf("register_source %s file has an invalid bucket name", backend)
	}
	return nil
}

func validateObjectStoreEndpoint(value string) (string, error) {
	u, ok := parseAbsoluteURL(value)
	if !ok {
		return "", errors.New("register_source file endpoint must be a valid HTTP(S) URL")
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return "", errors.New("register_source file endpoint must use HTTP or HTTPS")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("register_source file endpoint must not contain credentials, a query string, or a fragment")
	}
	return strings.TrimSuffix(whatwgHref(u), "/"), nil
}

func validatePublicFileBaseURL(value string) (string, error) {
	u, ok := parseAbsoluteURL(value)
	if !ok {
		return "", errors.New("register_source file public base URL must be valid")
	}
	if strings.ToLower(u.Scheme) != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("register_source file public base URL must use HTTPS without credentials, a query string, or a fragment")
	}
	return strings.TrimSuffix(whatwgHref(u), "/"), nil
}

func validateFilePresignTTL(value string) error {
	m := presignDuration.FindStringSubmatch(value)
	if m == nil {
		return errors.New("register_source file presign TTL must be a duration such as 15m")
	}
	amount, err := strconv.ParseFloat(m[1], 64)
	seconds := amount * map[string]float64{"s": 1, "m": 60, "h": 3600}[m[2]]
	if err != nil || seconds > 1<<53-1 || seconds < 60 || seconds > 3600 {
		return errors.New("register_source file presign TTL must be between 1m and 1h")
	}
	return nil
}

// managedFileSourceRuntimeRoot is the folder GraphJin reads a managed local
// file source from.
func managedFileSourceRuntimeRoot(orgID, sourceName, runtimeBase string) (string, error) {
	base := strings.TrimRight(runtimeBase, "/")
	if !strings.HasPrefix(base, "/") {
		return "", errors.New("managed GraphJin file runtime directory must be absolute")
	}
	dir, err := managedSource{orgID: orgID, name: sourceName}.dirName()
	if err != nil {
		return "", err
	}
	return base + "/" + dir, nil
}

func parseManagedFileSourceManifest(v any, sourceName string) (*jsObj, error) {
	if !managedSourceName.MatchString(sourceName) {
		return nil, errors.New("register_source file name must start with a letter and contain only letters, digits, and underscores (64 characters maximum)")
	}
	input, ok := v.(*jsObj)
	if !ok {
		return nil, errors.New("register_source local file needs a managed file manifest")
	}
	if input.vals["sourceName"] != sourceName || input.vals["ready"] != true {
		return nil, errors.New("register_source local file manifest must match the selected source name")
	}
	list, ok := input.vals["files"].([]any)
	if !ok || len(list) < 1 || len(list) > maxManagedFileSourceCount {
		return nil, errors.New("managed file source manifest has an invalid file count")
	}
	names := map[string]bool{}
	total := 0.0
	files := make([]any, 0, len(list))
	for _, item := range list {
		entry, ok := item.(*jsObj)
		if !ok {
			return nil, errors.New("managed file source manifest has an invalid file entry")
		}
		name, err := safeManagedFileName(jsStr(entry.vals["name"]))
		if err != nil {
			return nil, err
		}
		if names[name] {
			return nil, errors.New("managed file source manifest has duplicate names")
		}
		names[name] = true
		size, ok := entry.vals["size"].(float64)
		if !ok || size != math.Trunc(size) || size < 1 || size > maxManagedFileSize {
			return nil, fmt.Errorf("managed file \"%s\" has an invalid size", name)
		}
		checksum := jsStr(entry.vals["checksumSha256"])
		if !checksumPattern.MatchString(checksum) {
			return nil, fmt.Errorf("managed file \"%s\" has an invalid checksum", name)
		}
		supplied := jsStr(entry.vals["contentType"])
		if safeManifestContentType(supplied) != supplied {
			return nil, fmt.Errorf("managed file \"%s\" has an invalid content type", name)
		}
		total += size
		files = append(files, jsObjOf("name", name, "size", size, "contentType", supplied, "checksumSha256", checksum))
	}
	if total > maxManagedFileSourceSize || input.vals["totalSize"] != total {
		return nil, errors.New("managed file source manifest total does not match its files")
	}
	return jsObjOf("sourceName", sourceName, "ready", true, "files", files, "totalSize", total), nil
}

func safeManifestContentType(value string) string {
	if value == "" {
		value = "application/octet-stream"
	}
	value = cutUTF16(jsTrim(value), 200)
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "application/octet-stream"
	}
	return value
}

// graphjinInputWithJSONVariables writes a value in GraphJin's inline input
// syntax. An object with a key that is not a GraphQL name goes in a JSON
// variable.
func graphjinInputWithJSONVariables(v any) (literal, definitions string, variables *jsObj) {
	variables = &jsObj{}
	sequence := 0
	var encode func(any) string
	encode = func(item any) string {
		switch t := item.(type) {
		case nil:
			return "null"
		case string:
			return jsStringify(t, false)
		case float64:
			return jsNumberText(t)
		case bool:
			return strconv.FormatBool(t)
		case []any:
			parts := make([]string, len(t))
			for i, entry := range t {
				parts[i] = encode(entry)
			}
			return "[" + strings.Join(parts, ", ") + "]"
		case *jsObj:
			keys := t.ordered()
			for _, k := range keys {
				if !graphqlNamePattern.MatchString(k) {
					sequence++
					name := "json" + strconv.Itoa(sequence)
					variables.set(name, t)
					return "$" + name
				}
			}
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = k + ": " + encode(t.vals[k])
			}
			return "{ " + strings.Join(parts, ", ") + " }"
		}
		return "null"
	}
	literal = encode(v)
	if len(variables.keys) > 0 {
		defs := make([]string, len(variables.keys))
		for i, name := range variables.keys {
			defs[i] = "$" + name + ": JSON!"
		}
		definitions = "(" + strings.Join(defs, ", ") + ")"
	}
	return literal, definitions, variables
}
