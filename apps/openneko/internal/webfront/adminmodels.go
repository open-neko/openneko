package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

type modelPrice struct {
	InputUSDPer1M  float64 `json:"inputUsdPer1M"`
	OutputUSDPer1M float64 `json:"outputUsdPer1M"`
}

type publicModel struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Provider     string            `json:"provider"`
	Model        string            `json:"model"`
	Enabled      bool              `json:"enabled"`
	Config       map[string]any    `json:"config"`
	SecretStatus map[string]string `json:"secretStatus"`
	Price        *modelPrice       `json:"price"`
}

type publicRoute struct {
	Task            string  `json:"task"`
	Label           string  `json:"label"`
	Description     string  `json:"description"`
	ModelID         *string `json:"modelId"`
	FallbackModelID *string `json:"fallbackModelId"`
}

type modelSettings struct {
	DefaultModel struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Source   string `json:"source"`
	} `json:"defaultModel"`
	Models  []publicModel `json:"models"`
	Routes  []publicRoute `json:"routes"`
	Catalog struct {
		Providers []llm.ProviderOption   `json:"providers"`
		Defaults  map[string]string      `json:"defaults"`
		Fields    map[string][]llm.Field `json:"fields"`
	} `json:"catalog"`
}

type modelRow struct {
	ID, Name, Provider, Model string
	Config, Secrets           map[string]any
	Enabled                   bool
	InputUSD, OutputUSD       *string
}

const modelColumns = "id::text, name, provider, model, config, secrets, enabled, input_usd_per_1m::text, output_usd_per_1m::text"

func scanModel(row pgx.Row) (modelRow, error) {
	var m modelRow
	var cfg, sec []byte
	if err := row.Scan(&m.ID, &m.Name, &m.Provider, &m.Model, &cfg, &sec, &m.Enabled, &m.InputUSD, &m.OutputUSD); err != nil {
		return m, err
	}
	_ = json.Unmarshal(cfg, &m.Config)
	_ = json.Unmarshal(sec, &m.Secrets)
	if m.Config == nil {
		m.Config = map[string]any{}
	}
	return m, nil
}

func (m modelRow) price() *modelPrice {
	if m.InputUSD == nil || m.OutputUSD == nil {
		return nil
	}
	in, _ := strconv.ParseFloat(*m.InputUSD, 64)
	out, _ := strconv.ParseFloat(*m.OutputUSD, 64)
	return &modelPrice{in, out}
}

type modelSettingsError struct {
	status  int
	message string
}

func (e modelSettingsError) Error() string { return e.message }

func badModel(message string) error { return modelSettingsError{http.StatusBadRequest, message} }

var errNoSuchModel = modelSettingsError{http.StatusNotFound, "No such model."}

func (a *API) publicModel(m modelRow) (publicModel, error) {
	secrets, err := llm.DecryptSecrets(a.SecretsDir, m.Secrets)
	if err != nil {
		return publicModel{}, err
	}
	status := map[string]string{}
	for k, v := range secrets {
		status[k] = llm.MaskSecret(v)
	}
	return publicModel{m.ID, m.Name, m.Provider, m.Model, m.Enabled, m.Config, status, m.price()}, nil
}

// modelSettingsFor is getModelSettings() in packages/llm/src/model-routes.ts.
func (a *API) modelSettingsFor(ctx context.Context, orgID string) (*modelSettings, error) {
	out := &modelSettings{Models: []publicModel{}}
	rows, err := a.DB.Query(ctx, `select `+modelColumns+` from llm_model where org_id = $1 order by name`, orgID)
	if err != nil {
		return nil, err
	}
	models, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (modelRow, error) { return scanModel(row) })
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		pm, err := a.publicModel(m)
		if err != nil {
			return nil, err
		}
		out.Models = append(out.Models, pm)
	}
	routes := map[string][2]*string{}
	rows, err = a.DB.Query(ctx, `select task, model_id::text, fallback_model_id::text from llm_task_route where org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	var task string
	var modelID, fallbackID *string
	if _, err := pgx.ForEachRow(rows, []any{&task, &modelID, &fallbackID}, func() error {
		routes[task] = [2]*string{modelID, fallbackID}
		return nil
	}); err != nil {
		return nil, err
	}
	for _, t := range append([]llm.Task{llm.DefaultRoute}, llm.Tasks...) {
		r := routes[t.Task]
		out.Routes = append(out.Routes, publicRoute{t.Task, t.Label, t.Description, r[0], r[1]})
	}
	out.DefaultModel.Source = "default"
	if primary, err := llm.PrimarySettings(ctx, a.DB, a.SecretsDir, orgID); err == nil {
		out.DefaultModel.Provider, out.DefaultModel.Model, out.DefaultModel.Source = primary.Provider, primary.Model, primary.Source
	}
	out.Catalog.Providers = llm.PrimaryProviders
	out.Catalog.Defaults = map[string]string{}
	out.Catalog.Fields = map[string][]llm.Field{}
	for _, p := range llm.PrimaryProviders {
		out.Catalog.Defaults[p.Value] = llm.DefaultModel(p.Value)
		out.Catalog.Fields[p.Value] = llm.Fields(p.Value)
	}
	return out, nil
}

var modelName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

const maxPriceUSDPer1M = 1000

func draftRecord(body map[string]any, key string) (map[string]any, error) {
	v, ok := body[key]
	if !ok || v == nil {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, badModel(key + " must be an object.")
	}
	return m, nil
}

func readModelPrice(body map[string]any) (*modelPrice, error) {
	if body["price"] == nil {
		return nil, nil
	}
	raw, err := draftRecord(body, "price")
	if err != nil {
		return nil, err
	}
	valid := func(v any) (float64, bool) {
		n, ok := v.(float64)
		return n, ok && !math.IsInf(n, 0) && !math.IsNaN(n) && n >= 0 && n <= maxPriceUSDPer1M
	}
	in, okIn := valid(raw["inputUsdPer1M"])
	out, okOut := valid(raw["outputUsdPer1M"])
	if !okIn || !okOut {
		return nil, badModel(fmt.Sprintf("Each price must be a number from 0 to %d USD per million tokens.", maxPriceUSDPer1M))
	}
	return &modelPrice{in, out}, nil
}

func draftString(body map[string]any, key string) string {
	v := body[key]
	if v == nil {
		return ""
	}
	return jsTrim(jsStringOf(v))
}

// saveModel is saveModel() in model-routes.ts. A secret left out keeps its
// stored value; null removes it.
func (a *API) saveModel(ctx context.Context, act actor, id string, body map[string]any) (publicModel, error) {
	var existing *modelRow
	if id != "" {
		m, err := scanModel(a.DB.QueryRow(ctx, `select `+modelColumns+` from llm_model where org_id = $1 and id = $2::uuid`, act.OrgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return publicModel{}, errNoSuchModel
		}
		if err != nil {
			return publicModel{}, err
		}
		existing = &m
	}
	_, hasName := body["name"]
	name := draftString(body, "name")
	if !hasName && existing != nil {
		name = existing.Name
	}
	if !modelName.MatchString(name) || name == "default" {
		return publicModel{}, badModel(`The name must start with a letter and use only a to z, 0 to 9 and hyphens, at most 40 characters. "default" is taken.`)
	}
	_, hasProvider := body["provider"]
	provider := ""
	if v := body["provider"]; v != nil {
		provider = jsStringOf(v)
	}
	if !hasProvider && existing != nil {
		provider = existing.Provider
	}
	if !llm.IsPrimaryProvider(provider) {
		return publicModel{}, badModel("Choose a model provider from the list.")
	}
	providerChanged := existing == nil || existing.Provider != provider
	_, hasModel := body["model"]
	model := draftString(body, "model")
	if !hasModel && existing != nil && !providerChanged {
		model = existing.Model
	}
	if model == "" {
		model = llm.DefaultModel(provider)
	}
	if model == "" || jsLength(model) > 128 {
		return publicModel{}, badModel("The model name is required, at most 128 characters.")
	}
	fields := llm.Fields(provider)
	configInput, err := draftRecord(body, "config")
	if err != nil {
		return publicModel{}, err
	}
	cfg := map[string]any{}
	for _, f := range fields {
		if f.Kind == "secret" {
			continue
		}
		v := configInput[f.Key]
		if v == nil && !providerChanged {
			v = existing.Config[f.Key]
		}
		if s, ok := v.(string); ok && jsTrim(s) != "" {
			cfg[f.Key] = jsTrim(s)
		}
	}
	secrets := map[string]string{}
	if !providerChanged {
		if secrets, err = llm.DecryptSecrets(a.SecretsDir, existing.Secrets); err != nil {
			return publicModel{}, err
		}
	}
	secretInput, err := draftRecord(body, "secrets")
	if err != nil {
		return publicModel{}, err
	}
	for _, f := range fields {
		if f.Kind != "secret" {
			continue
		}
		v, present := secretInput[f.Key]
		if s, ok := v.(string); ok && jsTrim(s) != "" {
			secrets[f.Key] = jsTrim(s)
		} else if present && v == nil {
			delete(secrets, f.Key)
		}
	}
	for _, f := range fields {
		set := secrets[f.Key] != ""
		if f.Kind != "secret" {
			set = cfg[f.Key] != nil
		}
		if f.IsRequired() && !set {
			return publicModel{}, badModel(f.Label + " is required.")
		}
	}
	enabled := true
	if v, ok := body["enabled"]; ok {
		enabled = v == true
	} else if existing != nil {
		enabled = existing.Enabled
	}
	var price *modelPrice
	if _, ok := body["price"]; !ok && existing != nil {
		price = existing.price()
	} else if price, err = readModelPrice(body); err != nil {
		return publicModel{}, err
	}
	if _, err := llm.ResolveRuntime(provider, model, cfg); err != nil {
		return publicModel{}, badModel(err.Error())
	}
	if err := (llm.Settings{Source: "org", Provider: provider, Model: model, Enabled: enabled, Config: cfg, Secrets: secrets}).Validate(); err != nil {
		return publicModel{}, badModel(err.Error())
	}
	sealed := map[string]string{}
	for k, v := range secrets {
		if sealed[k], err = config.EncryptValue(a.SecretsDir, v); err != nil {
			return publicModel{}, err
		}
	}
	cfgJSON, _ := json.Marshal(cfg)
	sealedJSON, _ := json.Marshal(sealed)
	var in, out any
	if price != nil {
		in, out = price.InputUSDPer1M, price.OutputUSDPer1M
	}
	args := []any{act.OrgID, name, provider, model, cfgJSON, sealedJSON, enabled, in, out}
	var row pgx.Row
	if existing != nil {
		row = a.DB.QueryRow(ctx, `update llm_model set name = $2, provider = $3, model = $4, config = $5::jsonb, secrets = $6::jsonb, enabled = $7,
		        input_usd_per_1m = $8, output_usd_per_1m = $9, updated_at = now()
		  where org_id = $1 and id = $10::uuid returning `+modelColumns, append(args, existing.ID)...)
	} else {
		row = a.DB.QueryRow(ctx, `insert into llm_model (org_id, name, provider, model, config, secrets, enabled, input_usd_per_1m, output_usd_per_1m)
		  values ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9) returning `+modelColumns, args...)
	}
	stored, err := scanModel(row)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return publicModel{}, modelSettingsError{http.StatusConflict, fmt.Sprintf(`A model named "%s" exists.`, name)}
	}
	if err != nil {
		return publicModel{}, err
	}
	saved, err := a.publicModel(stored)
	if err != nil {
		return publicModel{}, err
	}
	keys := make([]string, 0, len(secretInput))
	for k := range secretInput {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	changed := make([]any, len(keys))
	for i, k := range keys {
		changed[i] = k
	}
	event := "model:added"
	if existing != nil {
		event = "model:changed"
	}
	var auditPrice any
	if price != nil {
		auditPrice = map[string]any{"inputUsdPer1M": json.Number(jsNumberText(price.InputUSDPer1M)), "outputUsdPer1M": json.Number(jsNumberText(price.OutputUSDPer1M))}
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "llm_model", EntityID: saved.ID, Event: event, Payload: map[string]any{
		"actorUserId": auditActor(act.UserID), "name": name, "provider": provider, "model": model, "enabled": enabled,
		"price": auditPrice, "secretsChanged": changed,
	}})
	return saved, nil
}

func (a *API) deleteModel(ctx context.Context, act actor, id string) error {
	var name string
	err := a.DB.QueryRow(ctx, `delete from llm_model where org_id = $1 and id = $2::uuid returning name`, act.OrgID, id).Scan(&name)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23503":
		return modelSettingsError{http.StatusConflict, "A task route uses this model. Change the route first."}
	case errors.Is(err, pgx.ErrNoRows):
		return errNoSuchModel
	case err != nil:
		return err
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "llm_model", EntityID: id, Event: "model:removed",
		Payload: map[string]any{"actorUserId": auditActor(act.UserID), "name": name}})
	return nil
}

func readRouteModelID(body map[string]any, key string) (*string, error) {
	v := body[key]
	if v == nil || v == "" || v == "default" {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok || !isUUID(s) {
		return nil, badModel(key + " must be a model id.")
	}
	return &s, nil
}

// saveRoute is saveRoute() in model-routes.ts. A route with no model and no
// fallback is removed, so the task follows the default route again.
func (a *API) saveModelRoute(ctx context.Context, act actor, body map[string]any) error {
	task, _ := body["task"].(string)
	if task != "default" && !llm.IsTask(task) {
		return badModel("Choose a task from the list.")
	}
	modelID, err := readRouteModelID(body, "modelId")
	if err != nil {
		return err
	}
	fallbackID, err := readRouteModelID(body, "fallbackModelId")
	if err != nil {
		return err
	}
	if task == "default" && modelID != nil {
		return badModel("The default route always uses the default model. Set only its fallback.")
	}
	if modelID != nil && fallbackID != nil && *modelID == *fallbackID {
		return badModel("The fallback must be another model.")
	}
	ids := []string{}
	for _, id := range []*string{modelID, fallbackID} {
		if id != nil {
			ids = append(ids, *id)
		}
	}
	if len(ids) > 0 {
		var found int
		if err := a.DB.QueryRow(ctx, `select count(*) from llm_model where org_id = $1 and id = any($2::uuid[])`, act.OrgID, ids).Scan(&found); err != nil {
			return err
		}
		if found != len(ids) {
			return errNoSuchModel
		}
		_, err = a.DB.Exec(ctx, `insert into llm_task_route (org_id, task, model_id, fallback_model_id) values ($1, $2, $3::uuid, $4::uuid)
		  on conflict (org_id, task) do update set model_id = excluded.model_id, fallback_model_id = excluded.fallback_model_id, updated_at = now()`,
			act.OrgID, task, modelID, fallbackID)
	} else {
		_, err = a.DB.Exec(ctx, `delete from llm_task_route where org_id = $1 and task = $2`, act.OrgID, task)
	}
	if err != nil {
		return err
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "llm_model", EntityID: task, Event: "model:route_changed",
		Payload: map[string]any{"actorUserId": auditActor(act.UserID), "task": task, "modelId": auditString(modelID), "fallbackModelId": auditString(fallbackID)}})
	return nil
}

func auditString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func (a *API) sendModelSettings(w http.ResponseWriter, r *http.Request, orgID string, status int, wrap func(*modelSettings) any) {
	settings, err := a.modelSettingsFor(r.Context(), orgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, status, wrap(settings))
}

func (a *API) modelFailure(w http.ResponseWriter, r *http.Request, err error) {
	var bad modelSettingsError
	if errors.As(err, &bad) {
		sendError(w, bad.status, bad.message)
		return
	}
	a.fail(w, r, err)
}

func asSettings(s *modelSettings) any { return s }

func (a *API) adminModels(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	a.sendModelSettings(w, r, act.OrgID, http.StatusOK, asSettings)
}

func (a *API) adminModelSave(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("modelId")
	status := http.StatusCreated
	if r.Method == http.MethodPut {
		status = http.StatusOK
		if !isUUID(id) {
			sendError(w, http.StatusNotFound, errNoSuchModel.message)
			return
		}
	}
	body := readJSONObject(r)
	if body == nil {
		sendError(w, http.StatusBadRequest, "A JSON body is required.")
		return
	}
	model, err := a.saveModel(r.Context(), act, id, body)
	if err != nil {
		a.modelFailure(w, r, err)
		return
	}
	a.sendModelSettings(w, r, act.OrgID, status, func(s *modelSettings) any {
		return map[string]any{"model": model, "settings": s}
	})
}

func (a *API) adminModelDelete(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("modelId")
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, errNoSuchModel.message)
		return
	}
	if err := a.deleteModel(r.Context(), act, id); err != nil {
		a.modelFailure(w, r, err)
		return
	}
	a.sendModelSettings(w, r, act.OrgID, http.StatusOK, asSettings)
}

func (a *API) adminModelRoute(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body := readJSONObject(r)
	if body == nil {
		sendError(w, http.StatusBadRequest, "A JSON body is required.")
		return
	}
	if err := a.saveModelRoute(r.Context(), act, body); err != nil {
		a.modelFailure(w, r, err)
		return
	}
	a.sendModelSettings(w, r, act.OrgID, http.StatusOK, asSettings)
}
