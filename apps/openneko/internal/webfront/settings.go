package webfront

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

func jsTrim(s string) string { return strings.TrimFunc(s, jsSpace) }

func fieldStringOrNil(body any, name string) *string {
	if s, ok := fieldString(body, name); ok {
		return &s
	}
	return nil
}

// settingsAdmin is the `actor.role !== "admin"` check of the settings routes.
func (a *API) settingsAdmin(w http.ResponseWriter, r *http.Request) (actor, bool) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return act, false
	}
	if !act.Admin {
		sendError(w, http.StatusForbidden, "admin only")
		return act, false
	}
	return act, true
}

// readObjectBody is `await request.json().catch(() => ({}))` followed by a
// property read: a JSON null throws in Node and gives 500.
func readObjectBody(w http.ResponseWriter, r *http.Request) (any, bool) {
	body, parsed := readBody(r)
	if parsed && body == nil {
		sendError(w, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	return body, true
}

type channelIdentity struct {
	ID            string  `json:"id"`
	OrgID         string  `json:"org_id"`
	ChannelPlugin string  `json:"channel_plugin"`
	WorkspaceID   string  `json:"workspace_id"`
	ChannelUserID string  `json:"channel_user_id"`
	AppUserID     *string `json:"app_user_id"`
	DisplayName   *string `json:"display_name"`
	Email         *string `json:"email"`
	Status        string  `json:"status"`
	FirstSeenAt   string  `json:"first_seen_at"`
	VerifiedAt    *string `json:"verified_at"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

const channelIdentityColumns = `id::text, org_id, channel_plugin, workspace_id, channel_user_id, app_user_id,
	display_name, email, status, first_seen_at, verified_at, created_at, updated_at`

func scanChannelIdentity(row pgx.Row) (channelIdentity, error) {
	var c channelIdentity
	var firstSeen, created, updated time.Time
	var verified *time.Time
	err := row.Scan(&c.ID, &c.OrgID, &c.ChannelPlugin, &c.WorkspaceID, &c.ChannelUserID, &c.AppUserID,
		&c.DisplayName, &c.Email, &c.Status, &firstSeen, &verified, &created, &updated)
	c.FirstSeenAt, c.VerifiedAt, c.CreatedAt, c.UpdatedAt = jsTime(firstSeen), jsTimePtr(verified), jsTime(created), jsTime(updated)
	return c, err
}

func (a *API) channelIdentities(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	rows, err := a.DB.Query(r.Context(), `select `+channelIdentityColumns+` from channel_identity
		where org_id = $1 order by updated_at desc limit 500`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	identities, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (channelIdentity, error) {
		return scanChannelIdentity(row)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if identities == nil {
		identities = []channelIdentity{}
	}
	sendJSON(w, http.StatusOK, map[string]any{"identities": identities})
}

func (a *API) channelIdentityPatch(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body, ok := readObjectBody(w, r)
	if !ok {
		return
	}
	action, _ := fieldString(body, "action")
	id := r.PathValue("id")
	ctx := r.Context()
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	var exists bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from channel_identity where org_id = $1 and id = $2)`,
		act.OrgID, id).Scan(&exists); err != nil {
		a.fail(w, r, err)
		return
	}
	if !exists {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	var update string
	args := []any{id}
	switch action {
	case "link":
		appUserID, _ := fieldString(body, "appUserId")
		var userID string
		err := a.DB.QueryRow(ctx, `select id from app_user where org_id = $1 and id = $2 and disabled_at is null`,
			act.OrgID, appUserID).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			sendError(w, http.StatusBadRequest, "unknown or disabled user")
			return
		}
		if err != nil {
			a.fail(w, r, err)
			return
		}
		update = `app_user_id = $2, status = 'linked', verified_at = now(), updated_at = now()`
		args = append(args, userID)
	case "block":
		update = `status = 'blocked', updated_at = now()`
	case "unlink", "unblock":
		update = `app_user_id = null, status = 'unverified', verified_at = null, updated_at = now()`
	default:
		sendError(w, http.StatusBadRequest, "action must be link|unlink|block|unblock")
		return
	}
	identity, err := scanChannelIdentity(a.DB.QueryRow(ctx, `update channel_identity set `+update+`
		where id = $1 returning `+channelIdentityColumns, args...))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"identity": identity})
}

type dataSourceRow struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Label           *string `json:"label"`
	Kind            string  `json:"kind"`
	GraphqlURL      string  `json:"graphqlUrl"`
	McpURL          *string `json:"mcpUrl"`
	SubscriptionURL *string `json:"subscriptionUrl"`
	AuthMode        string  `json:"authMode"`
	IsDefault       bool    `json:"isDefault"`
	Enabled         bool    `json:"enabled"`
	UpdatedAt       string  `json:"updatedAt"`
}

func (a *API) dataSources(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	rows, err := a.DB.Query(r.Context(), `select id::text, name, label, kind, graphql_url, mcp_url, subscription_url,
		auth_mode, is_default, enabled, updated_at from data_source where org_id = $1`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sources, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dataSourceRow, error) {
		var s dataSourceRow
		var updated time.Time
		err := row.Scan(&s.ID, &s.Name, &s.Label, &s.Kind, &s.GraphqlURL, &s.McpURL, &s.SubscriptionURL,
			&s.AuthMode, &s.IsDefault, &s.Enabled, &updated)
		s.UpdatedAt = jsTime(updated)
		return s, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if sources == nil {
		sources = []dataSourceRow{}
	}
	sendJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

var (
	dataSourceName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	sourceSecretName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	dataSourceKinds  = []string{"graphjin", "database", "api", "files", "code"}
)

func (a *API) dataSourcePut(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body, ok := readObjectBody(w, r)
	if !ok {
		return
	}
	name, _ := fieldString(body, "name")
	name = jsTrim(name)
	if !dataSourceName.MatchString(name) || len(name) > 64 {
		sendError(w, http.StatusBadRequest, "name must be lowercase letters, digits, dashes")
		return
	}
	authMode := "none"
	if v, _ := fieldString(body, "authMode"); v == "jwt" {
		authMode = "jwt"
	}
	kind := "graphjin"
	if v, ok := fieldString(body, "kind"); ok && slices.Contains(dataSourceKinds, v) {
		kind = v
	}
	graphqlURL, _ := fieldString(body, "graphqlUrl")
	enabledField, _ := field(body, "enabled")
	enabled := enabledField != false
	isDefault, _ := fieldBool(body, "isDefault")

	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	var id string
	var wasDefault bool
	err = tx.QueryRow(ctx, `insert into data_source (org_id, kind, name, label, graphql_url, mcp_url, subscription_url,
		auth_mode, enabled, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		on conflict (org_id, name) do update set label = excluded.label, graphql_url = excluded.graphql_url,
		  mcp_url = excluded.mcp_url, subscription_url = excluded.subscription_url, auth_mode = excluded.auth_mode,
		  enabled = excluded.enabled, updated_at = excluded.updated_at
		returning id::text, is_default`,
		act.OrgID, kind, name, fieldStringOrNil(body, "label"), graphqlURL, fieldStringOrNil(body, "mcpUrl"),
		fieldStringOrNil(body, "subscriptionUrl"), authMode, enabled).Scan(&id, &wasDefault)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if isDefault && !wasDefault {
		if _, err := tx.Exec(ctx, `update data_source set is_default = (id = $2), updated_at = now() where org_id = $1`, act.OrgID, id); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		OK   bool   `json:"ok"`
		ID   string `json:"id"`
		Name string `json:"name"`
	}{true, id, name})
}

func (a *API) sourceSecrets(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	type secret struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		UpdatedAt   string  `json:"updatedAt"`
	}
	rows, err := a.DB.Query(r.Context(), `select name, description, updated_at from data_source_secret
		where org_id = $1 order by updated_at desc`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	secrets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (secret, error) {
		var s secret
		var updated time.Time
		err := row.Scan(&s.Name, &s.Description, &updated)
		s.UpdatedAt = jsTime(updated)
		return s, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if secrets == nil {
		secrets = []secret{}
	}
	sendJSON(w, http.StatusOK, map[string]any{"secrets": secrets})
}

func (a *API) sourceSecretPut(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body, ok := readObjectBody(w, r)
	if !ok {
		return
	}
	name, _ := fieldString(body, "name")
	name = jsTrim(name)
	value, _ := fieldString(body, "value")
	if !sourceSecretName.MatchString(name) || len(name) > 128 {
		sendError(w, http.StatusBadRequest, "name must be letters, digits, . _ - (no spaces)")
		return
	}
	if value == "" {
		sendError(w, http.StatusBadRequest, "value is required")
		return
	}
	enc, err := config.EncryptValue(a.SecretsDir, value)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	description := fieldStringOrNil(body, "description")
	if _, err := a.DB.Exec(r.Context(), `insert into data_source_secret (org_id, name, value_enc, description, updated_at)
		values ($1, $2, $3, $4, now())
		on conflict (org_id, name) do update set value_enc = excluded.value_enc,
		  description = case when $5 then excluded.description else data_source_secret.description end,
		  updated_at = excluded.updated_at`,
		act.OrgID, name, enc, description, description != nil); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		OK   bool   `json:"ok"`
		Name string `json:"name"`
	}{true, name})
}

func (a *API) sourceSecretDelete(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	name := jsTrim(r.URL.Query().Get("name"))
	if name == "" {
		sendError(w, http.StatusBadRequest, "name is required")
		return
	}
	if _, err := a.DB.Exec(r.Context(), `delete from data_source_secret where org_id = $1 and name = $2`,
		act.OrgID, name); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// readSettingsJSON is `await request.json()` inside a try that answers 400
// with the error text.
func readSettingsJSON(w http.ResponseWriter, r *http.Request) (any, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err == nil {
		var v any
		if v, err = jsParse(raw); err == nil {
			return v, true
		}
	}
	sendError(w, http.StatusBadRequest, "request body is not valid JSON")
	return nil, false
}

type installPolicy struct {
	AllowGitURLInstalls bool `json:"allowGitUrlInstalls"`
}

func installPolicyFrom(raw []byte) installPolicy {
	var config map[string]any
	_ = json.Unmarshal(raw, &config)
	allow, _ := config["allowGitUrlInstalls"].(bool)
	return installPolicy{allow}
}

func (a *API) installPolicyGet(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	var raw []byte
	source := "org"
	err := a.DB.QueryRow(r.Context(), `select config from llm_provider_config where org_id = $1 and scope = 'install-policy' limit 1`,
		act.OrgID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		source = "default"
	} else if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Policy installPolicy `json:"policy"`
		Source string        `json:"source"`
	}{installPolicyFrom(raw), source})
}

func (a *API) installPolicyPatch(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	body, ok := readSettingsJSON(w, r)
	if !ok {
		return
	}
	if body == nil {
		sendError(w, http.StatusBadRequest, "Cannot read properties of null (reading 'allowGitUrlInstalls')")
		return
	}
	var allow *bool
	if o, isObj := body.(*jsObj); isObj {
		if b, isBool := o.vals["allowGitUrlInstalls"].(bool); isBool {
			allow = &b
		}
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('llm_provider_config:install-policy:' || $1))`, act.OrgID); err != nil {
		a.fail(w, r, err)
		return
	}
	var id string
	var raw []byte
	err = tx.QueryRow(ctx, `select id::text, config from llm_provider_config where org_id = $1 and scope = 'install-policy' limit 1`,
		act.OrgID).Scan(&id, &raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.fail(w, r, err)
		return
	}
	policy := installPolicyFrom(raw)
	if allow != nil {
		policy.AllowGitURLInstalls = *allow
	}
	stored, _ := json.Marshal(policy)
	if id != "" {
		_, err = tx.Exec(ctx, `update llm_provider_config set config = $2, updated_at = now() where id = $1`, id, stored)
	} else {
		_, err = tx.Exec(ctx, `insert into llm_provider_config (org_id, scope, provider, enabled, config, secrets)
			values ($1, 'install-policy', 'install-policy', true, $2, '{}')`, act.OrgID, stored)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Policy installPolicy `json:"policy"`
		Source string        `json:"source"`
	}{policy, "org"})
}

type skillLearnSettings struct {
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

func (a *API) skillLearnGet(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	out := skillLearnSettings{Source: "org"}
	err := a.DB.QueryRow(r.Context(), `select enabled from skill_learn_org where org_id = $1`, act.OrgID).Scan(&out.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		out = skillLearnSettings{false, "default"}
	} else if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, out)
}

func (a *API) skillLearnPatch(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	body, ok := readSettingsJSON(w, r)
	if !ok {
		return
	}
	o, isObj := body.(*jsObj)
	if !isObj {
		sendError(w, http.StatusBadRequest, "Skill learning payload must be an object")
		return
	}
	enabled, isBool := o.vals["enabled"].(bool)
	if !isBool {
		sendError(w, http.StatusBadRequest, "enabled must be a boolean")
		return
	}
	var unsupported []string
	for _, k := range o.ordered() {
		if k != "enabled" {
			unsupported = append(unsupported, k)
		}
	}
	if len(unsupported) > 0 {
		sendError(w, http.StatusBadRequest, "Unsupported skill learning setting: "+strings.Join(unsupported, ", "))
		return
	}
	if _, err := a.DB.Exec(r.Context(), `insert into skill_learn_org (org_id, enabled, updated_at) values ($1, $2, now())
		on conflict (org_id) do update set enabled = excluded.enabled, updated_at = excluded.updated_at`,
		act.OrgID, enabled); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, skillLearnSettings{enabled, "org"})
}

type graphjinConfigSettings struct {
	SourceConfigEnabled bool `json:"sourceConfigEnabled"`
}

// graphjinConfigSettingsFrom is graphjinConfigSettingsFromRow() in
// packages/db/src/graphjin-config.ts.
func graphjinConfigSettingsFrom(found, enabled bool, raw []byte) graphjinConfigSettings {
	if !found {
		return graphjinConfigSettings{true}
	}
	if raw == nil {
		return graphjinConfigSettings{enabled}
	}
	var config map[string]any
	_ = json.Unmarshal(raw, &config)
	on, isBool := config["sourceConfigEnabled"].(bool)
	return graphjinConfigSettings{enabled && (!isBool || on)}
}

func (a *API) graphjinConfigGet(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	var enabled bool
	var raw []byte
	source := "org"
	err := a.DB.QueryRow(r.Context(), `select enabled, config from llm_provider_config
		where org_id = $1 and scope = 'graphjin-config' limit 1`, act.OrgID).Scan(&enabled, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		source = "default"
	} else if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Settings graphjinConfigSettings `json:"settings"`
		Source   string                 `json:"source"`
	}{graphjinConfigSettingsFrom(source == "org", enabled, raw), source})
}

func (a *API) graphjinConfigPatch(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	body, ok := readSettingsJSON(w, r)
	if !ok {
		return
	}
	if body == nil {
		sendError(w, http.StatusBadRequest, "Cannot read properties of null (reading 'sourceConfigEnabled')")
		return
	}
	var want *bool
	if o, isObj := body.(*jsObj); isObj {
		if b, isBool := o.vals["sourceConfigEnabled"].(bool); isBool {
			want = &b
		}
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('llm_provider_config:graphjin-config:' || $1))`, act.OrgID); err != nil {
		a.fail(w, r, err)
		return
	}
	var id string
	var enabled bool
	var raw []byte
	err = tx.QueryRow(ctx, `select id::text, enabled, config from llm_provider_config
		where org_id = $1 and scope = 'graphjin-config' limit 1`, act.OrgID).Scan(&id, &enabled, &raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.fail(w, r, err)
		return
	}
	settings := graphjinConfigSettingsFrom(id != "", enabled, raw)
	if want != nil {
		settings.SourceConfigEnabled = *want
	}
	stored, _ := json.Marshal(settings)
	if id != "" {
		_, err = tx.Exec(ctx, `update llm_provider_config set enabled = $2, config = $3, updated_at = now() where id = $1`,
			id, settings.SourceConfigEnabled, stored)
	} else {
		_, err = tx.Exec(ctx, `insert into llm_provider_config (org_id, scope, provider, enabled, config, secrets)
			values ($1, 'graphjin-config', 'graphjin-config', $2, $3, '{}')`, act.OrgID, settings.SourceConfigEnabled, stored)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Settings graphjinConfigSettings `json:"settings"`
		Source   string                 `json:"source"`
	}{settings, "org"})
}
