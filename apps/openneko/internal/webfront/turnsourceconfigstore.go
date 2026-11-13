package webfront

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
)

type sourceConfigGate struct {
	denied string
	userID *string
}

// sourceConfigAccess is requireSourceConfigAccess() in
// packages/llm/src/work/control-plane.ts.
func (r *runTools) sourceConfigAccess(ctx context.Context, orgID string, runID *string) (sourceConfigGate, error) {
	db := r.api.DB
	on, err := r.api.sourceConfigEnabled(ctx, orgID)
	if err != nil {
		return sourceConfigGate{}, err
	}
	if !on {
		return sourceConfigGate{denied: "GraphJin config capability is disabled."}, nil
	}
	var role string
	var userID *string
	if runID != nil {
		var runOrg string
		var snapshot *string
		err := db.QueryRow(ctx, `select org_id, actor_user_id, actor_role from work_run where id::text = $1 limit 1`, *runID).
			Scan(&runOrg, &userID, &snapshot)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && runOrg != orgID {
			return sourceConfigGate{denied: "GraphJin config run is not available."}, nil
		}
		if err != nil {
			return sourceConfigGate{}, err
		}
		if userID != nil {
			var disabled *time.Time
			err := db.QueryRow(ctx, `select disabled_at from app_user where id = $1 and org_id = $2 limit 1`, *userID, orgID).Scan(&disabled)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return sourceConfigGate{}, err
			}
			if err == nil && disabled == nil {
				if role, err = r.api.role(ctx, orgID, *userID); err != nil {
					return sourceConfigGate{}, err
				}
			}
		} else {
			var anyUser bool
			if err := db.QueryRow(ctx, `select exists (select 1 from app_user where org_id = $1)`, orgID).Scan(&anyUser); err != nil {
				return sourceConfigGate{}, err
			}
			if !anyUser {
				role = deref(snapshot)
			}
		}
	}
	if role != "admin" {
		return sourceConfigGate{denied: "GraphJin config tools are admin-only."}, nil
	}
	return sourceConfigGate{userID: userID}, nil
}

// cutUTF16 is s.slice(0, n).
func cutUTF16(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}

// gjPost is graphjinQuery() in packages/llm/src/graphjin/client.ts for a
// configuration query: data and the error messages, or an error when
// GraphJin does not answer 2xx.
func gjPost(ctx context.Context, endpoint, token, query string) (any, []string, error) {
	return gjPostVars(ctx, endpoint, token, query, &jsObj{})
}

func gjPostVars(ctx context.Context, endpoint, token, query string, variables *jsObj) (any, []string, error) {
	payload := jsStringify(jsObjOf("query", query, "variables", variables), false)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("authorization", "Bearer "+token)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect mode is set to error") }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, fmt.Errorf("graphjin query failed: %d %s", resp.StatusCode, cutUTF16(string(raw), 500))
	}
	body, err := jsParse(bytes.TrimSpace(raw))
	if err != nil {
		return nil, nil, err
	}
	var messages []string
	if list, ok := jsGet(body, "errors").([]any); ok {
		for _, item := range list {
			switch t := item.(type) {
			case string:
				messages = append(messages, t)
			case *jsObj:
				if m, ok := t.vals["message"].(string); ok {
					messages = append(messages, m)
				} else {
					messages = append(messages, jsStringify(t, false))
				}
			default:
				messages = append(messages, jsTemplate(t))
			}
		}
	}
	data, _ := jsField(body, "data")
	return data, messages, nil
}

const openAPIStoreFailed = "The OpenAPI document could not be saved. The server log holds the cause."

func sourceConfigError(err error) any { return jsObjOf("error", cutUTF16(err.Error(), 1000)) }

// describeSourceGraph is InProcessControlPlane.describeSourceGraph().
func (r *runTools) describeSourceGraph(ctx context.Context, in sourceConfigIn) (any, error) {
	gate, err := r.sourceConfigAccess(ctx, in.OrgID, in.RunID)
	if err != nil {
		return nil, err
	}
	if gate.denied != "" {
		return jsObjOf("reachable", false, "denied", true, "error", gate.denied), nil
	}
	var name string
	var graphqlURL *string
	err = r.api.DB.QueryRow(ctx, `select name, graphql_url from data_source where org_id = $1
		order by is_default desc, created_at limit 1`, in.OrgID).Scan(&name, &graphqlURL)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if deref(graphqlURL) == "" {
		return jsObjOf("reachable", false, "error", "no data source configured"), nil
	}
	token, err := r.api.graphjinToken(in.OrgID, "service", 300*time.Second)
	if err != nil {
		return nil, err
	}
	buckets := map[string]any{}
	for _, kind := range []string{"database", "capability", "namespace"} {
		data, messages, err := gjPost(ctx, *graphqlURL,
			token, `query { gj_catalog(where: { kind: { eq: "`+kind+`" } }, limit: 100) { id name summary } }`)
		if err != nil {
			return jsObjOf("reachable", false, "error", cutUTF16(err.Error(), 300)), nil
		}
		if len(messages) > 0 {
			return jsObjOf("reachable", false, "error", cutUTF16(strings.Join(messages, "; "), 300)), nil
		}
		rows, ok := jsGet(data, "gj_catalog").([]any)
		if !ok {
			rows = []any{}
		}
		buckets[kind] = rows
	}
	return jsObjOf("reachable", true, "sourceName", name, "host", hostOrNull(graphqlURL),
		"databases", buckets["database"], "capabilities", buckets["capability"], "namespaces", buckets["namespace"]), nil
}

// listSourceSecretNames is InProcessControlPlane.listSourceSecretNames().
func (r *runTools) listSourceSecretNames(ctx context.Context, in sourceConfigIn) (any, error) {
	gate, err := r.sourceConfigAccess(ctx, in.OrgID, in.RunID)
	if err != nil {
		return nil, err
	}
	if gate.denied != "" {
		return jsObjOf("denied", true, "error", gate.denied, "names", []any{}), nil
	}
	type secretName struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		UpdatedAt   string  `json:"updatedAt"`
	}
	rows, err := r.api.DB.Query(ctx, `select name, description, updated_at from data_source_secret
		where org_id = $1 order by updated_at desc`, in.OrgID)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (secretName, error) {
		var s secretName
		var updated time.Time
		err := row.Scan(&s.Name, &s.Description, &updated)
		s.UpdatedAt = jsTime(updated)
		return s, err
	})
	if err != nil {
		return nil, err
	}
	if names == nil {
		names = []secretName{}
	}
	return jsFromGo(map[string]any{"names": names}), nil
}

// importOpenAPISpec is InProcessControlPlane.importOpenApiSpec().
func (r *runTools) importOpenAPISpec(ctx context.Context, in openAPIImportIn) (any, error) {
	gate, err := r.sourceConfigAccess(ctx, in.OrgID, in.RunID)
	if err != nil {
		return nil, err
	}
	if gate.denied != "" {
		return jsObjOf("denied", true, "error", gate.denied), nil
	}
	imported, err := importOpenAPISpecFromURL(ctx, in.URL, in.BaseURL)
	if err != nil {
		return sourceConfigError(err), nil
	}
	asset, err := r.api.storeOpenAPIAsset(ctx, actor{OrgID: in.OrgID, UserID: deref(gate.userID)}, "url",
		imported.OriginalName, &imported.SourceURL, imported.openAPISpecSummary)
	if err != nil {
		log.Printf("[tools] store the imported OpenAPI document: %v", err)
		return jsObjOf("error", openAPIStoreFailed), nil
	}
	return jsFromGo(map[string]any{"asset": asset}), nil
}

// listOpenAPISpecs is InProcessControlPlane.listOpenApiSpecs().
func (r *runTools) listOpenAPISpecs(ctx context.Context, in openAPIListIn) (any, error) {
	gate, err := r.sourceConfigAccess(ctx, in.OrgID, in.RunID)
	if err != nil {
		return nil, err
	}
	if gate.denied != "" {
		return jsObjOf("denied", true, "error", gate.denied, "assets", []any{}), nil
	}
	limit := 20
	if in.Limit != nil {
		limit = min(max(*in.Limit, 1), 100)
	}
	rows, err := r.api.DB.Query(ctx, `select `+openAPIAssetColumns+` from openapi_spec_asset
		where org_id = $1 order by created_at desc limit $2`, in.OrgID, limit)
	if err != nil {
		return nil, err
	}
	assets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (openAPISpecAsset, error) { return scanOpenAPIAsset(row) })
	if err != nil {
		return nil, err
	}
	if assets == nil {
		assets = []openAPISpecAsset{}
	}
	return jsFromGo(map[string]any{"assets": assets}), nil
}

// checkSource is InProcessControlPlane.checkSource().
func (r *runTools) checkSource(ctx context.Context, in sourceCheckIn) (any, error) {
	gate, err := r.sourceConfigAccess(ctx, in.OrgID, in.RunID)
	if err != nil {
		return nil, err
	}
	if gate.denied != "" {
		return jsObjOf("denied", true, "error", gate.denied), nil
	}
	answers := map[string]any{}
	for _, k := range in.Answers.ordered() {
		answers[k] = in.Answers.vals[k]
	}
	check, err := r.api.checkSourceForOrg(ctx, in.OrgID, answers)
	if err != nil {
		return nil, err
	}
	return jsFromGo(map[string]any{"check": check}), nil
}

var sourceConfigViewFields = []string{"sources", "roles", "databases", "tables", "blocklist", "system", "mcp", "serv"}

const sourceConfigViewMaxChars = 60_000

// describeSourceConfig is InProcessControlPlane.describeSourceConfig().
func (r *runTools) describeSourceConfig(ctx context.Context, in sourceConfigDescribeIn) (any, error) {
	gate, err := r.sourceConfigAccess(ctx, in.OrgID, in.RunID)
	if err != nil {
		return nil, err
	}
	if gate.denied != "" {
		return jsObjOf("denied", true, "error", gate.denied), nil
	}
	return r.api.readSourceConfigView(ctx, in.OrgID, deref(gate.userID), in.DataSourceID)
}

type selectedSource struct {
	id, name, kind string
	graphqlURL     *string
}

// selectGraphjinSource is the data source choice of readSourceConfigView():
// the one enabled source, or the one named by dataSourceID.
func (a *API) selectGraphjinSource(ctx context.Context, orgID string, dataSourceID *string, many string) (*selectedSource, *jsObj, any, error) {
	query := `select id::text, name, kind, graphql_url from data_source where org_id = $1 and enabled = true`
	args := []any{orgID, 3}
	if dataSourceID != nil {
		query += ` and id = $3::uuid`
		args = []any{orgID, 1, *dataSourceID}
	}
	rows, err := a.DB.Query(ctx, query+` limit $2`, args...)
	if err != nil {
		return nil, nil, nil, err
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (selectedSource, error) {
		var s selectedSource
		return s, row.Scan(&s.id, &s.name, &s.kind, &s.graphqlURL)
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if len(found) == 0 {
		return nil, nil, jsObjOf("error", "no enabled customer GraphJin data source was found"), nil
	}
	if dataSourceID == nil && len(found) > 1 {
		return nil, nil, jsObjOf("error", many), nil
	}
	src := found[0]
	view := jsObjOf("id", src.id, "name", src.name, "host", hostOrNull(src.graphqlURL))
	return &src, view, nil, nil
}

// readSourceConfigView is readSourceConfigView() in
// packages/llm/src/work/control-plane.ts.
func (a *API) readSourceConfigView(ctx context.Context, orgID, userID string, dataSourceID *string) (any, error) {
	src, source, early, err := a.selectGraphjinSource(ctx, orgID, dataSourceID,
		"multiple enabled data sources exist; choose a dataSourceId with list_data_sources first")
	if err != nil || early != nil {
		return early, err
	}
	if src.kind != "graphjin" {
		return jsObjOf("source", source, "error", "the selected data source is not a GraphJin server"), nil
	}
	if isInternalGraphjinURL(deref(src.graphqlURL)) {
		return jsObjOf("denied", true, "source", source, "error", "refusing to read the OpenNeko internal GraphJin config"), nil
	}
	token, err := a.mintGraphjinToken(graphjinClaims{orgID: orgID, userID: userID, role: "admin"}, 120*time.Second)
	if err != nil {
		return nil, err
	}
	data, messages, err := gjPost(ctx, deref(src.graphqlURL), token,
		`query { gj_config(id: "current") { catalog_revision `+strings.Join(sourceConfigViewFields, " ")+` } }`)
	if err != nil {
		return jsObjOf("source", source, "error", cutUTF16(err.Error(), 1000)), nil
	}
	row, _ := jsGet(data, "gj_config").(*jsObj)
	if len(messages) > 0 || row == nil {
		message := cutUTF16(strings.Join(messages, "; "), 1000)
		if message == "" {
			message = "GraphJin did not return its config"
		}
		return jsObjOf("source", source, "error", message), nil
	}
	orEmpty := func(v any) any {
		if v == nil {
			return []any{}
		}
		return v
	}
	summary := describeGraphjinConfig(redactSourceConfig(orEmpty(parseJSONColumn(row.vals["sources"]))),
		redactSourceConfig(orEmpty(parseJSONColumn(row.vals["roles"]))))
	config := &jsObj{}
	size, truncated := 0, false
	for _, f := range sourceConfigViewFields {
		redacted := redactSourceConfig(parseJSONColumn(row.vals[f]))
		length := utf16Len(jsStringify(redacted, false))
		if size+length > sourceConfigViewMaxChars {
			truncated = true
			continue
		}
		size += length
		config.set(f, redacted)
	}
	revision := ""
	if v := row.vals["catalog_revision"]; v != nil {
		revision = jsTemplate(v)
	}
	out := jsObjOf("source", source, "catalogRevision", revision, "config", config, "summary", summary)
	if truncated {
		out.set("truncated", true)
	}
	return out, nil
}

// hostOrNull is hostnameOf(): the URL's host name, or null.
func hostOrNull(raw *string) any {
	if h := urlHostname(raw); h != nil {
		return *h
	}
	return nil
}

// parseJSONColumn is parseJsonColumn(): a JSON text parsed, else the value.
func parseJSONColumn(v any) any {
	text, ok := v.(string)
	if !ok {
		return v
	}
	parsed, err := jsParse([]byte(text))
	if err != nil {
		return v
	}
	return parsed
}

const redactedValue = "[REDACTED]"

var (
	sensitiveConfigParts = []string{"password", "secret", "token", "passphrase", "private_key", "client_key",
		"api_key", "apikey", "authorization", "cookie"}
	sensitiveConfigKeys = []string{"connection_string", "key_value", "dsn"}
	urlUserinfo         = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s@]*@`)
)

// isSensitiveConfigKey is isSensitiveConfigKey() in
// packages/llm/src/graphjin/config-redact.ts.
func isSensitiveConfigKey(key string) bool {
	k := strings.ReplaceAll(strings.ToLower(key), "-", "_")
	if containsString(sensitiveConfigKeys, k) {
		return true
	}
	for _, part := range sensitiveConfigParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

// redactSourceConfig is redactSourceConfig(): GraphJin's gj_config
// redaction rules, applied again on OpenNeko's side.
func redactSourceConfig(v any) any {
	switch t := v.(type) {
	case string:
		return urlUserinfo.ReplaceAllString(t, "${1}"+redactedValue+"@")
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = redactSourceConfig(item)
		}
		return out
	case *jsObj:
		out := &jsObj{}
		for _, k := range t.ordered() {
			if isSensitiveConfigKey(k) {
				out.set(k, redactedValue)
			} else {
				out.set(k, redactSourceConfig(t.vals[k]))
			}
		}
		return out
	}
	return v
}
