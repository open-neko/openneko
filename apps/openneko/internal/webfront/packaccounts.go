package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const (
	PackOAuthCookie       = "openneko_pack_oauth_state"
	packOAuthTTLSeconds   = 10 * 60
	packPendingTTL        = 10 * time.Minute
	personalConnectionsUI = "/chat/connectors"
)

var (
	packIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	nonEnvChars   = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// packError is a refusal the route reports to the caller with status 400,
// as the Node route reports every error it catches.
type packError string

func (e packError) Error() string { return string(e) }

type packConnection struct {
	Key                 string            `json:"key"`
	AuthorizationURL    string            `json:"authorizationUrl"`
	AuthorizationParams map[string]string `json:"authorizationParams"`
	ClientSecret        string            `json:"clientSecret"`
	Scopes              []string          `json:"scopes"`
	paramOrder          []string
}

type packInstall struct {
	id             string
	definitionOnly bool
	connections    []packConnection
}

// personalPackAccount matches the POST and DELETE handlers in
// apps/web/src/app/api/v1/my/pack-accounts/[packId]/[connectionKey]/route.ts.
func (a *API) personalPackAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.currentUser(ctx, r, orgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if u == nil {
		sendError(w, http.StatusBadRequest, "Sign in to OpenNeko to manage your personal connections.")
		return
	}
	packID, key := r.PathValue("packId"), r.PathValue("connectionKey")
	if !packIDPattern.MatchString(packID) || !packIDPattern.MatchString(key) {
		sendError(w, http.StatusBadRequest, "Invalid pack connection")
		return
	}
	if r.Method != http.MethodDelete {
		held, err := a.holdsPack(ctx, orgID, u.ID, packID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if !held {
			sendError(w, http.StatusNotFound, "Pack not found")
			return
		}
	}
	redirectURI := a.packCallbackURI(r, packID, key)
	callback, err := url.Parse(redirectURI)
	if err != nil || r.Header.Get("Origin") != originOf(callback) {
		sendError(w, http.StatusForbidden, "Invalid request origin")
		return
	}
	if r.Method == http.MethodDelete {
		err = a.disconnectPersonal(ctx, orgID, u.ID, packID, key)
		if a.packFailed(w, r, err) {
			return
		}
		sendJSON(w, http.StatusOK, map[string]bool{"disconnected": true})
		return
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
	authorizationURL, err := a.beginPersonal(ctx, orgID, u.ID, packID, key, state,
		base64.RawURLEncoding.EncodeToString(challenge[:]), redirectURI, callback)
	if a.packFailed(w, r, err) {
		return
	}
	returnPath := "/integrations"
	if r.URL.Query().Get("returnTo") == personalConnectionsUI {
		returnPath = personalConnectionsUI
	}
	cookie, err := a.packStateCookie(map[string]any{
		"packId": packID, "connectionKey": key, "state": state, "codeVerifier": verifier,
		"returnPath": returnPath, "orgId": orgID, "userId": u.ID, "personal": true,
		"expiresAt": time.Now().Add(packOAuthTTLSeconds * time.Second).UnixMilli(),
	})
	if a.packFailed(w, r, err) {
		return
	}
	a.setCookie(w, PackOAuthCookie, cookie, packOAuthTTLSeconds)
	w.Header().Set("Cache-Control", "private, no-store")
	sendJSON(w, http.StatusOK, map[string]string{"authorizationUrl": authorizationURL})
}

func (a *API) packFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var refusal packError
	if errors.As(err, &refusal) {
		sendError(w, http.StatusBadRequest, refusal.Error())
	} else {
		a.fail(w, r, err)
	}
	return true
}

// holdsPack matches holdsItem("pack", id) in apps/web/src/lib/entitlements.ts.
func (a *API) holdsPack(ctx context.Context, orgID, userID, packID string) (bool, error) {
	if role, err := a.role(ctx, orgID, userID); err != nil || role == "admin" {
		return err == nil, err
	}
	var held bool
	err := a.DB.QueryRow(ctx, `
		select exists (
		  select 1 from item_grant gr
		  join user_group g on g.id = gr.group_id and g.org_id = $1
		  join app_user u on u.id = $2 and u.org_id = g.org_id and u.disabled_at is null
		  where gr.org_id = $1 and gr.item_type = 'pack' and gr.item_id in ($3, '*')
		    and (g.slug = 'everyone'
		      or exists (select 1 from user_group_membership m where m.group_id = g.id and m.user_id = u.id)))`,
		orgID, userID, packID).Scan(&held)
	return held, err
}

// packCallbackURI matches packOAuthCallbackUri() in apps/web/src/lib/pack-oauth.ts.
func (a *API) packCallbackURI(r *http.Request, packID, key string) string {
	base := a.PublicURL
	if base == "" {
		base = os.Getenv("OPENNEKO_PUBLIC_URL")
	}
	base = strings.TrimRight(base, "/")
	if base == "" {
		host := strings.TrimSpace(strings.SplitN(r.Header.Get("X-Forwarded-Host"), ",", 2)[0])
		if host == "" {
			host = r.Host
		}
		proto := strings.TrimSpace(strings.SplitN(r.Header.Get("X-Forwarded-Proto"), ",", 2)[0])
		if proto != "http" && proto != "https" {
			proto = "http"
			if r.TLS != nil {
				proto = "https"
			}
		}
		base = proto + "://" + host
	}
	return base + "/api/pack-accounts/" + encodeURIComponent(packID) + "/" + encodeURIComponent(key) + "/callback"
}

// originOf matches the WHATWG URL origin for http and https.
func originOf(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "null"
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" || (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + port
}

// packStateCookie matches writePackOAuthState() in apps/web/src/lib/pack-oauth.ts.
func (a *API) packStateCookie(value map[string]any) (string, error) {
	secret := a.Gate.sessionSecret()
	if len(secret) < 32 {
		return "", packError("OPENNEKO_SESSION_SECRET must be at least 32 characters")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + macOf(secret, body), nil
}

func (a *API) installedPack(ctx context.Context, orgID, userID, packID, key string) (*packInstall, *packConnection, error) {
	var active bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from app_user where id = $1 and org_id = $2 and disabled_at is null)`,
		userID, orgID).Scan(&active); err != nil {
		return nil, nil, err
	}
	if !active {
		return nil, nil, packError("Sign in with an active account to manage personal connections")
	}
	var id string
	var config json.RawMessage
	err := a.DB.QueryRow(ctx, `select id::text, config from pack_install where org_id = $1 and pack_id = $2 and status = 'installed' limit 1`,
		orgID, packID).Scan(&id, &config)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, packError("This pack is not installed")
	}
	if err != nil {
		return nil, nil, err
	}
	install := &packInstall{id: id}
	var parsed struct {
		DefinitionOnly any             `json:"_definitionOnly"`
		UserOAuth      json.RawMessage `json:"_userOAuth"`
	}
	_ = json.Unmarshal(config, &parsed)
	install.definitionOnly = truthy(parsed.DefinitionOnly)
	var entries []json.RawMessage
	if json.Unmarshal(parsed.UserOAuth, &entries) == nil {
		for _, entry := range entries {
			var c packConnection
			if json.Unmarshal(entry, &c) != nil {
				continue
			}
			c.paramOrder = objectKeys(entry, "authorizationParams")
			install.connections = append(install.connections, c)
		}
	}
	for i := range install.connections {
		if install.connections[i].Key == key {
			return install, &install.connections[i], nil
		}
	}
	return nil, nil, packError("This personal connection is unavailable; ask an admin to upgrade the pack")
}

func (a *API) beginPersonal(ctx context.Context, orgID, userID, packID, key, state, challenge, redirectURI string, callback *url.URL) (string, error) {
	install, conn, err := a.installedPack(ctx, orgID, userID, packID, key)
	if err != nil {
		return "", err
	}
	if install.definitionOnly {
		return "", packError("Ask an admin to finish configuring this pack first")
	}
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return "", err
	}
	section := store["pack."+packID]
	clientID := section["OAUTH_"+envKey(conn.Key)+"_CLIENT_ID"]
	if clientID == "" || section[envKey(conn.ClientSecret)] == "" {
		return "", packError("Ask an admin to configure this connection first")
	}
	host := callback.Hostname()
	if callback.Scheme != "https" && !(callback.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1")) {
		return "", packError("OAuth callback must use HTTPS except on localhost")
	}
	revision, err := newUUID()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(state))
	err = a.lockedConnection(ctx, orgID, userID, install.id, key, `
		pending_state_hash = $5, pending_expires_at = $6, revision = $7::uuid`,
		hex.EncodeToString(sum[:]), time.Now().Add(packPendingTTL), revision)
	if err != nil {
		return "", err
	}
	return authorizationURL(conn, clientID, redirectURI, state, challenge)
}

func (a *API) disconnectPersonal(ctx context.Context, orgID, userID, packID, key string) error {
	install, _, err := a.installedPack(ctx, orgID, userID, packID, key)
	if err != nil {
		return err
	}
	revision, err := newUUID()
	if err != nil {
		return err
	}
	return a.lockedConnection(ctx, orgID, userID, install.id, key, `
		revision = $5::uuid, credentials = null, account_id = null, account_label = null,
		pending_state_hash = null, pending_expires_at = null`, revision)
}

// lockedConnection matches locked() in packages/llm/src/graphjin/pack-user-connections.ts.
// The row stays as a tombstone on disconnect, so an old callback never becomes valid again.
func (a *API) lockedConnection(ctx context.Context, orgID, userID, installID, key, set string, args ...any) error {
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var active bool
		err := tx.QueryRow(ctx, `
			select true from app_user u join pack_install i on i.org_id = u.org_id
			where u.id = $1 and u.org_id = $2 and u.disabled_at is null and i.id = $3::uuid and i.status = 'installed'
			for share of u, i`, userID, orgID, installID).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return packError("The user or pack is no longer active")
		}
		if err != nil {
			return err
		}
		ids := []any{orgID, userID, installID, key}
		if _, err := tx.Exec(ctx, `
			insert into pack_user_connection (org_id, user_id, pack_install_id, connection_key)
			values ($1, $2, $3::uuid, $4) on conflict do nothing`, ids...); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			select 1 from pack_user_connection
			where org_id = $1 and user_id = $2 and pack_install_id = $3::uuid and connection_key = $4 for update`, ids...); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update pack_user_connection set `+set+`, updated_at = now()
			where org_id = $1 and user_id = $2 and pack_install_id = $3::uuid and connection_key = $4`, append(ids, args...)...)
		return err
	})
}

// authorizationURL matches buildPackAuthorizationUrl() in packages/packs/src/oauth-client.ts,
// with URLSearchParams order and encoding.
func authorizationURL(c *packConnection, clientID, redirectURI, state, challenge string) (string, error) {
	u, err := url.Parse(c.AuthorizationURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", packError("Invalid URL")
	}
	params := parseSearch(u.RawQuery)
	for _, k := range c.paramOrder {
		params = params.set(k, c.AuthorizationParams[k])
	}
	for _, kv := range [][2]string{
		{"response_type", "code"}, {"client_id", clientID}, {"redirect_uri", redirectURI},
		{"scope", strings.Join(c.Scopes, " ")}, {"state", state},
		{"code_challenge", challenge}, {"code_challenge_method", "S256"},
	} {
		params = params.set(kv[0], kv[1])
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.RawQuery = params.encode()
	u.ForceQuery = false
	return u.String(), nil
}

type searchParams [][2]string

func parseSearch(raw string) searchParams {
	var out searchParams
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		out = append(out, [2]string{formDecode(k), formDecode(v)})
	}
	return out
}

func formDecode(s string) string {
	if d, err := url.QueryUnescape(s); err == nil {
		return d
	}
	return strings.ReplaceAll(s, "+", " ")
}

func (p searchParams) set(k, v string) searchParams {
	out := p[:0:0]
	found := false
	for _, kv := range p {
		if kv[0] != k {
			out = append(out, kv)
		} else if !found {
			out = append(out, [2]string{k, v})
			found = true
		}
	}
	if !found {
		out = append(out, [2]string{k, v})
	}
	return out
}

func (p searchParams) encode() string {
	parts := make([]string, len(p))
	for i, kv := range p {
		parts[i] = formEncode(kv[0]) + "=" + formEncode(kv[1])
	}
	return strings.Join(parts, "&")
}

// formEncode matches the application/x-www-form-urlencoded serializer of URLSearchParams.
func formEncode(s string) string {
	return strings.NewReplacer("~", "%7E", "%2A", "*").Replace(url.QueryEscape(s))
}

func envKey(key string) string {
	return strings.ToUpper(nonEnvChars.ReplaceAllString(key, "_"))
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	default:
		return true
	}
}

// objectKeys lists the keys of the object at name in their JSON order,
// since JavaScript keeps insertion order when it copies them to the URL.
func objectKeys(raw json.RawMessage, name string) []string {
	var outer map[string]json.RawMessage
	if json.Unmarshal(raw, &outer) != nil {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(outer[name])))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return keys
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return keys
		}
		if k, ok := t.(string); ok {
			keys = append(keys, k)
		}
	}
	return keys
}
