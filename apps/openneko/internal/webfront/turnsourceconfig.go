package webfront

import (
	"context"
	_ "embed"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
)

type sourceConfigIn struct {
	OrgID string  `json:"orgId"`
	RunID *string `json:"runId"`
}

type sourceConfigDescribeIn struct {
	OrgID        string  `json:"orgId"`
	RunID        *string `json:"runId"`
	DataSourceID *string `json:"dataSourceId,omitempty"`
}

type sourceCheckIn struct {
	OrgID   string  `json:"orgId"`
	RunID   *string `json:"runId"`
	Answers *jsObj  `json:"answers"`
}

type openAPIImportIn struct {
	OrgID   string  `json:"orgId"`
	RunID   *string `json:"runId"`
	URL     string  `json:"url"`
	BaseURL *string `json:"baseUrl,omitempty"`
}

type openAPIListIn struct {
	OrgID string  `json:"orgId"`
	RunID *string `json:"runId"`
	Limit *int    `json:"limit,omitempty"`
}

var databaseSourceTypes = []string{"postgres", "mysql", "mariadb"}

var (
	secretNamePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)
	sourceTableName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*)?$`)
	graphqlNamePattern = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)
)

func optionalText(max float64) *zStr { return &zStr{trim: true, max: num(max)} }

var sourceFormPrefillShape = &zObj{fields: []zField{
	zOptional("name", optionalText(64)),
	zOptional("kind", zEnum("", "database", "api", "file")),
	zOptional("type", zEnum("", databaseSourceTypes...)),
	zOptional("host", optionalText(255)),
	zOptional("port", zIntRange(1, 65535)),
	zOptional("dbname", optionalText(128)),
	zOptional("user", optionalText(128)),
	{name: "secretRef", typ: optionalText(128), optional: true,
		desc: "A stored secret name from list_source_secret_names. Leave it out to let the admin type the password in the card."},
	{name: "blockedTables", typ: optionalText(2000), optional: true, desc: "Comma-separated tables that no one may read."},
	{name: "adminTables", typ: optionalText(2000), optional: true, desc: "Comma-separated tables that only admins may read."},
	zOptional("backend", zEnum("", "local", "s3", "gcs")),
	zOptional("bucket", optionalText(255)),
	zOptional("prefix", optionalText(255)),
	zOptional("region", optionalText(64)),
	zOptional("endpoint", optionalText(255)),
	zOptional("publicBaseUrl", optionalText(255)),
	zOptional("presignTtl", optionalText(16)),
}}

var sourceCheckShape = &zObj{fields: []zField{
	zRequired("type", zEnum("", databaseSourceTypes...)),
	zRequired("host", zText(1, 255)),
	zOptional("port", zIntRange(1, 65535)),
	zRequired("dbname", zText(1, 128)),
	zRequired("user", zText(1, 128)),
	zRequired("secretRef", &zStr{trim: true, min: num(1), max: num(128), pattern: secretNamePattern,
		patternMsg: "a secret name, not a value", lengthFirst: true}),
}}

var packIDShape = &zStr{trim: true, pattern: packIDPattern, patternMsg: "a pack ID such as magento", max: num(64)}

func tableNameList() zType {
	return &zArr{elem: &zStr{trim: true, pattern: sourceTableName, patternMsg: "a table name", max: num(128)}, max: num(500)}
}

func writeModes() *zStr { return zEnum("", "authenticated", "account", "owner", "admin", "blocked") }

var sourceConfigChangeShape = &zObj{fields: []zField{
	zRequired("action", zEnum("", "add_role", "set_source_access", "set_source_tables", "set_source_capabilities",
		"expose_api_operation", "enable_api_writes", "register_source")),
	zOptional("name", zText(1, 64)),
	zOptional("match", zText(1, 500)),
	zOptional("source", zText(1, 64)),
	zOptional("read", zEnum("", "public", "authenticated", "account", "owner", "admin", "blocked")),
	zOptional("write", writeModes()),
	zOptional("delete", writeModes()),
	zOptional("blockedTables", tableNameList()),
	zOptional("adminTables", tableNameList()),
	zOptional("openTables", tableNameList()),
	zOptional("apiWrite", zPrim("boolean")),
	zOptional("apiDelete", zPrim("boolean")),
	zOptional("spec", zText(1, 128)),
	zOptional("operation", zText(1, 200)),
	zOptional("exposeMutation", zPrim("boolean")),
	zOptional("allowedRoles", &zArr{elem: zEnum("", "admin", "member", "service", "user"), max: num(4)}),
	zOptional("exposeAs", &zStr{trim: true, pattern: graphqlNamePattern, patternMsg: "a GraphQL field name", max: num(120)}),
	zOptional("kind", zOneOrList{zEnum("", "database", "api", "file")}),
	zOptional("type", zOneOrList{zEnum("", databaseSourceTypes...)}),
	zOptional("host", optionalText(255)),
	zOptional("port", zIntRange(1, 65535)),
	zOptional("dbname", optionalText(128)),
	zOptional("user", optionalText(128)),
	zOptional("secretRef", &zStr{trim: true, max: num(128), pattern: secretNamePattern,
		patternMsg: "a secret name, not a value", lengthFirst: true}),
	zOptional("specAssetId", &zStr{uuid: true}),
	zOptional("backend", zOneOrList{zEnum("", "local", "s3", "gcs")}),
	zOptional("bucket", optionalText(255)),
	zOptional("prefix", optionalText(500)),
	zOptional("region", optionalText(100)),
	zOptional("endpoint", &zStr{url: true, max: num(1000)}),
	zOptional("publicBaseUrl", &zStr{url: true, max: num(1000)}),
	zOptional("presignTtl", optionalText(40)),
	zOptional("localFiles", &zObj{fields: []zField{
		zRequired("sourceName", zText(1, 64)),
		zRequired("ready", zLiteral(true)),
		zRequired("files", &zArr{min: num(1), max: num(500), elem: &zObj{fields: []zField{
			zRequired("name", zText(1, 128)),
			zRequired("size", zIntRange(1, 10*1024*1024)),
			zRequired("contentType", zText(1, 200)),
			zRequired("checksumSha256", &zStr{pattern: regexp.MustCompile(`^[a-f0-9]{64}$`)}),
		}}}),
		zRequired("totalSize", zIntRange(1, 100*1024*1024)),
	}}),
	zOptional("dataSourceId", &zStr{uuid: true}),
	intentField,
}}

//go:embed sourceform.json
var sourceFormComponentsJSON []byte

var sourceFormComponents = sync.OnceValue(func() any {
	v, err := jsParse(sourceFormComponentsJSON)
	if err != nil {
		panic(err)
	}
	return v
})

// sourceFormSurface is buildSourceFormSurface() in
// packages/llm/src/work/source-form.ts. prefill holds parsed tool arguments.
func sourceFormSurface(prefill *jsObj, surfaceID string, problems any) []any {
	pick := func(key, fallback string) []any {
		if v, ok := prefill.vals[key].(string); ok {
			return []any{v}
		}
		return []any{fallback}
	}
	form := jsObjOf("name", "", "kind", pick("kind", "database"), "type", pick("type", "postgres"),
		"host", "", "port", 5432.0, "dbname", "", "user", "", "secretRef", "", "blockedTables", "", "adminTables", "",
		"openApiSpec", nil, "backend", pick("backend", "local"), "localFiles", nil, "bucket", "", "prefix", "",
		"region", "", "endpoint", "", "publicBaseUrl", "", "presignTtl", "15m")
	for _, k := range prefill.ordered() {
		if k == "kind" || k == "type" || k == "backend" {
			continue
		}
		if _, known := form.vals[k]; known {
			form.set(k, prefill.vals[k])
		}
	}
	return []any{jsObjOf("version", a2uiVersion, "createSurface", jsObjOf("surfaceId", surfaceID, "catalogId", a2uiCatalogID,
		"dataModel", jsObjOf("form", form, "problems", problemsByField(problems)), "components", sourceFormComponents()))}
}

var sourceFormFields = []string{"name", "kind", "type", "host", "port", "dbname", "user", "secretRef",
	"blockedTables", "adminTables", "backend", "bucket", "prefix", "region", "endpoint", "publicBaseUrl", "presignTtl"}

func problemsByField(problems any) *jsObj {
	out := &jsObj{}
	var general []string
	list, _ := problems.([]any)
	for _, item := range list {
		message := jsStr(jsGet(item, "message"))
		field, ok := jsGet(item, "field").(string)
		if !ok || !containsString(sourceFormFields, field) {
			general = append(general, message)
			continue
		}
		if prior, ok := out.vals[field].(string); ok && prior != "" {
			message = prior + " " + message
		}
		out.set(field, message)
	}
	if len(general) > 0 {
		out.set("general", strings.Join(general, " "))
	}
	return out
}

// sourceFormPrefillFromAnswers turns the answers of a register_source
// proposal back into the form's values.
func sourceFormPrefillFromAnswers(answers *jsObj) *jsObj {
	prefill := &jsObj{}
	for _, key := range []string{"name", "host", "dbname", "user", "secretRef", "bucket", "prefix", "region", "endpoint", "publicBaseUrl", "presignTtl"} {
		if s, ok := answers.vals[key].(string); ok {
			prefill.set(key, s)
		}
	}
	for _, key := range []string{"kind", "type", "backend"} {
		v := answers.vals[key]
		if list, ok := v.([]any); ok && len(list) == 1 {
			v = list[0]
		}
		if s, ok := v.(string); ok {
			prefill.set(key, s)
		}
	}
	if port, ok := answers.vals["port"].(float64); ok {
		prefill.set("port", port)
	}
	for _, key := range []string{"blockedTables", "adminTables"} {
		if list, ok := answers.vals[key].([]any); ok && len(list) > 0 {
			parts := make([]string, len(list))
			for i, item := range list {
				parts[i] = jsStr(item)
			}
			prefill.set(key, strings.Join(parts, ", "))
		}
	}
	return prefill
}

// packSetupSurface is buildPackSetupSurface().
func packSetupSurface(packID, surfaceID string) []any {
	return []any{jsObjOf("version", a2uiVersion, "createSurface", jsObjOf("surfaceId", surfaceID, "catalogId", a2uiCatalogID,
		"dataModel", &jsObj{}, "components", []any{
			jsObjOf("id", "root", "component", "Answer", "eyebrow", "PACK SETUP", "title", "Connect with a pack",
				"subtitle", "The pack holds a ready config. Enter the address and the login.", "children", []any{"setup"}),
			jsObjOf("id", "setup", "component", "PackSetup", "packId", packID),
		}))}
}

func sourceConfigCall(ctx context.Context, be toolBackend, path string, call func() (any, error)) (toolResult, error) {
	be.audit(ctx, path)
	result, err := call()
	if err != nil {
		return toolResult{}, err
	}
	return toolText(result), nil
}

// checkSourceResult is the check_source tool's answer: at most 200 tables,
// the rest counted.
func checkSourceResult(response any) any {
	check, ok := jsField(response, "check")
	if !ok || !jsTruthy(check) {
		return response
	}
	okValue, _ := jsField(check, "ok")
	version, _ := jsField(check, "serverVersion")
	findings, _ := jsField(check, "findings")
	more, _ := jsField(check, "moreTables")
	tablesValue, _ := jsField(check, "tables")
	tables, _ := tablesValue.([]any)
	moreCount, _ := more.(float64)
	moreCount += float64(max(0, len(tables)-200))
	next := "Tell the admin which step failed and why, in one or two sentences. When a finding has `sql`, show it as a code block for their database administrator."
	if jsTruthy(okValue) {
		next = "Tell the admin the check passed. In the form card, the admin chooses the tables under Check connection; cleared tables become blockedTables."
	}
	return jsObjOf("ok", okValue, "serverVersion", version, "findings", findings,
		"tables", tables[:min(len(tables), 200)], "moreTables", moreCount, "next", next)
}

// sourceConfigTools is buildSourceConfigManagerServer().
func sourceConfigTools(b turnBinding, be toolBackend) []toolDef {
	const server = "neko_source_config_manager"
	in := sourceConfigIn{OrgID: b.OrgID, RunID: b.runID()}
	return []toolDef{
		{server: server, name: "describe_source_graph", shape: &zObj{},
			description: joinWords("Return the selected GraphJin data engine's live source graph, including",
				"its discovered databases, capabilities, and namespaces. Use the result",
				"to answer source questions and to prepare configuration requests."),
			call: func(ctx context.Context, _ *jsObj) (toolResult, error) {
				return sourceConfigCall(ctx, be, "/v1/source-graph/describe", func() (any, error) { return be.describeSourceGraph(ctx, in) })
			}},
		{server: server, name: "describe_source_config",
			shape: &zObj{fields: []zField{zOptional("dataSourceId", &zStr{uuid: true})}},
			description: joinWords("Return the selected GraphJin data engine's current config: sources,",
				"roles, databases, tables, blocklist, system, mcp and serv settings,",
				"with every secret redacted, and `summary`: plain sentences per source",
				"and role. Explain the config to the admin from `summary`. Use `config`",
				"to plan a change. For multiple data sources, pass the selected dataSourceId."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				req := sourceConfigDescribeIn{OrgID: b.OrgID, RunID: b.runID()}
				if id, ok := args.vals["dataSourceId"].(string); ok {
					req.DataSourceID = &id
				}
				return sourceConfigCall(ctx, be, "/v1/source-config/describe", func() (any, error) { return be.describeSourceConfig(ctx, req) })
			}},
		{server: server, name: "list_source_secret_names", shape: &zObj{},
			description: joinWords("Return stored connection-secret names for use as `secretRef` values in",
				"a register_source proposal. When the required name is absent, direct",
				"the admin to Admin > Settings > GraphJin Config to add it."),
			call: func(ctx context.Context, _ *jsObj) (toolResult, error) {
				return sourceConfigCall(ctx, be, "/v1/source-secrets/names", func() (any, error) { return be.listSourceSecretNames(ctx, in) })
			}},
		{server: server, name: "present_source_form", shape: sourceFormPrefillShape,
			description: joinWords("Show the admin the source configuration form for a new or changed",
				"Database, API or Files source. Pass any values you already know; the",
				"admin completes the rest and submits it for review. The submitted values",
				"arrive in the next turn for request_source_config_change."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				if err := be.emitEvent(ctx, jsObjOf("type", "surface", "messages", sourceFormSurface(args, "source-form-"+uuid.NewString(), nil))); err != nil {
					return toolResult{}, err
				}
				return toolResult{text: "Form shown."}, nil
			}},
		{server: server, name: "check_source", shape: sourceCheckShape,
			description: joinWords("Test one PostgreSQL, MySQL or MariaDB source before you file it, or when",
				"the admin says a source fails. The check runs in order: the host",
				"resolves, the port answers, TLS, the login, the rights to read the",
				"schema, and read-only rights. Each failed step names the answer at fault",
				"and, where SQL fixes it, the SQL for the database administrator. When",
				"the login works, `tables` lists the tables it can read. The password",
				"comes from the stored secret named by secretRef and never reaches you."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				be.audit(ctx, "/v1/source-check")
				response, err := be.checkSource(ctx, sourceCheckIn{OrgID: b.OrgID, RunID: b.runID(), Answers: args})
				if err != nil {
					return toolResult{}, err
				}
				return toolText(checkSourceResult(response)), nil
			}},
		{server: server, name: "present_pack_setup",
			shape: &zObj{fields: []zField{zRequired("packId", packIDShape)}},
			description: joinWords("Show the admin a pack's setup card in chat, for a system that has a pack",
				"with a ready config (magento, shopify, google-workspace, slack, resend,",
				"parallel-search, or an uploaded pack the admin names). The card installs",
				"the pack, asks for the address and the login, shows the review, and",
				"applies it when the admin approves. Credentials go from the card to the",
				"pack store and never reach you. A message arrives when the admin applies it."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				if err := be.emitEvent(ctx, jsObjOf("type", "surface", "messages",
					packSetupSurface(stringArg(args, "packId"), "pack-setup-"+uuid.NewString()))); err != nil {
					return toolResult{}, err
				}
				return toolText(jsObjOf("ok", true, "next", "Card shown. Tell the admin to enter the address and the login in the card, then review and approve it. Wait for their message. Do not file a register_source proposal for this system.")), nil
			}},
		{server: server, name: "import_openapi_spec",
			shape: &zObj{fields: []zField{
				zRequired("url", &zStr{url: true, max: num(2000)}),
				zOptional("baseUrl", &zStr{url: true, max: num(2000)}),
			}},
			description: joinWords("Import and validate a hosted OpenAPI 3.x YAML or JSON document for",
				"the current organization. Use this when an admin supplies an HTTPS",
				"URL in chat. The trusted host fetches it and returns a managed asset ID."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				req := openAPIImportIn{OrgID: b.OrgID, RunID: b.runID(), URL: stringArg(args, "url")}
				if base, ok := args.vals["baseUrl"].(string); ok {
					req.BaseURL = &base
				}
				return sourceConfigCall(ctx, be, "/v1/openapi/import", func() (any, error) { return be.importOpenAPISpec(ctx, req) })
			}},
		{server: server, name: "list_openapi_specs",
			shape:       &zObj{fields: []zField{zOptional("limit", zIntRange(1, 100))}},
			description: "List this organization's recently imported OpenAPI asset metadata and IDs.",
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				req := openAPIListIn{OrgID: b.OrgID, RunID: b.runID(), Limit: optionalInt(args, "limit")}
				return sourceConfigCall(ctx, be, "/v1/openapi/list", func() (any, error) { return be.listOpenAPISpecs(ctx, req) })
			}},
		{server: server, name: "request_source_config_change", shape: sourceConfigChangeShape,
			description: strings.Join([]string{
				"Create a source_config_admin proposal for admin review:",
				"- add_role { name, match }: a GraphJin role selected by a JWT match expression.",
				"- set_source_access { source, read, write, delete }: access mode per source (one of public|authenticated|account|owner|admin|blocked; write/delete also accept blocked).",
				"- enable_api_writes { source, spec, operation, allowedRoles, write?, exposeAs?, apiDelete? }: one proposal that turns on api.write, sets access.write (authenticated by default, or admin), and exposes one OpenAPI POST/PUT/PATCH/DELETE operation as a GraphQL mutation for the listed roles. Prefer this for governed writes.",
				"- set_source_capabilities { source, apiWrite?, apiDelete? } and expose_api_operation { source, spec, operation, exposeMutation, allowedRoles, exposeAs? }: adjust one write setting on an API source that already has writes enabled.",
				"Database sources stay read-only; a proposal that opens a write path on one is rejected.",
				"- set_source_tables { source, blockedTables?, adminTables?, openTables? }: per-table access on a database source. blockedTables: no one may read them. adminTables: only admins may read them. openTables: back to the source's read mode.",
				"- register_source database { name, kind, type?, host?, port?, dbname?, user?, secretRef?, blockedTables?, adminTables? }. type is postgres, mysql or mariadb.",
				"- register_source api { name, kind, specAssetId } using an OpenAPI document already imported by URL or upload.",
				"- register_source file local { name, kind, backend, localFiles } using OpenNeko-managed isolated storage.",
				"- register_source file s3|gcs { name, kind, backend, bucket, prefix?, region?, endpoint?, publicBaseUrl?, presignTtl? } using deployment runtime credentials.",
				"File sources are provisioned read-only for authenticated organization members.",
				"After the tool returns, summarize the proposed change and its approval status.",
			}, "\n"),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				return requestSourceConfigChange(ctx, b, be, args)
			}},
	}
}

func sourceConfigTarget(action string, args *jsObj) string {
	text := func(key string) string { s, _ := args.vals[key].(string); return s }
	switch action {
	case "add_role":
		return "role:" + text("name")
	case "set_source_access":
		return "access:" + text("source")
	case "set_source_tables":
		return "tables:" + text("source")
	case "set_source_capabilities":
		return "capabilities:" + text("source")
	case "expose_api_operation", "enable_api_writes":
		return "operation:" + text("source") + "/" + text("spec") + "#" + text("operation")
	}
	return "source:" + text("name")
}

// requestSourceConfigChange previews the change and, when the preview is
// valid, files a source_config_admin proposal. The payload holds a secret's
// name only; the worker reads the value at apply time.
func requestSourceConfigChange(ctx context.Context, b turnBinding, be toolBackend, args *jsObj) (toolResult, error) {
	action, _ := args.vals["action"].(string)
	intent, _ := args.vals["intent"].(string)
	dataSourceID, _ := args.vals["dataSourceId"].(string)
	rest := &jsObj{}
	for _, k := range args.ordered() {
		if k != "action" && k != "intent" && k != "dataSourceId" {
			rest.set(k, args.vals[k])
		}
	}
	payload := jsObjOf("action", action)
	for _, k := range rest.ordered() {
		payload.set(k, rest.vals[k])
	}
	in := sourceConfigPreviewIn{OrgID: b.OrgID, RunID: b.runID(), Payload: payload}
	if dataSourceID != "" {
		payload.set("dataSourceId", dataSourceID)
		in.DataSourceID = &dataSourceID
	}
	be.audit(ctx, "/v1/source-config/preview")
	preview, err := be.previewSourceConfigChange(ctx, in)
	if err != nil {
		return toolResult{}, err
	}
	denied, _ := jsField(preview, "denied")
	failure, _ := jsField(preview, "error")
	inner, _ := jsField(preview, "preview")
	valid, _ := jsField(inner, "valid")
	if jsTruthy(denied) || jsTruthy(failure) || !jsTruthy(valid) {
		problemsValue, _ := jsField(preview, "problems")
		problems, _ := problemsValue.([]any)
		formShown := false
		if len(problems) > 0 && action == "register_source" {
			issues := []any{}
			prefill := sourceFormPrefillShape.parse(sourceFormPrefillFromAnswers(rest), []any{}, &issues)
			if len(issues) == 0 {
				formShown = be.emitEvent(ctx, surfaceEvent(sourceFormSurface(prefill.(*jsObj), "source-form-"+uuid.NewString(), problems))) == nil
			}
		}
		stage := "graphjin_config_preview"
		if len(problems) > 0 {
			stage = "answer_check"
		}
		out := jsObjOf("ok", false, "stage", stage)
		spread := jsSpread(preview)
		for _, k := range spread.ordered() {
			out.set(k, spread.vals[k])
		}
		switch {
		case formShown:
			out.set("next", "The form is shown again with each problem beside its answer. Tell the admin what to fix, in one or two sentences, and wait for the new answers.")
		case len(problems) > 0:
			out.set("next", "Tell the admin which answer to fix and why, using the problem messages, then ask for the new answer.")
		}
		return toolText(out), nil
	}
	proposal := jsSpread(payload)
	if spec, ok := jsField(preview, "specAsset"); ok && jsTruthy(spec) {
		proposal.set("specSummary", spec)
	}
	summary := &jsObj{}
	if source, ok := jsField(preview, "source"); ok {
		summary.set("source", source)
	}
	for _, k := range jsSpread(inner).ordered() {
		summary.set(k, inner.(*jsObj).vals[k])
	}
	proposal.set("preview", summary)
	return b.proposeAdmin(ctx, be, "source_config_admin", sourceConfigTarget(action, args), intent, proposal)
}
