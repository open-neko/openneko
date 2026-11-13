package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/config"
)

type sourceConfigPreviewIn struct {
	OrgID        string  `json:"orgId"`
	RunID        *string `json:"runId"`
	DataSourceID *string `json:"dataSourceId,omitempty"`
	Payload      *jsObj  `json:"payload"`
}

// parseConfigPreviewJSON is GraphJin's JSON text parsed, the text cut to
// 4,000 characters when it does not parse, or null when it is empty.
func parseConfigPreviewJSON(value string) any {
	if value == "" {
		return nil
	}
	parsed, err := jsParse([]byte(value))
	if err != nil {
		return cutUTF16(value, 4000)
	}
	return parsed
}

// previewSourceConfigChange is InProcessControlPlane.previewSourceConfigChange():
// the answer checks, the config change, its description, GraphJin's own
// preview and the config file check, with the secret never in the result.
func (r *runTools) previewSourceConfigChange(ctx context.Context, in sourceConfigPreviewIn) (any, error) {
	gate, err := r.sourceConfigAccess(ctx, in.OrgID, in.RunID)
	if err != nil {
		return nil, err
	}
	if gate.denied != "" {
		return jsObjOf("denied", true, "error", gate.denied), nil
	}
	src, source, early, err := r.api.selectGraphjinSource(ctx, in.OrgID, in.DataSourceID,
		"multiple enabled data sources exist; choose a dataSourceId before previewing")
	if err != nil || early != nil {
		return early, err
	}
	if src.kind != "graphjin" {
		return jsObjOf("source", source, "error", "the selected data source is not a GraphJin server"), nil
	}
	if isInternalGraphjinURL(deref(src.graphqlURL)) {
		return jsObjOf("denied", true, "source", source, "error", "refusing to preview changes against the OpenNeko internal GraphJin"), nil
	}
	p := &configPreview{api: r.api, orgID: in.OrgID, userID: deref(gate.userID), endpoint: deref(src.graphqlURL), source: source, payload: in.Payload}
	if p.payload == nil {
		p.payload = &jsObj{}
	}
	result, err := p.run(ctx)
	if err == nil {
		return result, nil
	}
	if errors.Is(err, errPasswordRequired) {
		return jsObjOf("source", source, "problems", []any{jsObjOf("field", "secretRef", "message", err.Error())}, "error", err.Error()), nil
	}
	return jsObjOf("source", source, "error", cutUTF16(p.scrub(err.Error()), 1000)), nil
}

type configPreview struct {
	api              *API
	orgID, userID    string
	endpoint         string
	source           *jsObj
	payload          *jsObj
	secret           string
	token            string
	currentSources   any
	currentRoles     any
	catalogRevision  any
	specAsset        *openAPISpecAsset
	localFiles       *jsObj
	sourceName, kind string
}

func (p *configPreview) scrub(s string) string {
	if p.secret == "" {
		return s
	}
	return strings.ReplaceAll(s, p.secret, redactedValue)
}

func (p *configPreview) fail(message string) *jsObj {
	return jsObjOf("source", p.source, "error", message)
}

func (p *configPreview) problems(problems []configProblem, prefix string) *jsObj {
	return jsObjOf("source", p.source, "problems", jsValueOf(problems), "error", prefix+describeConfigProblems(problems))
}

// jsValueOf turns a Go value into the JS value JSON.parse would give.
func jsValueOf(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	out, _ := jsParse(raw)
	return out
}

func firstOf(v any) any {
	if list, ok := v.([]any); ok {
		if len(list) == 0 {
			return nil
		}
		return list[0]
	}
	return v
}

func (p *configPreview) run(ctx context.Context) (any, error) {
	action := jsStr(p.payload.vals["action"])
	p.kind, _ = firstOf(p.payload.vals["kind"]).(string)
	backend, _ := firstOf(p.payload.vals["backend"]).(string)
	p.sourceName = trimmedText(p.payload.vals["name"])
	if action == "register_source" && p.kind == "api" {
		id, _ := p.payload.vals["specAssetId"].(string)
		if id == "" {
			return p.fail("API source requires an imported OpenAPI specification"), nil
		}
		asset, err := scanOpenAPIAsset(p.api.DB.QueryRow(ctx, `select `+openAPIAssetColumns+`
			from openapi_spec_asset where org_id = $1 and id = $2::uuid limit 1`, p.orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return p.fail("OpenAPI specification is not available"), nil
		}
		if err != nil {
			return nil, err
		}
		p.specAsset = &asset
	}
	opts := configUpdateOptions{}
	if action == "register_source" && p.kind == "file" && backend == "local" {
		manifest, err := parseManagedFileSourceManifest(p.payload.vals["localFiles"], p.sourceName)
		if err != nil {
			return nil, err
		}
		p.localFiles = manifest
		base := strings.TrimSpace(os.Getenv("OPENNEKO_GRAPHJIN_FILES_DIR"))
		if base == "" {
			base = "/config/files"
		}
		if opts.managedLocalFilesRoot, err = managedFileSourceRuntimeRoot(p.orgID, p.sourceName, base); err != nil {
			return nil, err
		}
	}
	if p.specAsset != nil {
		opts.managedOpenAPISpecsDir = "/config/specs"
	}
	var err error
	if p.token, err = p.api.mintGraphjinToken(graphjinClaims{orgID: p.orgID, userID: p.userID, role: "admin"}, 120*time.Second); err != nil {
		return nil, err
	}
	data, messages, err := gjPost(ctx, p.endpoint, p.token, `query { gj_config(id: "current") { catalog_revision sources roles } }`)
	if err != nil {
		return nil, err
	}
	row, _ := jsGet(data, "gj_config").(*jsObj)
	if row == nil {
		row = &jsObj{}
	}
	p.catalogRevision = row.vals["catalog_revision"]
	if !jsTruthy(p.catalogRevision) || len(messages) > 0 {
		message := strings.Join(messages, "; ")
		if message == "" {
			message = "GraphJin did not return catalog_revision"
		}
		return p.fail(message), nil
	}
	p.currentSources = row.vals["sources"]
	if text, ok := p.currentSources.(string); ok {
		if p.currentSources, err = jsParse([]byte(text)); err != nil {
			p.currentSources = []any{}
		}
	}
	p.currentRoles = parseJSONColumn(row.vals["roles"])
	return p.check(ctx, action, opts)
}

func (p *configPreview) check(ctx context.Context, action string, opts configUpdateOptions) (any, error) {
	rows, err := p.api.DB.Query(ctx, `select name from data_source_secret where org_id = $1`, p.orgID)
	if err != nil {
		return nil, err
	}
	secretNames, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	tables := map[string][]string{}
	if tablesSource := trimmedText(p.payload.vals["source"]); action == "set_source_tables" && tablesSource != "" {
		data, messages, err := gjPostVars(ctx, p.endpoint, p.token,
			`query ($source: String!) { gj_catalog(where: { kind: { eq: "table" }, source: { eq: $source } }, limit: 5000) { table_name } }`,
			jsObjOf("source", tablesSource))
		if err != nil {
			return nil, err
		}
		if len(messages) == 0 {
			names := []string{}
			list, _ := jsGet(data, "gj_catalog").([]any)
			for _, item := range list {
				if name, ok := jsGet(item, "table_name").(string); ok && name != "" {
					names = append(names, name)
				}
			}
			tables[strings.ToLower(tablesSource)] = names
		}
	}
	if problems := checkSourceConfigAnswers(p.payload, answerCheckContext{secretNames: secretNames, hasSecretNames: true,
		currentSources: p.currentSources, sourceTables: tables}); len(problems) > 0 {
		return p.problems(problems, ""), nil
	}
	update, err := buildGraphjinConfigUpdate(p.payload, func(name string) (string, error) {
		var enc string
		err := p.api.DB.QueryRow(ctx, `select value_enc from data_source_secret where org_id = $1 and name = $2 limit 1`, p.orgID, name).Scan(&enc)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("no stored source secret named \"%s\"; add it in GraphJin Config first", name)
		}
		if err != nil {
			return "", err
		}
		if p.secret, err = config.MaybeDecryptValue(p.api.SecretsDir, enc); err != nil {
			return "", err
		}
		return p.secret, nil
	}, opts)
	if err != nil {
		return nil, err
	}
	// Only the typed, credential-free proposal is hashed: even a plain
	// SHA-256 of a low-entropy password must stay out of the approval.
	patchHash := graphjinConfigPatchHash(p.payload)
	if err := assertDatabaseSourcesStayReadOnly(update, p.currentSources); err != nil {
		return nil, err
	}
	if action == "register_source" {
		list, _ := p.currentSources.([]any)
		for _, item := range list {
			if o, ok := item.(*jsObj); ok && o.vals["name"] == p.sourceName {
				return p.fail(fmt.Sprintf("a GraphJin source named \"%s\" already exists", p.sourceName)), nil
			}
		}
	}
	var summary *jsObj
	if described, err := describeGraphjinConfigChange(p.currentSources, p.currentRoles, update); err == nil {
		yamlPart := described.vals["yaml"].(*jsObj)
		described.set("yaml", jsObjOf("before", p.scrub(yamlPart.vals["before"].(string)), "after", p.scrub(yamlPart.vals["after"].(string))))
		summary = described
	}
	preview := func(pairs ...any) *jsObj {
		out := jsObjOf(append([]any{"valid", true, "patchHash", patchHash, "catalogRevision", p.catalogRevision}, pairs...)...)
		if summary != nil {
			out.set("summary", summary)
		}
		return out
	}
	// A managed file is written only after approval; the worker runs
	// GraphJin's own preview on the staged folder.
	if a := p.specAsset; a != nil {
		return jsObjOf("source", p.source, "specAsset", jsValueOf(a), "preview", preview("expiresAt", nil, "scope", "managed_openapi",
			"reloadMode", "staged_reload", "changes", jsObjOf("action", "register_source", "source", p.sourceName, "kind", "api",
				"title", a.Title, "version", jsValueOf(a.Version), "baseUrl", a.BaseURL, "operationCount", float64(a.OperationCount),
				"authSchemes", jsValueOf(a.AuthSchemes), "checksumSha256", a.ChecksumSha256),
			"findings", jsValueOf(a.Warnings), "errors", nil)), nil
	}
	if m := p.localFiles; m != nil {
		files, _ := m.vals["files"].([]any)
		return jsObjOf("source", p.source, "preview", preview("expiresAt", nil, "scope", "managed_local_files",
			"reloadMode", "staged_reload", "changes", jsObjOf("action", "register_source", "source", p.sourceName, "kind", "file",
				"backend", "local", "fileCount", float64(len(files)), "totalSize", m.vals["totalSize"], "readOnly", true, "access", "authenticated"),
			"findings", []any{}, "errors", nil)), nil
	}
	return p.graphjinPreview(ctx, update, patchHash, summary)
}

func (p *configPreview) graphjinPreview(ctx context.Context, update *jsObj, patchHash string, summary *jsObj) (any, error) {
	input := jsObjOf("mode", "preview", "expected_catalog_revision", p.catalogRevision)
	for _, k := range update.ordered() {
		input.set(k, update.vals[k])
	}
	literal, definitions, variables := graphjinInputWithJSONVariables(input)
	data, messages, err := gjPostVars(ctx, p.endpoint, p.token,
		`mutation`+definitions+` { gj_config(id: "current", update: `+literal+`) { valid preview_id expires_at change_summary_json findings_json errors_json } }`,
		variables)
	if err != nil {
		return nil, err
	}
	if len(messages) > 0 {
		return p.fail(p.scrub(strings.Join(messages, "; "))), nil
	}
	row, _ := jsGet(data, "gj_config").(*jsObj)
	if row == nil {
		row = &jsObj{}
	}
	text := func(key string) string { return p.scrub(jsStr(row.vals[key])) }
	valid := row.vals["valid"] == true && jsTruthy(row.vals["preview_id"])
	errs := parseConfigPreviewJSON(text("errors_json"))
	expires := row.vals["expires_at"]
	preview := jsObjOf("valid", valid, "patchHash", patchHash, "catalogRevision", p.catalogRevision, "expiresAt", expires,
		"scope", nil, "reloadMode", nil, "changes", parseConfigPreviewJSON(text("change_summary_json")),
		"findings", parseConfigPreviewJSON(text("findings_json")), "errors", errs)
	if summary != nil {
		preview.set("summary", summary)
	}
	if !valid {
		out := jsObjOf("source", p.source, "preview", preview)
		if problems := mapGraphjinConfigErrors(errs, p.payload); len(problems) > 0 {
			out.set("problems", jsValueOf(problems))
			out.set("error", describeConfigProblems(problems))
		}
		return out, nil
	}
	fileCheck, err := checkGraphjinConfigCandidate(ctx, os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"), os.Getenv("OPENNEKO_CONFIG_CHECK_SOCKET"), update)
	if err != nil {
		return nil, err
	}
	if fileCheck.vals["checked"] == true {
		list, _ := fileCheck.vals["errors"].([]any)
		scrubbed := make([]any, len(list))
		for i, item := range list {
			scrubbed[i] = p.scrub(jsStr(item))
		}
		fileCheck.set("errors", scrubbed)
		if fileCheck.vals["valid"] != true {
			problems := mapGraphjinConfigErrors(scrubbed, p.payload)
			failed := jsSpread(preview)
			failed.set("valid", false)
			failed.set("errors", scrubbed)
			failed.set("fileCheck", fileCheck)
			out := p.problems(problems, "The config file would not load in GraphJin. ")
			out.set("preview", failed)
			return out, nil
		}
	} else {
		fileCheck.set("reason", p.scrub(jsStr(fileCheck.vals["reason"])))
	}
	preview.set("fileCheck", fileCheck)
	return jsObjOf("source", p.source, "preview", preview), nil
}
