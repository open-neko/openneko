package webfront

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const ssoListTimeout = 30 * time.Second

// requireAdmin matches requireAdminActor() in apps/web/src/lib/admin-auth.ts.
func (a *API) requireAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID, err := a.OrgID(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return "", false
	}
	u, err := a.currentUser(r.Context(), r, orgID)
	if err != nil {
		a.fail(w, r, err)
		return "", false
	}
	if u == nil {
		sendError(w, http.StatusForbidden, "admin only")
		return "", false
	}
	role, err := a.role(r.Context(), orgID, u.ID)
	if err != nil {
		a.fail(w, r, err)
		return "", false
	}
	if role != "admin" {
		sendError(w, http.StatusForbidden, "admin only")
		return "", false
	}
	return orgID, true
}

func (a *API) ssoWriteJSON(w http.ResponseWriter, what string, out any, err error) {
	if err != nil {
		sendError(w, http.StatusInternalServerError, fmt.Sprintf("%s failed (500): %s", what, err.Error()))
		return
	}
	raw, err := json.Marshal(out)
	if err != nil {
		sendError(w, http.StatusInternalServerError, fmt.Sprintf("%s failed (500): %s", what, err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (a *API) ssoSetupStatus(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	out, err := a.ssoGetStatus(r.Context(), orgID)
	a.ssoWriteJSON(w, "SSO setup status", out, err)
}

func (a *API) ssoSetupEnvironments(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	envs, err := a.ssoListEnvironments(r.Context(), orgID)
	a.ssoWriteJSON(w, "SSO environment list", map[string]any{"environments": envs}, err)
}

func (a *API) ssoSetupOrganizations(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	env := r.URL.Query().Get("environmentId")
	if env == "" {
		sendError(w, http.StatusBadRequest, "environmentId query parameter is required")
		return
	}
	orgs, err := a.ssoListOrganizations(r.Context(), orgID, env)
	a.ssoWriteJSON(w, "SSO organization list", map[string]any{"organizations": orgs}, err)
}

func (a *API) ssoSetupCredentialsInfo(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	env := r.URL.Query().Get("environmentId")
	if env == "" {
		sendError(w, http.StatusBadRequest, "environmentId query parameter is required")
		return
	}
	creds, err := a.ssoGetEnvironmentCredentials(r.Context(), orgID, env)
	a.ssoWriteJSON(w, "SSO credentials fetch", creds, err)
}

func (a *API) ssoSetupIDs(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	body, _ := readBody(r)
	env, ok := nonEmptyString(body, "environmentId")
	if !ok {
		sendError(w, http.StatusBadRequest, "environmentId (string) is required")
		return
	}
	org, ok := nonEmptyString(body, "organizationId")
	if !ok {
		sendError(w, http.StatusBadRequest, "organizationId (string) is required")
		return
	}
	tier, ok := nonEmptyString(body, "tier")
	if !ok {
		tier = "dev"
	}
	err := a.ssoSetScalekitIDs(r.Context(), orgID, env, org, tier)
	a.ssoWriteJSON(w, "SSO environment selection", map[string]bool{"ok": true}, err)
}

func (a *API) ssoSetupCredentials(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	body, _ := readBody(r)
	values := map[string]string{}
	for _, name := range []string{"environmentUrl", "clientId", "clientSecret"} {
		v, ok := nonEmptyString(body, name)
		if !ok {
			sendError(w, http.StatusBadRequest, name+" (string) is required")
			return
		}
		values[name] = v
	}
	err := a.ssoSetSignInCredentials(r.Context(), orgID, values["environmentUrl"], values["clientId"], values["clientSecret"])
	a.ssoWriteJSON(w, "SSO sign-in credentials", map[string]bool{"ok": true}, err)
}

func (a *API) ssoSetupPortalLink(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	out, err := a.ssoGeneratePortalLink(r.Context(), orgID)
	a.ssoWriteJSON(w, "SSO portal link generation", out, err)
}

func (a *API) ssoSetupCheck(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	out, err := a.ssoCheckConnection(r.Context(), orgID)
	a.ssoWriteJSON(w, "SSO connection check", out, err)
}

func nonEmptyString(body any, name string) (string, bool) {
	v, _ := field(body, name)
	s, ok := v.(string)
	return s, ok && s != ""
}
