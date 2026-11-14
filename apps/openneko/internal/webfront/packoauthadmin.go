package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

// packSecretsMu serializes the read-modify-write of secrets.json within
// this process.
var packSecretsMu sync.Mutex

// packOAuth is PackOAuthService of apps/worker/src/packs/oauth.ts for one
// connection of one pack.
type packOAuth struct {
	a         *API
	orgID     string
	packID    string
	conn      packs.OAuthConnection
	installed bool
	graphjin  bool
}

func oauthStateKey(connectionKey, field string) string {
	return "OAUTH_" + envKey(connectionKey) + "_" + field
}

func (a *API) packOAuthFor(ctx context.Context, orgID, packID, key string) (*packOAuth, error) {
	var version, source string
	err := a.DB.QueryRow(ctx, `select version, source from pack_install where org_id = $1 and pack_id = $2 and status = 'installed' limit 1`,
		orgID, packID).Scan(&version, &source)
	installed := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if source != "uploaded" {
		version = ""
	}
	bundle, err := packBundle(orgID, packID, version)
	if err != nil {
		return nil, packError(err.Error())
	}
	for _, c := range bundle.Manifest.OAuth {
		if c.Key == key {
			return &packOAuth{a: a, orgID: orgID, packID: packID, conn: c, installed: installed,
				graphjin: bundle.Manifest.Artifacts.GraphJin != nil}, nil
		}
	}
	return nil, packError(fmt.Sprintf("pack %s does not declare OAuth connection %s", packID, key))
}

func (p *packOAuth) section(store secrets.Store) map[string]string {
	if s := store["pack."+p.packID]; s != nil {
		return s
	}
	return map[string]string{}
}

func (p *packOAuth) writeSection(store secrets.Store, section map[string]string) error {
	store["pack."+p.packID] = section
	return secrets.Write(store, p.a.SecretsDir)
}

func (p *packOAuth) clientIDKey() string     { return oauthStateKey(p.conn.Key, "CLIENT_ID") }
func (p *packOAuth) clientSecretKey() string { return envKey(p.conn.ClientSecret) }

type packOAuthAccount struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type packOAuthState struct {
	Scope         string            `json:"scope"`
	Configured    bool              `json:"configured"`
	Key           string            `json:"key"`
	ProviderLabel string            `json:"providerLabel"`
	ClientID      *string           `json:"clientId"`
	Scopes        []string          `json:"scopes"`
	Connected     bool              `json:"connected"`
	Account       *packOAuthAccount `json:"account"`
	ExpiresAt     *string           `json:"expiresAt"`
	CallbackURL   string            `json:"callbackUrl,omitempty"`
}

func storedValue(section map[string]string, key string) *string {
	if v, ok := section[key]; ok {
		return &v
	}
	return nil
}

// binding is packOAuthBinding(): the account that an org-scope connection holds.
func (p *packOAuth) binding(section map[string]string) map[string]any {
	id, label := section[oauthStateKey(p.conn.Key, "ACCOUNT_ID")], section[oauthStateKey(p.conn.Key, "ACCOUNT_LABEL")]
	if id == "" || label == "" {
		return nil
	}
	return map[string]any{"key": p.conn.Key, "providerLabel": p.conn.ProviderLabel, "accountId": id, "accountLabel": label,
		"scopes": strings.Fields(section[oauthStateKey(p.conn.Key, "SCOPES")])}
}

func (p *packOAuth) status() (*packOAuthState, error) {
	store, err := secrets.Read(p.a.SecretsDir)
	if err != nil {
		return nil, err
	}
	section := p.section(store)
	var binding map[string]any
	if p.conn.Scope != "user" {
		binding = p.binding(section)
	}
	out := &packOAuthState{
		Scope: p.conn.Scope, Key: p.conn.Key, ProviderLabel: p.conn.ProviderLabel,
		Configured: section[p.clientIDKey()] != "" && section[p.clientSecretKey()] != "",
		ClientID:   storedValue(section, p.clientIDKey()), Scopes: p.conn.Scopes,
		Connected: binding != nil && section[envKey(p.conn.RefreshToken)] != "",
		ExpiresAt: storedValue(section, oauthStateKey(p.conn.Key, "EXPIRES_AT")),
	}
	if out.Scopes == nil {
		out.Scopes = []string{}
	}
	if binding != nil {
		out.Account = &packOAuthAccount{binding["accountId"].(string), binding["accountLabel"].(string)}
	}
	return out, nil
}

func (p *packOAuth) configure(ctx context.Context, clientIDInput, clientSecretInput string) (*packOAuthState, error) {
	if p.conn.Scope == "user" && !p.installed {
		return nil, packError("Install this pack before configuring its OAuth client")
	}
	packSecretsMu.Lock()
	defer packSecretsMu.Unlock()
	store, err := secrets.Read(p.a.SecretsDir)
	if err != nil {
		return nil, err
	}
	saved := p.section(store)
	clientID, clientSecret := strings.TrimSpace(clientIDInput), strings.TrimSpace(clientSecretInput)
	if clientSecret == "" {
		clientSecret = saved[p.clientSecretKey()]
	}
	if clientID == "" || clientSecret == "" {
		return nil, packError("OAuth client ID and client secret are required")
	}
	if p.conn.Scope == "user" && (clientID != saved[p.clientIDKey()] || clientSecret != saved[p.clientSecretKey()]) {
		if _, err := p.a.DB.Exec(ctx, `delete from pack_user_connection c using pack_install i
			where c.pack_install_id = i.id and c.org_id = $1 and i.org_id = $1 and i.pack_id = $2 and c.connection_key = $3`,
			p.orgID, p.packID, p.conn.Key); err != nil {
			return nil, err
		}
	}
	next := maps.Clone(saved)
	next[p.clientIDKey()], next[p.clientSecretKey()] = clientID, clientSecret
	if err := p.writeSection(store, next); err != nil {
		return nil, err
	}
	return p.status()
}

func (p *packOAuth) begin(clientIDInput, clientSecretInput, redirectURI, state, challenge string) (string, error) {
	if p.conn.Scope == "user" {
		return "", packError("Connect your personal account on Integrations")
	}
	packSecretsMu.Lock()
	defer packSecretsMu.Unlock()
	store, err := secrets.Read(p.a.SecretsDir)
	if err != nil {
		return "", err
	}
	saved := p.section(store)
	clientID, clientSecret := strings.TrimSpace(clientIDInput), strings.TrimSpace(clientSecretInput)
	if clientID == "" {
		clientID = saved[p.clientIDKey()]
	}
	if clientSecret == "" {
		clientSecret = saved[p.clientSecretKey()]
	}
	if clientID == "" || clientSecret == "" || redirectURI == "" || state == "" || challenge == "" {
		return "", packError("OAuth client ID, client secret, redirect URI, state and PKCE challenge are required")
	}
	callback, err := url.Parse(redirectURI)
	if err != nil {
		return "", packError("Invalid URL")
	}
	host := callback.Hostname()
	if callback.Scheme != "https" && !(callback.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1")) {
		return "", packError("OAuth callback must use HTTPS except on localhost")
	}
	next := maps.Clone(saved)
	next[p.clientIDKey()], next[p.clientSecretKey()] = clientID, clientSecret
	if err := p.writeSection(store, next); err != nil {
		return "", err
	}
	return authorizationURL(p.connection(), clientID, redirectURI, state, challenge)
}

// connection gives the manifest connection in the shape that the personal
// flow uses. The manifest map holds no key order, so the authorization
// parameters go to the URL in key order.
func (p *packOAuth) connection() *packConnection {
	c := &packConnection{Key: p.conn.Key, AuthorizationURL: p.conn.AuthorizationURL, ClientSecret: p.conn.ClientSecret,
		Scopes: p.conn.Scopes, TokenURL: p.conn.TokenURL, UserInfoURL: p.conn.UserInfoURL,
		AccountIDField: p.conn.AccountIDField, AccountLabelField: p.conn.AccountLabelField}
	if p.conn.AuthorizationParams != nil {
		c.AuthorizationParams = *p.conn.AuthorizationParams
		for k := range c.AuthorizationParams {
			c.paramOrder = append(c.paramOrder, k)
		}
		slices.Sort(c.paramOrder)
	}
	return c
}

func (p *packOAuth) complete(ctx context.Context, code, verifier, redirectURI string) error {
	if p.conn.Scope == "user" {
		return packError("Connect your personal account on Integrations")
	}
	if code == "" || verifier == "" || redirectURI == "" {
		return packError("OAuth code, verifier and redirect URI are required")
	}
	store, err := secrets.Read(p.a.SecretsDir)
	if err != nil {
		return err
	}
	section := p.section(store)
	clientID, clientSecret := section[p.clientIDKey()], section[p.clientSecretKey()]
	if clientID == "" || clientSecret == "" {
		return packError("OAuth client configuration is missing; start the connection again")
	}
	conn := p.connection()
	tokens, err := exchangePackCode(ctx, conn, clientID, clientSecret, code, verifier, redirectURI)
	if err != nil {
		return err
	}
	if tokens.RefreshToken == "" {
		return packError("OAuth provider did not return a refresh token; grant offline access and consent again")
	}
	var missing []string
	for _, s := range p.conn.Scopes {
		if !slices.Contains(tokens.Scopes, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return packError("OAuth consent did not grant required scope(s): " + strings.Join(missing, ", "))
	}
	accountID, accountLabel, err := fetchPackAccount(ctx, conn, tokens.AccessToken)
	if err != nil {
		return err
	}
	pending := oauthStateKey(p.conn.Key, "SYNC_PENDING")
	final, err := p.updateSection(func(s map[string]string) {
		s[envKey(p.conn.AccessToken)] = tokens.AccessToken
		s[envKey(p.conn.RefreshToken)] = tokens.RefreshToken
		s[oauthStateKey(p.conn.Key, "EXPIRES_AT")] = tokens.ExpiresAt
		s[oauthStateKey(p.conn.Key, "SCOPES")] = strings.Join(tokens.Scopes, " ")
		s[oauthStateKey(p.conn.Key, "ACCOUNT_ID")] = accountID
		s[oauthStateKey(p.conn.Key, "ACCOUNT_LABEL")] = accountLabel
		if p.installed {
			s[pending] = "true"
		}
	})
	if err != nil {
		return err
	}
	if p.installed {
		if err := p.sync(ctx, true); err != nil {
			return err
		}
		if final, err = p.updateSection(func(s map[string]string) { delete(s, pending) }); err != nil {
			return err
		}
	}
	return p.bindInstallation(ctx, final)
}

func (p *packOAuth) updateSection(change func(map[string]string)) (map[string]string, error) {
	packSecretsMu.Lock()
	defer packSecretsMu.Unlock()
	store, err := secrets.Read(p.a.SecretsDir)
	if err != nil {
		return nil, err
	}
	next := maps.Clone(p.section(store))
	change(next)
	return next, p.writeSection(store, next)
}

func (p *packOAuth) disconnect(ctx context.Context) (bool, error) {
	if p.conn.Scope == "user" {
		return false, packError("Connect your personal account on Integrations")
	}
	store, err := secrets.Read(p.a.SecretsDir)
	if err != nil {
		return false, err
	}
	if _, ok := store["pack."+p.packID]; !ok {
		return false, nil
	}
	keys := []string{envKey(p.conn.AccessToken), envKey(p.conn.RefreshToken),
		oauthStateKey(p.conn.Key, "EXPIRES_AT"), oauthStateKey(p.conn.Key, "SCOPES"),
		oauthStateKey(p.conn.Key, "ACCOUNT_ID"), oauthStateKey(p.conn.Key, "ACCOUNT_LABEL")}
	removed := false
	for _, k := range keys {
		if _, ok := store["pack."+p.packID][k]; ok {
			removed = true
		}
	}
	if removed && p.installed {
		if err := p.sync(ctx, false); err != nil {
			return false, err
		}
	}
	next, err := p.updateSection(func(s map[string]string) {
		for _, k := range keys {
			delete(s, k)
		}
		delete(s, oauthStateKey(p.conn.Key, "SYNC_PENDING"))
	})
	if err != nil {
		return false, err
	}
	return removed, p.bindInstallation(ctx, next)
}

func (p *packOAuth) sync(ctx context.Context, enabled bool) error {
	if !p.graphjin {
		return nil
	}
	return p.a.syncPackOAuth(ctx, p.orgID, p.packID, p.conn.Key, enabled)
}

func (p *packOAuth) bindInstallation(ctx context.Context, section map[string]string) error {
	var binding *string
	if b := p.binding(section); b != nil {
		raw, _ := json.Marshal(b)
		binding = new(string(raw))
	}
	_, err := p.a.DB.Exec(ctx, `
		update pack_install set updated_at = now(), config = jsonb_set(config, '{_oauth}',
		  ((case when jsonb_typeof(config->'_oauth') = 'object' then config->'_oauth' else '{}'::jsonb end) - $3::text)
		  || case when $4::jsonb is null then '{}'::jsonb else jsonb_build_object($3::text, $4::jsonb) end)
		where id = (select id from pack_install where org_id = $1 and pack_id = $2 and status = 'installed' limit 1)`,
		p.orgID, p.packID, p.conn.Key, binding)
	return err
}
