package webfront

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	ConnectStateCookie     = "openneko_connect_state"
	connectStateTTLSeconds = 10 * 60
	deploymentOperatorID   = "deployment"
	connectStatusTimeout   = 2 * time.Second
	connectFlowTimeout     = 60 * time.Second
	connectRemoveTimeout   = 5 * time.Second
)

type connectProvider struct {
	PluginID        string   `json:"pluginId"`
	PluginName      string   `json:"pluginName"`
	ProviderLabel   string   `json:"providerLabel"`
	Scopes          []string `json:"scopes"`
	Flow            string   `json:"flow"`
	CredentialScope string   `json:"credentialScope"`
}

type connectStatus struct {
	PluginName  string `json:"pluginName"`
	ConnectedAt string `json:"connectedAt"`
}

type connectView struct {
	PluginID        string   `json:"pluginId"`
	PluginName      string   `json:"pluginName"`
	ProviderLabel   string   `json:"providerLabel"`
	Scopes          []string `json:"scopes"`
	Flow            string   `json:"flow"`
	CredentialScope string   `json:"credentialScope"`
	Connected       bool     `json:"connected"`
	ConnectedAt     *string  `json:"connectedAt"`
}

// connectState matches ConnectStateCookiePayload in apps/web/src/lib/integrations.ts.
type connectState struct {
	PluginName   string `json:"pluginName"`
	OperatorID   string `json:"operatorId"`
	State        string `json:"state"`
	CodeVerifier string `json:"codeVerifier"`
	ReturnPath   string `json:"returnPath"`
	OAuthState   string `json:"oauthState,omitempty"`
}

func (a *API) workerList(ctx context.Context, path, field string, out any) {
	status, reply, err := a.workerDo(ctx, http.MethodGet, path, connectStatusTimeout, nil)
	if err != nil || status < 200 || status > 299 {
		return
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(reply, &body) == nil && body[field] != nil {
		_ = json.Unmarshal(body[field], out)
	}
}

func (a *API) connectProviders(ctx context.Context) []connectProvider {
	var providers []connectProvider
	a.workerList(ctx, "/admin/connect/providers", "providers", &providers)
	return providers
}

func (a *API) connectProvider(ctx context.Context, name string) *connectProvider {
	for _, p := range a.connectProviders(ctx) {
		if p.PluginName == name {
			return &p
		}
	}
	return nil
}

func connectedMap(list []connectStatus) map[string]string {
	m := make(map[string]string, len(list))
	for _, s := range list {
		m[s.PluginName] = s.ConnectedAt
	}
	return m
}

func (a *API) integrationsList(w http.ResponseWriter, r *http.Request) {
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
	var operator, deployment []connectStatus
	providers := a.connectProviders(ctx)
	a.workerList(ctx, "/admin/connect/status/"+encodeURIComponent(act.UserID), "connected", &operator)
	a.workerList(ctx, "/admin/connect/deployment/status", "connected", &deployment)
	held, err := a.heldItems(ctx, act, "integration")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	byOperator, byDeployment := connectedMap(operator), connectedMap(deployment)
	workspace, connectors := []connectView{}, []connectView{}
	for _, p := range providers {
		if !held.has(p.PluginName) {
			continue
		}
		connected := byOperator
		if p.CredentialScope == "deployment" {
			connected = byDeployment
		}
		view := connectView{p.PluginID, p.PluginName, p.ProviderLabel, p.Scopes, p.Flow, p.CredentialScope, false, nil}
		if at, ok := connected[p.PluginName]; ok {
			view.Connected, view.ConnectedAt = true, &at
		}
		if p.CredentialScope == "deployment" {
			workspace = append(workspace, view)
		} else {
			connectors = append(connectors, view)
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{"workspace": workspace, "connectors": connectors})
}

// connectOperator applies the gates of the Node start and disconnect routes:
// a deployment connector needs an admin and uses the shared slot; any other
// needs a signed-in user. checkHeld adds the start route's item check.
func (a *API) connectOperator(w http.ResponseWriter, r *http.Request, p *connectProvider, name string, checkHeld bool) (string, bool) {
	if p != nil && p.CredentialScope == "deployment" {
		_, ok := a.requireAdmin(w, r)
		return deploymentOperatorID, ok
	}
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return "", false
	}
	if act.UserID == "" {
		sendError(w, http.StatusUnauthorized, "not signed in")
		return "", false
	}
	if checkHeld {
		held, err := a.heldItems(r.Context(), act, "integration")
		if err != nil {
			a.fail(w, r, err)
			return "", false
		}
		if !held.has(name) {
			sendError(w, http.StatusNotFound, `connect plugin "`+name+`" not installed`)
			return "", false
		}
	}
	return act.UserID, true
}

func (a *API) integrationDisconnect(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("plugin")
	operatorID, ok := a.connectOperator(w, r, a.connectProvider(r.Context(), name), name, false)
	if !ok {
		return
	}
	body, _ := json.Marshal(map[string]string{"pluginName": name, "operatorId": operatorID})
	status, reply, err := a.workerDo(r.Context(), http.MethodPost, "/admin/connect/disconnect", connectRemoveTimeout, body)
	if err != nil {
		log.Printf("connect disconnect: %v", err)
		sendError(w, http.StatusBadGateway, "worker is unavailable")
		return
	}
	var out struct {
		Removed bool `json:"removed"`
	}
	if status >= 200 && status <= 299 {
		_ = json.Unmarshal(reply, &out)
	}
	sendJSON(w, http.StatusOK, out)
}

func (a *API) connectCallbackURI(r *http.Request, name string) string {
	return a.publicBaseURL(r) + "/api/integrations/connect/" + encodeURIComponent(name) + "/callback"
}

func (a *API) integrationConnectStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("plugin")
	p := a.connectProvider(ctx, name)
	if p == nil {
		sendError(w, http.StatusNotFound, `connect plugin "`+name+`" not installed`)
		return
	}
	operatorID, ok := a.connectOperator(w, r, p, name, true)
	if !ok {
		return
	}
	returnPath := r.URL.Query().Get("returnTo")
	if !internalPath(returnPath) || strings.Contains(returnPath, `\`) {
		returnPath = "/integrations"
	}
	state, err := randomURL(24)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	verifier, err := randomURL(32)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	scopes := p.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	var begun struct {
		AuthorizationURL string `json:"authorizationUrl"`
		OAuthState       string `json:"oauthState"`
	}
	err = a.workerCall(ctx, "/admin/connect/begin", connectFlowTimeout, "connect begin", map[string]any{
		"pluginName": name,
		"params": map[string]any{"operatorId": operatorID, "redirectUri": a.connectCallbackURI(r, name),
			"state": state, "scopes": scopes, "codeVerifier": verifier},
	}, &begun)
	if err != nil {
		sendError(w, http.StatusBadGateway, err.Error())
		return
	}
	cookie, err := a.connectStateCookie(connectState{name, operatorID, state, verifier, returnPath, begun.OAuthState})
	if err != nil {
		sendError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.setCookie(w, ConnectStateCookie, cookie, connectStateTTLSeconds)
	w.Header().Set("Location", begun.AuthorizationURL)
	w.WriteHeader(http.StatusFound)
}

func (a *API) integrationConnectCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		description := e
		if q.Has("error_description") {
			description = q.Get("error_description")
		}
		appRedirect(w, "/integrations?error="+encodeURIComponent(description))
		return
	}
	name := r.PathValue("plugin")
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		appRedirect(w, "/integrations?error=missing+code+or+state")
		return
	}
	stored := a.readAndClearConnectState(w, r)
	switch {
	case stored == nil:
		appRedirect(w, "/integrations?error=state+cookie+missing+or+expired")
		return
	case stored.PluginName != name:
		appRedirect(w, "/integrations?error=plugin+mismatch")
		return
	case stored.State != state:
		appRedirect(w, "/integrations?error=state+mismatch")
		return
	}
	p := a.connectProvider(r.Context(), name)
	if p == nil {
		appRedirect(w, "/integrations?error=plugin+removed")
		return
	}
	scopes := p.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	params := map[string]any{"operatorId": stored.OperatorID, "code": code, "redirectUri": a.connectCallbackURI(r, name),
		"state": state, "codeVerifier": stored.CodeVerifier, "scopes": scopes}
	if stored.OAuthState != "" {
		params["oauthState"] = stored.OAuthState
	}
	var done json.RawMessage
	if err := a.workerCall(r.Context(), "/admin/connect/complete", connectFlowTimeout, "connect complete",
		map[string]any{"pluginName": name, "params": params}, &done); err != nil {
		appRedirect(w, stored.ReturnPath+"?error="+encodeURIComponent(err.Error()))
		return
	}
	appRedirect(w, stored.ReturnPath+"?connected="+encodeURIComponent(name))
}

// connectStateCookie matches writeConnectStateCookie() in
// apps/web/src/lib/integrations.ts, so a flow begun before an upgrade ends.
func (a *API) connectStateCookie(s connectState) (string, error) {
	secret, err := a.signingSecret()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + macOf(secret, body), nil
}

func (a *API) readAndClearConnectState(w http.ResponseWriter, r *http.Request) *connectState {
	c, err := r.Cookie(ConnectStateCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: ConnectStateCookie, Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1})
	split := strings.LastIndex(c.Value, ".")
	if split <= 0 {
		return nil
	}
	body, mac := c.Value[:split], c.Value[split+1:]
	secret, err := a.signingSecret()
	if err != nil || subtle.ConstantTimeCompare([]byte(mac), []byte(macOf(secret, body))) != 1 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	s := &connectState{}
	targets := map[string]*string{"pluginName": &s.PluginName, "operatorId": &s.OperatorID, "state": &s.State,
		"codeVerifier": &s.CodeVerifier, "returnPath": &s.ReturnPath, "oauthState": &s.OAuthState}
	for key, target := range targets {
		v, present := fields[key]
		if !present {
			if key == "oauthState" {
				continue
			}
			return nil
		}
		if json.Unmarshal(v, target) != nil || v[0] != '"' {
			return nil
		}
	}
	return s
}
