package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

const agentDefaultGlobalCap = 3

type agentSettings struct {
	Source    string `json:"source"`
	Backend   string `json:"backend"`
	GlobalCap int    `json:"globalCap"`
	Terminal  bool   `json:"terminal"`
}

var agentBackendOptions = []llm.ProviderOption{
	{Value: "ax", Label: "Ax", Description: "OpenNeko's Go agent. Calls tools from code, so one model step can run many tools."},
}

// numberOf is Number(v) for the JSON values a settings body can hold.
func numberOf(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case bool:
		if n {
			return 1
		}
		return 0
	case nil:
		return 0
	case string:
		if f, ok := jsNumber(n); ok {
			return f
		}
	}
	return math.NaN()
}

func positiveCap(raw any, fallback int) int {
	n := numberOf(raw)
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > 1000 {
		return fallback
	}
	return int(math.Floor(n))
}

func (a *API) loadAgentRow(ctx context.Context, orgID string) (id string, cfg map[string]any, found bool, err error) {
	var raw []byte
	err = a.DB.QueryRow(ctx, `select id::text, config from llm_provider_config where org_id = $1 and scope = 'agent' limit 1`, orgID).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", map[string]any{}, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	_ = json.Unmarshal(raw, &cfg)
	if cfg == nil {
		cfg = map[string]any{}
	}
	return id, cfg, true, nil
}

func (a *API) settingsAgentGet(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	_, cfg, found, err := a.loadAgentRow(r.Context(), act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	source := "default"
	if found {
		source = "org"
	}
	sendJSON(w, http.StatusOK, map[string]any{
		"agent":             agentSettings{source, "ax", positiveCap(cfg["globalCap"], agentDefaultGlobalCap), cfg["terminal"] != false},
		"options":           agentBackendOptions,
		"terminalAvailable": strings.TrimSpace(os.Getenv("OPENNEKO_SANDBOX_URL")) != "",
		"defaults":          map[string]any{"globalCap": agentDefaultGlobalCap, "terminal": true},
	})
}

// readSettingsBody is `await request.json()` inside a try block whose catch
// answers 400 with the error text.
func readSettingsBody(w http.ResponseWriter, r *http.Request) (any, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err == nil {
		var v any
		if v, err = jsParse(raw); err == nil {
			return v, true
		}
	}
	sendError(w, http.StatusBadRequest, "The request body is not valid JSON.")
	return nil, false
}

func jsDisplay(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "null"
	case *jsObj:
		return "[object Object]"
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			if item != nil {
				parts[i] = jsDisplay(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return jsStringify(v, false)
}

func (a *API) settingsAgentPut(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	parsed, ok := readSettingsBody(w, r)
	if !ok {
		return
	}
	body, isObj := parsed.(*jsObj)
	if !isObj {
		sendError(w, http.StatusBadRequest, "Agent settings payload must be an object")
		return
	}
	var unsupported []string
	for _, k := range body.ordered() {
		if k != "backend" && k != "globalCap" && k != "terminal" {
			unsupported = append(unsupported, k)
		}
	}
	if len(unsupported) > 0 {
		sendError(w, http.StatusBadRequest, "Unsupported agent setting: "+strings.Join(unsupported, ", "))
		return
	}
	if backend, set := body.vals["backend"]; set && backend != "ax" {
		sendError(w, http.StatusBadRequest, "Unsupported agent backend: "+jsDisplay(backend))
		return
	}
	terminalRaw, terminalSet := body.vals["terminal"]
	if _, isBool := terminalRaw.(bool); terminalSet && !isBool {
		sendError(w, http.StatusBadRequest, "terminal must be true or false")
		return
	}
	ctx := r.Context()
	id, existing, found, err := a.loadAgentRow(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	capRaw := body.vals["globalCap"]
	if capRaw == nil {
		capRaw = existing["globalCap"]
	}
	globalCap := positiveCap(capRaw, agentDefaultGlobalCap)
	terminal := existing["terminal"] != false
	if terminalSet {
		terminal = terminalRaw.(bool)
	}
	cfg, _ := json.Marshal(map[string]any{"backend": "ax", "globalCap": globalCap, "terminal": terminal})
	if found {
		_, err = a.DB.Exec(ctx, `update llm_provider_config set provider = 'ax', config = $2, updated_at = now() where id = $1`, id, cfg)
	} else {
		_, err = a.DB.Exec(ctx, `insert into llm_provider_config (org_id, scope, provider, enabled, config, secrets)
			values ($1, 'agent', 'ax', true, $2, '{}')`, act.OrgID, cfg)
	}
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.provisionHostConfig(ctx, act.OrgID)
	sendJSON(w, http.StatusOK, agentSettings{"org", "ax", globalCap, terminal})
}

type publicDataSource struct {
	Source     string `json:"source"`
	Kind       string `json:"kind"`
	GraphqlURL string `json:"graphqlUrl"`
	McpURL     string `json:"mcpUrl"`
	Label      string `json:"label"`
}

type sourceSetting struct {
	id, kind, graphqlURL string
	mcpURL, label        *string
}

func (a *API) loadDataSource(ctx context.Context, orgID string) (*sourceSetting, error) {
	var row sourceSetting
	err := a.DB.QueryRow(ctx, `select id::text, kind, graphql_url, mcp_url, label from data_source where org_id = $1
		order by is_default desc, created_at limit 1`, orgID).Scan(&row.id, &row.kind, &row.graphqlURL, &row.mcpURL, &row.label)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &row, err
}

func publicSource(row *sourceSetting) publicDataSource {
	if row == nil {
		return publicDataSource{"unset", "graphjin", "", "", "primary"}
	}
	p := publicDataSource{"org", row.kind, row.graphqlURL, "", "primary"}
	if row.mcpURL != nil {
		p.McpURL = *row.mcpURL
	}
	if row.label != nil {
		p.Label = *row.label
	}
	return p
}

func (a *API) settingsDataSourceGet(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	row, err := a.loadDataSource(r.Context(), act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, publicSource(row))
}

type dataSourceDraft struct {
	graphqlURL    string
	mcpURL, label *string
}

// readDataSourceDraft is `String(body.graphqlUrl ?? "")` and
// `body.x == null ? null : String(body.x)` for the two optional fields.
func readDataSourceDraft(parsed any) dataSourceDraft {
	var d dataSourceDraft
	body, _ := parsed.(*jsObj)
	val := func(k string) any {
		if body == nil {
			return nil
		}
		return body.vals[k]
	}
	if v := val("graphqlUrl"); v != nil {
		d.graphqlURL = jsDisplay(v)
	}
	for k, dst := range map[string]**string{"mcpUrl": &d.mcpURL, "label": &d.label} {
		if v := val(k); v != nil {
			s := jsDisplay(v)
			*dst = &s
		}
	}
	return d
}

func urlProblem(raw, name string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || ((u.Scheme == "http" || u.Scheme == "https") && u.Host == "") {
		return name + " is not a valid URL."
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return name + " must use http or https."
	}
	return ""
}

func (d dataSourceDraft) problems() []string {
	var errs []string
	if g := strings.TrimSpace(d.graphqlURL); g == "" {
		errs = append(errs, "GraphQL URL is required.")
	} else if p := urlProblem(g, "GraphQL URL"); p != "" {
		errs = append(errs, p)
	}
	if d.mcpURL != nil {
		if m := strings.TrimSpace(*d.mcpURL); m != "" {
			if p := urlProblem(m, "MCP URL"); p != "" {
				errs = append(errs, p)
			}
		}
	}
	return errs
}

func trimmedOr(s *string, fallback *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return fallback
	}
	t := strings.TrimSpace(*s)
	return &t
}

func (a *API) settingsDataSourcePut(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	parsed, ok := readSettingsBody(w, r)
	if !ok {
		return
	}
	d := readDataSourceDraft(parsed)
	if errs := d.problems(); len(errs) > 0 {
		sendError(w, http.StatusBadRequest, strings.Join(errs, " "))
		return
	}
	ctx := r.Context()
	existing, err := a.loadDataSource(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	primary := "primary"
	saved := sourceSetting{kind: "graphjin", graphqlURL: strings.TrimSpace(d.graphqlURL), mcpURL: trimmedOr(d.mcpURL, nil), label: trimmedOr(d.label, &primary)}
	if existing != nil {
		saved.kind = existing.kind
		_, err = a.DB.Exec(ctx, `update data_source set kind = $2, graphql_url = $3, mcp_url = $4, label = $5, updated_at = now() where id = $1`,
			existing.id, saved.kind, saved.graphqlURL, saved.mcpURL, saved.label)
	} else {
		_, err = a.DB.Exec(ctx, `insert into data_source (org_id, kind, graphql_url, mcp_url, label) values ($1, $2, $3, $4, $5)`,
			act.OrgID, saved.kind, saved.graphqlURL, saved.mcpURL, saved.label)
	}
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.provisionHostConfig(ctx, act.OrgID)
	sendJSON(w, http.StatusOK, publicSource(&saved))
}

func (a *API) settingsDataSourceTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.settingsAdmin(w, r); !ok {
		return
	}
	parsed, ok := readSettingsBody(w, r)
	if !ok {
		return
	}
	d := readDataSourceDraft(parsed)
	if errs := d.problems(); len(errs) > 0 {
		sendError(w, http.StatusBadRequest, strings.Join(errs, " "))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	status, body, err := postJSON(ctx, strings.TrimSpace(d.graphqlURL), `{"query":"{ __typename }"}`, "application/json")
	if err != nil {
		sendError(w, http.StatusBadRequest, "GraphQL URL unreachable: "+err.Error())
		return
	}
	var gql any
	if json.Unmarshal(body, &gql) != nil {
		gql = nil
	}
	switch v := gql.(type) {
	case map[string]any:
		_, hasData := v["data"]
		_, hasErrors := v["errors"]
		if !hasData && !hasErrors {
			sendError(w, http.StatusBadRequest, "GraphQL URL did not return a valid GraphQL response.")
			return
		}
	case []any:
		sendError(w, http.StatusBadRequest, "GraphQL URL did not return a valid GraphQL response.")
		return
	default:
		sendError(w, http.StatusBadRequest, fmt.Sprintf("GraphQL URL did not return JSON (status %d).", status))
		return
	}
	var mcp *string
	if d.mcpURL != nil && strings.TrimSpace(*d.mcpURL) != "" {
		mcp = new(strings.TrimSpace(*d.mcpURL))
	}
	if mcp == nil {
		sendJSON(w, http.StatusOK, map[string]any{"graphqlOk": true, "mcpOk": nil})
		return
	}
	mcpStatus, _, err := postJSON(ctx, *mcp, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "application/json, text/event-stream")
	sendJSON(w, http.StatusOK, map[string]any{"graphqlOk": true, "mcpOk": err == nil && mcpStatus >= 200 && mcpStatus <= 299})
}

func postJSON(ctx context.Context, target, body, accept string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader([]byte(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "application/json" {
		req.Header.Set("Accept", accept)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res.StatusCode, out, err
}

type providerForm struct {
	Scope        string            `json:"scope"`
	Source       string            `json:"source"`
	Provider     string            `json:"provider"`
	Model        string            `json:"model"`
	Label        *string           `json:"label"`
	Enabled      bool              `json:"enabled"`
	Config       map[string]any    `json:"config"`
	SecretStatus map[string]string `json:"secretStatus"`

	id      string
	secrets map[string]string
}

func (f providerForm) public() providerForm {
	f.SecretStatus = map[string]string{}
	for k, v := range f.secrets {
		f.SecretStatus[k] = llm.MaskSecret(v)
	}
	return f
}

func defaultProviderForm(scope string) providerForm {
	f := providerForm{Scope: scope, Source: "default", Enabled: true, Config: map[string]any{}, secrets: map[string]string{}}
	if scope == "primary" {
		f.Provider, f.Model = "google-gemini", llm.DefaultModel("google-gemini")
	} else {
		f.Provider, f.Model = "perplexity", llm.DefaultResearchModel("perplexity")
	}
	return f
}

func validForScope(scope, provider string) bool {
	if scope == "primary" {
		return llm.IsPrimaryProvider(provider)
	}
	return llm.IsResearchProvider(provider)
}

func (a *API) loadProviderForm(ctx context.Context, orgID, scope string) (providerForm, error) {
	f := providerForm{Scope: scope, Source: "org"}
	var model *string
	var rawCfg, rawSecrets []byte
	err := a.DB.QueryRow(ctx, `select id::text, provider, model, label, enabled, config, secrets from llm_provider_config
		where org_id = $1 and scope = $2 limit 1`, orgID, scope).Scan(&f.id, &f.Provider, &model, &f.Label, &f.Enabled, &rawCfg, &rawSecrets)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultProviderForm(scope), nil
	}
	if err != nil {
		return f, err
	}
	if !validForScope(scope, f.Provider) {
		d := defaultProviderForm(scope)
		d.id = f.id
		return d, nil
	}
	if model != nil {
		f.Model = *model
	}
	_ = json.Unmarshal(rawCfg, &f.Config)
	if f.Config == nil {
		f.Config = map[string]any{}
	}
	var sealed map[string]any
	_ = json.Unmarshal(rawSecrets, &sealed)
	f.secrets, err = llm.DecryptSecrets(a.SecretsDir, sealed)
	return f, err
}

func (a *API) settingsProviderGet(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	primary, err := a.loadProviderForm(r.Context(), act.OrgID, "primary")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	research, err := a.loadProviderForm(r.Context(), act.OrgID, "research")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	primary.id, research.id = "", ""
	defaults := map[string]map[string]string{"primary": {}, "research": {}}
	fields := map[string]map[string][]llm.Field{"primary": {}, "research": {}}
	for _, o := range llm.PrimaryProviders {
		defaults["primary"][o.Value] = llm.DefaultModel(o.Value)
		fields["primary"][o.Value] = llm.Fields(o.Value)
	}
	for _, o := range llm.ResearchProviders {
		defaults["research"][o.Value] = llm.DefaultResearchModel(o.Value)
		fields["research"][o.Value] = llm.ResearchFields(o.Value)
	}
	sendJSON(w, http.StatusOK, map[string]any{
		"primary":  primary.public(),
		"research": research.public(),
		"options":  map[string]any{"primary": llm.PrimaryProviders, "research": llm.ResearchProviders},
		"defaults": defaults,
		"fields":   fields,
	})
}

// mergeProviderDraft is mergeDraft() in apps/web/src/lib/provider-settings.ts.
func mergeProviderDraft(existing providerForm, body *jsObj, scope string) (providerForm, error) {
	provider, _ := body.vals["provider"].(string)
	if !validForScope(scope, provider) {
		return providerForm{}, fmt.Errorf("Unsupported %s provider: %s", scope, jsDisplay(body.vals["provider"]))
	}
	defaultModel := llm.DefaultModel
	if scope == "research" {
		defaultModel = llm.DefaultResearchModel
	}
	changed := existing.Provider != provider
	m := providerForm{Scope: scope, Provider: provider, Label: existing.Label, Enabled: existing.Enabled, Config: map[string]any{}, secrets: map[string]string{}, id: existing.id}
	if !changed {
		for k, v := range existing.secrets {
			m.secrets[k] = v
		}
		for k, v := range existing.Config {
			m.Config[k] = v
		}
	}
	if secrets, ok := body.vals["secrets"].(*jsObj); ok {
		for _, k := range secrets.ordered() {
			switch v := secrets.vals[k].(type) {
			case nil:
				delete(m.secrets, k)
			case string:
				if t := strings.TrimSpace(v); t != "" {
					m.secrets[k] = t
				}
			}
		}
	}
	if cfg, ok := body.vals["config"].(*jsObj); ok {
		for _, k := range cfg.ordered() {
			m.Config[k] = jsPlain(cfg.vals[k])
		}
	}
	model, _ := body.vals["model"].(string)
	m.Model = strings.TrimSpace(model)
	if m.Model == "" {
		m.Model = existing.Model
		if changed || m.Model == "" {
			m.Model = defaultModel(provider)
		}
	}
	if label, ok := body.vals["label"].(string); ok {
		m.Label = &label
	}
	if enabled, ok := body.vals["enabled"].(bool); ok {
		m.Enabled = enabled
	}
	return m, nil
}

// jsPlain turns a jsParse value back into plain JSON values for storage.
func jsPlain(v any) any {
	var out any
	_ = json.Unmarshal([]byte(jsStringify(v, false)), &out)
	return out
}

func (a *API) settingsProviderPut(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	parsed, ok := readSettingsBody(w, r)
	if !ok {
		return
	}
	body, isObj := parsed.(*jsObj)
	if !isObj {
		sendError(w, http.StatusBadRequest, "Provider settings payload must be an object")
		return
	}
	scope, _ := body.vals["scope"].(string)
	if scope != "primary" && scope != "research" {
		sendError(w, http.StatusBadRequest, "Unsupported provider scope: "+jsDisplay(body.vals["scope"]))
		return
	}
	ctx := r.Context()
	existing, err := a.loadProviderForm(ctx, act.OrgID, scope)
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	merged, err := mergeProviderDraft(existing, body, scope)
	if err == nil {
		if errs := llm.FormErrors(scope, merged.Provider, merged.Model, merged.Enabled, merged.Config, merged.secrets); len(errs) > 0 {
			err = errors.New(strings.Join(errs, " "))
		}
	}
	if err == nil {
		err = a.saveProviderForm(ctx, act.OrgID, merged)
	}
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	if scope == "primary" {
		a.provisionHostConfig(ctx, act.OrgID)
	}
	merged.Source, merged.id = "org", ""
	sendJSON(w, http.StatusOK, merged.public())
}

func (a *API) saveProviderForm(ctx context.Context, orgID string, f providerForm) error {
	sealed := map[string]string{}
	for k, v := range f.secrets {
		enc, err := config.EncryptValue(a.SecretsDir, v)
		if err != nil {
			return err
		}
		sealed[k] = enc
	}
	secrets, _ := json.Marshal(sealed)
	cfg, _ := json.Marshal(f.Config)
	var model *string
	if f.Model != "" {
		model = &f.Model
	}
	var err error
	if f.id != "" {
		_, err = a.DB.Exec(ctx, `update llm_provider_config set scope = $2, provider = $3, model = $4, label = $5, enabled = $6,
			config = $7, secrets = $8, updated_at = now() where id = $1`, f.id, f.Scope, f.Provider, model, f.Label, f.Enabled, cfg, secrets)
	} else {
		_, err = a.DB.Exec(ctx, `insert into llm_provider_config (org_id, scope, provider, model, label, enabled, config, secrets)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`, orgID, f.Scope, f.Provider, model, f.Label, f.Enabled, cfg, secrets)
	}
	return err
}
