package webfront

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const (
	packAdminReturn    = "/admin/settings/packs"
	providerTimeout    = 30 * time.Second
	providerReplyLimit = 1 << 20
)

var providerClient = &http.Client{Timeout: providerTimeout}

func packOAuthPath(packID, key, action string) string {
	return "/admin/packs/" + encodeURIComponent(packID) + "/oauth/" + encodeURIComponent(key) + "/" + action
}

// packAdminTarget applies requireAdminActor() and validPackId() for the
// admin pack OAuth routes.
func (a *API) packAdminTarget(w http.ResponseWriter, r *http.Request, invalid string) (actor, string, string, bool) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return act, "", "", false
	}
	packID, key := r.PathValue("packId"), r.PathValue("connectionKey")
	if !packIDPattern.MatchString(packID) || !packIDPattern.MatchString(key) {
		sendError(w, http.StatusBadRequest, invalid)
		return act, "", "", false
	}
	return act, packID, key, true
}

// packOAuthTarget loads the connection for an admin pack OAuth route, or
// sends the failure.
func (a *API) packOAuthTarget(w http.ResponseWriter, r *http.Request, invalid string) (actor, *packOAuth, bool) {
	act, packID, key, ok := a.packAdminTarget(w, r, invalid)
	if !ok {
		return act, nil, false
	}
	p, err := a.packOAuthFor(r.Context(), act.OrgID, packID, key)
	if err != nil {
		a.packOAuthFailed(w, r, err)
		return act, nil, false
	}
	return act, p, true
}

func (a *API) packOAuthFailed(w http.ResponseWriter, r *http.Request, err error) {
	var refusal packError
	if !errors.As(err, &refusal) {
		log.Printf("pack OAuth %s: %v", r.URL.Path, err)
	}
	sendPackFailure(w, http.StatusBadRequest, err, "oauth")
}

func (a *API) packOAuthStatus(w http.ResponseWriter, r *http.Request) {
	_, p, ok := a.packOAuthTarget(w, r, "invalid pack OAuth connection")
	if !ok {
		return
	}
	status, err := p.status()
	if err != nil {
		a.packOAuthFailed(w, r, err)
		return
	}
	status.CallbackURL = a.packCallbackURI(r, p.packID, p.conn.Key)
	sendJSON(w, http.StatusOK, status)
}

func (a *API) packOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	_, p, ok := a.packOAuthTarget(w, r, "invalid pack OAuth connection")
	if !ok {
		return
	}
	removed, err := p.disconnect(context.WithoutCancel(r.Context()))
	if err != nil {
		a.packOAuthFailed(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"disconnected": removed})
}

// packJSONBody is `await request.json().catch(() => null)` for a body read
// with readPackBody: nil when the body is not JSON.
func packJSONBody(w http.ResponseWriter, r *http.Request) (*jsObj, bool) {
	raw, err := readPackBody(r, packBodyLimit)
	if errors.Is(err, errPackTooLarge) {
		sendError(w, http.StatusRequestEntityTooLarge, err.Error())
		return nil, false
	}
	parsed, perr := jsParse(raw)
	if err != nil || perr != nil {
		return nil, true
	}
	obj, _ := parsed.(*jsObj)
	return obj, true
}

func (a *API) packOAuthConfigure(w http.ResponseWriter, r *http.Request) {
	act, packID, key, ok := a.packAdminTarget(w, r, "Invalid pack connection")
	if !ok {
		return
	}
	body, ok := packJSONBody(w, r)
	if !ok {
		return
	}
	var clientID any
	if body != nil {
		clientID = body.vals["clientId"]
	}
	if _, isString := clientID.(string); !isString {
		sendError(w, http.StatusBadRequest, "Client ID is required")
		return
	}
	clientSecret, _ := body.vals["clientSecret"].(string)
	ctx := context.WithoutCancel(r.Context())
	p, err := a.packOAuthFor(ctx, act.OrgID, packID, key)
	if err != nil {
		a.packOAuthFailed(w, r, err)
		return
	}
	status, err := p.configure(ctx, clientID.(string), clientSecret)
	if err != nil {
		a.packOAuthFailed(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, status)
}

func (a *API) packOAuthStart(w http.ResponseWriter, r *http.Request) {
	act, packID, key, ok := a.packAdminTarget(w, r, "invalid pack OAuth connection")
	if !ok {
		return
	}
	body, ok := packJSONBody(w, r)
	if !ok {
		return
	}
	text := func(name string) string {
		if body == nil {
			return ""
		}
		s, _ := body.vals[name].(string)
		return s
	}
	clientID, clientSecret, returnPath := text("clientId"), text("clientSecret"), text("returnTo")
	if clientID == "" {
		sendError(w, http.StatusBadRequest, "clientId is required")
		return
	}
	if !internalPath(returnPath) || strings.Contains(returnPath, `\`) {
		returnPath = packAdminReturn
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
	challenge := sha256.Sum256([]byte(verifier))
	redirectURI := a.packCallbackURI(r, packID, key)
	p, err := a.packOAuthFor(r.Context(), act.OrgID, packID, key)
	if err != nil {
		a.packOAuthFailed(w, r, err)
		return
	}
	authorizationURL, err := p.begin(clientID, clientSecret, redirectURI, state, base64.RawURLEncoding.EncodeToString(challenge[:]))
	if err != nil {
		a.packOAuthFailed(w, r, err)
		return
	}
	cookie, err := a.packStateCookie(map[string]any{
		"packId": packID, "connectionKey": key, "state": state, "codeVerifier": verifier,
		"returnPath": returnPath, "userId": act.UserID, "orgId": act.OrgID,
		"expiresAt": time.Now().Add(packOAuthTTLSeconds * time.Second).UnixMilli(),
	})
	if a.packFailed(w, r, err) {
		return
	}
	a.setCookie(w, PackOAuthCookie, cookie, packOAuthTTLSeconds)
	sendJSON(w, http.StatusOK, map[string]string{"authorizationUrl": authorizationURL})
}

// readAndClearPackState matches readAndClearPackOAuthState() in the former
// apps/web/src/lib/pack-oauth.ts, so a flow that Node began ends in Go.
func (a *API) readAndClearPackState(w http.ResponseWriter, r *http.Request) map[string]any {
	c, err := r.Cookie(PackOAuthCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: PackOAuthCookie, Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1})
	split := strings.LastIndex(c.Value, ".")
	if split < 1 {
		return nil
	}
	body, mac := c.Value[:split], c.Value[split+1:]
	secret := a.Gate.sessionSecret()
	if len(secret) < 32 || subtle.ConstantTimeCompare([]byte(mac), []byte(macOf(secret, body))) != 1 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil
	}
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	for _, k := range []string{"packId", "connectionKey", "state", "codeVerifier", "returnPath"} {
		if s, _ := v[k].(string); s == "" {
			return nil
		}
	}
	if exp, ok := v["expiresAt"].(float64); !ok || exp <= float64(time.Now().UnixMilli()) {
		return nil
	}
	if truthy(v["personal"]) {
		if _, ok := v["userId"].(string); !ok {
			return nil
		}
		if _, ok := v["orgId"].(string); !ok {
			return nil
		}
	}
	return v
}

func (a *API) packOAuthCallback(w http.ResponseWriter, r *http.Request) {
	packID, key := r.PathValue("packId"), r.PathValue("connectionKey")
	saved := a.readAndClearPackState(w, r)
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	if saved == nil || saved["packId"] != packID || saved["connectionKey"] != key || saved["state"] != state || code == "" {
		sendError(w, http.StatusBadRequest, "OAuth callback state is missing or invalid")
		return
	}
	verifier := saved["codeVerifier"].(string)
	redirectURI := a.packCallbackURI(r, packID, key)
	if truthy(saved["personal"]) {
		err := a.completePersonal(context.WithoutCancel(r.Context()), r, saved, packID, key, code, state, verifier, redirectURI)
		if err == nil {
			appRedirect(w, "/integrations?connected="+encodeURIComponent(key))
			return
		}
		var refusal packError
		message := "Connection failed"
		if errors.As(err, &refusal) {
			message = refusal.Error()
		} else {
			log.Printf("pack connection %s/%s: %v", packID, key, err)
		}
		appRedirect(w, "/integrations?"+searchParams{{"error", message}}.encode())
		return
	}
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	if saved["userId"] != act.UserID || saved["orgId"] != act.OrgID {
		sendError(w, http.StatusForbidden, "OAuth session changed")
		return
	}
	ctx := context.WithoutCancel(r.Context())
	p, err := a.packOAuthFor(ctx, act.OrgID, packID, key)
	if err == nil {
		err = p.complete(ctx, code, verifier, redirectURI)
	}
	if err != nil {
		a.packOAuthFailed(w, r, err)
		return
	}
	appRedirect(w, withSearch(saved["returnPath"].(string), searchParams{{"pack", packID}, {"connected", key}}))
}

// withSearch sets each param on the query of an app path, as
// URL.searchParams.set() does.
func withSearch(path string, add searchParams) string {
	if strings.Contains(path, `\`) {
		path = packAdminReturn
	}
	path, fragment, hasFragment := strings.Cut(path, "#")
	path, query, _ := strings.Cut(path, "?")
	params := parseSearch(query)
	for _, kv := range add {
		params = params.set(kv[0], kv[1])
	}
	out := path + "?" + params.encode()
	if hasFragment {
		out += "#" + fragment
	}
	return out
}

func (a *API) completePersonal(ctx context.Context, r *http.Request, saved map[string]any, packID, key, code, state, verifier, redirectURI string) error {
	orgID, err := a.OrgID(ctx)
	if err != nil {
		return err
	}
	u, err := a.currentUser(ctx, r, orgID)
	if err != nil {
		return err
	}
	if u == nil {
		return packError("Sign in to OpenNeko to manage your personal connections.")
	}
	if saved["userId"] != u.ID || saved["orgId"] != orgID {
		return packError("Sign in as the user who started this connection")
	}
	install, conn, err := a.installedPack(ctx, orgID, u.ID, packID, key)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(state))
	stateHash := hex.EncodeToString(sum[:])
	var revision string
	err = a.withConnection(ctx, orgID, u.ID, install.id, key, func(row connectionRow, update func(string, ...any) error) error {
		if row.pendingHash == nil || *row.pendingHash != stateHash || row.pendingExpires == nil || !row.pendingExpires.After(time.Now()) {
			return packError("OAuth connection expired; connect again")
		}
		revision = row.revision
		return update("pending_state_hash = null, pending_expires_at = null")
	})
	if err != nil {
		return err
	}
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return err
	}
	section := store["pack."+packID]
	tokens, err := exchangePackCode(ctx, conn, section["OAUTH_"+envKey(conn.Key)+"_CLIENT_ID"], section[envKey(conn.ClientSecret)], code, verifier, redirectURI)
	if err != nil {
		return err
	}
	if tokens.RefreshToken == "" || !holdsAll(tokens.Scopes, conn.Scopes) {
		return packError("Grant all requested permissions and offline access, then connect again")
	}
	accountID, accountLabel, err := fetchPackAccount(ctx, conn, tokens.AccessToken)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	sealed, err := config.EncryptValue(a.SecretsDir, string(raw))
	if err != nil {
		return err
	}
	return a.withConnection(ctx, orgID, u.ID, install.id, key, func(row connectionRow, update func(string, ...any) error) error {
		if row.revision != revision {
			return packError("The connection changed; connect again")
		}
		return update("credentials = $5, account_id = $6, account_label = $7", sealed, accountID, accountLabel)
	})
}

func holdsAll(have, want []string) bool {
	set := make(map[string]bool, len(have))
	for _, s := range have {
		set[s] = true
	}
	for _, s := range want {
		if !set[s] {
			return false
		}
	}
	return true
}

// packTokens matches PackOAuthTokens in packages/packs/src/oauth-client.ts;
// the worker decrypts and reads this JSON.
type packTokens struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken,omitempty"`
	ExpiresAt    string   `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
}

// providerJSON matches providerJson() in packages/packs/src/oauth-client.ts.
func providerJSON(req *http.Request, operation string) (map[string]any, error) {
	res, err := providerClient.Do(req)
	if err != nil {
		log.Printf("%s: %v", operation, err)
		return nil, packError(operation + " failed: the provider did not answer")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, packError(fmt.Sprintf("%s failed with HTTP %d", operation, res.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, providerReplyLimit))
	var v map[string]any
	if err != nil || json.Unmarshal(raw, &v) != nil || v == nil {
		return nil, packError(operation + " returned an invalid JSON response")
	}
	return v, nil
}

func exchangePackCode(ctx context.Context, conn *packConnection, clientID, clientSecret, code, verifier, redirectURI string) (*packTokens, error) {
	return packTokenRequest(ctx, conn, "OAuth code exchange", "", conn.Scopes, [][2]string{{"grant_type", "authorization_code"},
		{"client_id", clientID}, {"client_secret", clientSecret}, {"code", code}, {"code_verifier", verifier}, {"redirect_uri", redirectURI}})
}

func refreshPackToken(ctx context.Context, conn *packConnection, clientID, clientSecret, refreshToken string, scopes []string) (*packTokens, error) {
	return packTokenRequest(ctx, conn, "OAuth token refresh", refreshToken, scopes, [][2]string{{"grant_type", "refresh_token"},
		{"client_id", clientID}, {"client_secret", clientSecret}, {"refresh_token", refreshToken}})
}

// packTokenRequest matches parseTokens() in packages/packs/src/oauth-client.ts.
func packTokenRequest(ctx context.Context, conn *packConnection, operation, fallbackRefresh string, fallbackScopes []string, fields [][2]string) (*packTokens, error) {
	var form searchParams
	for _, kv := range fields {
		if kv[1] != "" {
			form = append(form, kv)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, conn.TokenURL, strings.NewReader(form.encode()))
	if err != nil {
		return nil, packError(operation + " failed: the token URL is invalid")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	v, err := providerJSON(req, operation)
	if err != nil {
		return nil, err
	}
	access, _ := v["access_token"].(string)
	if access == "" {
		return nil, packError("OAuth token response did not contain an access token")
	}
	expiresIn := jsValueNumber(v["expires_in"])
	if math.IsNaN(expiresIn) || math.IsInf(expiresIn, 0) || expiresIn <= 0 {
		return nil, packError("OAuth token response did not contain a valid expiry")
	}
	scopes := fallbackScopes
	if s, ok := v["scope"].(string); ok {
		scopes = strings.Fields(s)
	}
	if scopes == nil {
		scopes = []string{}
	}
	refresh, _ := v["refresh_token"].(string)
	if refresh == "" {
		refresh = fallbackRefresh
	}
	expires := time.Now().Add(time.Duration(expiresIn * float64(time.Second))).UTC()
	return &packTokens{AccessToken: access, RefreshToken: refresh, ExpiresAt: expires.Format("2006-01-02T15:04:05.000Z"), Scopes: scopes}, nil
}

// jsValueNumber is Number(v) for a value from encoding/json.
func jsValueNumber(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case bool:
		if t {
			return 1
		}
		return 0
	case nil:
		return 0
	case string:
		if n, ok := jsNumber(t); ok {
			return n
		}
	}
	return math.NaN()
}

func fetchPackAccount(ctx context.Context, conn *packConnection, accessToken string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, conn.UserInfoURL, nil)
	if err != nil {
		return "", "", packError("OAuth account lookup failed: the user info URL is invalid")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	v, err := providerJSON(req, "OAuth account lookup")
	if err != nil {
		return "", "", err
	}
	id, _ := v[conn.AccountIDField].(string)
	label, _ := v[conn.AccountLabelField].(string)
	if id == "" || label == "" {
		return "", "", packError("OAuth account response did not contain the declared identity fields")
	}
	return id, label, nil
}
