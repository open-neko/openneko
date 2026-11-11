package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

const (
	ssoStatusTimeout = 2 * time.Second
	ssoListTimeout   = 30 * time.Second
	ssoPostTimeout   = 20 * time.Second
)

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

// ssoWorker matches the worker bridge in apps/web/src/lib/sso-setup.ts:
// a failure gives 500 with the worker's error text.
func (a *API) ssoWorker(ctx context.Context, method, path string, timeout time.Duration, body any, what string) (json.RawMessage, error) {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	status, reply, err := a.workerDo(ctx, method, path, timeout, raw)
	if err != nil {
		log.Printf("%s: %v", what, err)
		return nil, fmt.Errorf("%s failed: the worker did not answer", what)
	}
	if status < 200 || status > 299 {
		detail := string(reply)
		var parsed struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(reply, &parsed) == nil && parsed.Error != "" {
			detail = parsed.Error
		}
		if detail == "" {
			detail = http.StatusText(status)
		}
		return nil, fmt.Errorf("%s failed (%d): %s", what, status, detail)
	}
	if !json.Valid(reply) {
		return nil, fmt.Errorf("%s failed: the worker gave no JSON", what)
	}
	return reply, nil
}

func (a *API) ssoAnswer(w http.ResponseWriter, reply json.RawMessage, err error) {
	if err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(reply)
}

func (a *API) ssoSetupStatus(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	reply, err := a.ssoWorker(r.Context(), http.MethodGet,
		"/admin/sso/setup/status?orgId="+encodeURIComponent(orgID), ssoStatusTimeout, nil, "SSO setup status")
	a.ssoAnswer(w, reply, err)
}

func (a *API) ssoSetupEnvironments(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	reply, err := a.ssoWorker(r.Context(), http.MethodGet,
		"/admin/sso/setup/environments?orgId="+encodeURIComponent(orgID), ssoListTimeout, nil, "SSO environment list")
	if err == nil {
		reply, err = listField(reply, "environments")
	}
	a.ssoAnswer(w, reply, err)
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
	reply, err := a.ssoWorker(r.Context(), http.MethodGet,
		"/admin/sso/setup/organizations?orgId="+encodeURIComponent(orgID)+"&environmentId="+encodeURIComponent(env),
		ssoListTimeout, nil, "SSO organization list")
	if err == nil {
		reply, err = listField(reply, "organizations")
	}
	a.ssoAnswer(w, reply, err)
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
	reply, err := a.ssoWorker(r.Context(), http.MethodGet,
		"/admin/sso/setup/credentials-info?orgId="+encodeURIComponent(orgID)+"&environmentId="+encodeURIComponent(env),
		ssoListTimeout, nil, "SSO credentials fetch")
	a.ssoAnswer(w, reply, err)
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
	_, err := a.ssoWorker(r.Context(), http.MethodPost, "/admin/sso/setup/ids", ssoPostTimeout, map[string]string{
		"orgId": orgID, "environmentId": env, "organizationId": org, "tier": tier,
	}, "SSO environment selection")
	a.ssoAnswer(w, json.RawMessage(`{"ok":true}`), err)
}

func (a *API) ssoSetupCredentials(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	body, _ := readBody(r)
	values := map[string]string{"orgId": orgID}
	for _, name := range []string{"environmentUrl", "clientId", "clientSecret"} {
		v, ok := nonEmptyString(body, name)
		if !ok {
			sendError(w, http.StatusBadRequest, name+" (string) is required")
			return
		}
		values[name] = v
	}
	_, err := a.ssoWorker(r.Context(), http.MethodPost, "/admin/sso/setup/credentials", ssoPostTimeout, values, "SSO sign-in credentials")
	a.ssoAnswer(w, json.RawMessage(`{"ok":true}`), err)
}

func (a *API) ssoSetupPortalLink(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	reply, err := a.ssoWorker(r.Context(), http.MethodPost, "/admin/sso/setup/generate-portal-link", ssoPostTimeout,
		map[string]string{"orgId": orgID}, "SSO portal link generation")
	a.ssoAnswer(w, reply, err)
}

func (a *API) ssoSetupCheck(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	reply, err := a.ssoWorker(r.Context(), http.MethodPost, "/admin/sso/setup/check", ssoPostTimeout,
		map[string]string{"orgId": orgID}, "SSO connection check")
	a.ssoAnswer(w, reply, err)
}

func nonEmptyString(body any, name string) (string, bool) {
	v, _ := field(body, name)
	s, ok := v.(string)
	return s, ok && s != ""
}

// listField gives {name: reply[name] ?? []}, as the Node routes do.
func listField(reply json.RawMessage, name string) (json.RawMessage, error) {
	var parsed map[string]json.RawMessage
	_ = json.Unmarshal(reply, &parsed)
	list := parsed[name]
	if len(list) == 0 || string(list) == "null" {
		list = json.RawMessage("[]")
	}
	key, _ := json.Marshal(name)
	return json.RawMessage("{" + string(key) + ":" + string(list) + "}"), nil
}
