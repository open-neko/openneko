package webfront

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

type pendingDevice struct {
	UserCode string `json:"userCode"`
	Name     string `json:"name"`
}

func (a *API) deviceRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	userCode := normalizeUserCode(r.URL.Query().Get("code"))
	if userCode == "" {
		sendJSON(w, http.StatusOK, map[string]any{"request": nil})
		return
	}
	var name string
	err = a.DB.QueryRow(ctx, `
		select name from app_device_request
		where org_id = $1 and user_code = $2 and expires_at > now()
		  and approved_at is null and denied_at is null`, orgID, userCode).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		sendJSON(w, http.StatusOK, map[string]any{"request": nil})
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"request": pendingDevice{
		UserCode: formatUserCode(userCode),
		Name:     name,
	}})
}

type personalPackDecl struct {
	Key           string          `json:"key"`
	ProviderLabel string          `json:"providerLabel"`
	Experience    json.RawMessage `json:"experience"`
	Scopes        []string        `json:"scopes"`
	ClientSecret  string          `json:"clientSecret"`
}

type personalPackStatus struct {
	PackID        string          `json:"packId"`
	Key           string          `json:"key"`
	ProviderLabel string          `json:"providerLabel"`
	Experience    json.RawMessage `json:"experience,omitempty"`
	Scopes        []string        `json:"scopes"`
	Configured    bool            `json:"configured"`
	Connected     bool            `json:"connected"`
	AccountLabel  *string         `json:"accountLabel"`
}

func (a *API) personalPackAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if act.UserID == "" {
		sendError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	held, err := a.heldItems(ctx, act, "pack")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	type userConn struct {
		connected bool
		label     *string
	}
	byKey := map[[2]string]userConn{}
	uRows, err := a.DB.Query(ctx, `
		select pack_install_id::text, connection_key, account_label, credentials is not null
		from pack_user_connection where org_id = $1 and user_id = $2`, act.OrgID, act.UserID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var installID, connKey string
	var label *string
	var connected bool
	if _, err := pgx.ForEachRow(uRows, []any{&installID, &connKey, &label, &connected}, func() error {
		var copied *string
		if label != nil {
			s := *label
			copied = &s
		}
		byKey[[2]string{installID, connKey}] = userConn{connected: connected, label: copied}
		return nil
	}); err != nil {
		a.fail(w, r, err)
		return
	}
	iRows, err := a.DB.Query(ctx, `
		select id::text, pack_id, config from pack_install
		where org_id = $1 and status = 'installed' order by pack_id`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	connections := []personalPackStatus{}
	var packID string
	var rawConfig json.RawMessage
	if _, err := pgx.ForEachRow(iRows, []any{&installID, &packID, &rawConfig}, func() error {
		if !held.has(packID) {
			return nil
		}
		var cfg struct {
			DefinitionOnly any                `json:"_definitionOnly"`
			UserOAuth      []personalPackDecl `json:"_userOAuth"`
		}
		_ = json.Unmarshal(rawConfig, &cfg)
		defOnly := truthy(cfg.DefinitionOnly)
		section := store["pack."+packID]
		for _, c := range cfg.UserOAuth {
			uc := byKey[[2]string{installID, c.Key}]
			scopes := c.Scopes
			if scopes == nil {
				scopes = []string{}
			}
			configured := !defOnly && section["OAUTH_"+envKey(c.Key)+"_CLIENT_ID"] != "" && section[envKey(c.ClientSecret)] != ""
			connections = append(connections, personalPackStatus{
				PackID:        packID,
				Key:           c.Key,
				ProviderLabel: c.ProviderLabel,
				Experience:    c.Experience,
				Scopes:        scopes,
				Configured:    configured,
				Connected:     uc.connected,
				AccountLabel:  uc.label,
			})
		}
		return nil
	}); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"connections": connections})
}

type wizardInitial struct {
	CompanyName          string   `json:"companyName"`
	CompanyNote          string   `json:"companyNote"`
	FiscalYearStartMonth int      `json:"fiscalYearStartMonth"`
	ActiveSeats          []string `json:"activeSeats"`
	Priorities           []string `json:"priorities"`
}

func (a *API) onboardingWizard(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var setupComplete, hasCurrentProfile bool
	var orgName string
	if err := a.DB.QueryRow(ctx, `
		select setup_complete_at is not null, name,
			exists (select 1 from customer_profile where org_id = $1 and is_current)
		from organization where id = $1`, act.OrgID).Scan(&setupComplete, &orgName, &hasCurrentProfile); err != nil {
		a.fail(w, r, err)
		return
	}
	if !setupComplete {
		sendJSON(w, http.StatusOK, map[string]any{"kind": "settings"})
		return
	}
	personaUserID := ""
	if a.Gate.Status(ctx).Live() {
		personaUserID = act.UserID
	}
	if personaUserID != "" && hasCurrentProfile {
		var roleTemplate string
		var focusAreas []string
		err := noRows(a.DB.QueryRow(ctx, `
			select role_template, focus_areas from operator_profile
			where org_id = $1 and user_id = $2 limit 1`, act.OrgID, personaUserID).Scan(&roleTemplate, &focusAreas))
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if focusAreas == nil {
			focusAreas = []string{}
		}
		sendJSON(w, http.StatusOK, map[string]any{
			"kind":                "persona",
			"initialRoleTemplate": roleTemplate,
			"initialFocusAreas":   focusAreas,
		})
		return
	}
	initial := wizardInitial{
		CompanyName:          orgName,
		FiscalYearStartMonth: 1,
		ActiveSeats:          []string{},
		Priorities:           []string{},
	}
	if initial.CompanyName == "My Workspace" {
		initial.CompanyName = ""
	}
	err := noRows(a.DB.QueryRow(ctx, `
		select company_note, fiscal_year_start_month, active_seats, priorities
		from onboarding_wizard where org_id = $1 limit 1`, act.OrgID).Scan(
		&initial.CompanyNote, &initial.FiscalYearStartMonth, &initial.ActiveSeats, &initial.Priorities))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if initial.ActiveSeats == nil {
		initial.ActiveSeats = []string{}
	}
	if initial.Priorities == nil {
		initial.Priorities = []string{}
	}
	sendJSON(w, http.StatusOK, map[string]any{
		"kind":     "wizard",
		"initial":  initial,
		"teamMode": personaUserID != "",
	})
}
