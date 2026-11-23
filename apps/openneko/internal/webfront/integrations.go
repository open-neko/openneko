package webfront

import (
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

func (a *API) connectProvider(name string) *connectProvider {
	for _, p := range a.connectProviders() {
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
	providers, deployment := a.connectProviders(), a.deploymentConnectStatus()
	held, err := a.heldItems(ctx, act, "integration")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	byOperator, byDeployment := map[string]string{}, connectedMap(deployment)
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
	if _, ok := a.connectOperator(w, r, a.connectProvider(name), name, false); !ok {
		return
	}
	removed, err := a.disconnectConnector(name)
	if err != nil {
		log.Printf("connect disconnect: %v", err)
	}
	sendJSON(w, http.StatusOK, map[string]bool{"removed": removed})
}

func (a *API) connectCallbackURI(r *http.Request, name string) string {
	return a.publicBaseURL(r) + "/api/integrations/connect/" + encodeURIComponent(name) + "/callback"
}

func (a *API) integrationConnectStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("plugin")
	p := a.connectProvider(name)
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
	authURL, oauthState, err := a.beginConnect(ctx, name, connectBegin{a.connectCallbackURI(r, name), state, verifier})
	if err != nil {
		sendError(w, http.StatusBadGateway, "connect begin failed: "+err.Error())
		return
	}
	cookie, err := a.connectStateCookie(connectState{name, operatorID, state, verifier, returnPath, oauthState})
	if err != nil {
		sendError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.setCookie(w, r, ConnectStateCookie, cookie, connectStateTTLSeconds)
	w.Header().Set("Location", authURL)
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
	if a.connectProvider(name) == nil {
		appRedirect(w, "/integrations?error=plugin+removed")
		return
	}
	err := a.completeConnect(r.Context(), name, connectComplete{code, a.connectCallbackURI(r, name), stored.CodeVerifier, stored.OAuthState})
	if err != nil {
		appRedirect(w, stored.ReturnPath+"?error="+encodeURIComponent("connect complete failed: "+err.Error()))
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
