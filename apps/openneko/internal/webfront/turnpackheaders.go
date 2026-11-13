package webfront

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

type connectionBinding struct {
	InstallID     string `json:"installId"`
	ConnectionKey string `json:"connectionKey"`
	Revision      string `json:"revision"`
}

var errApprovedConnectionGone = errors.New("The approved pack connection is no longer available")

// packConnectionHeader matches packConnectionHeader() in packages/packs/src/oauth-client.ts.
func packConnectionHeader(packID, key string) string {
	sum := sha256.Sum256([]byte(jsStringify([]any{packID, key}, false)))
	return "X-OpenNeko-Pack-" + hex.EncodeToString(sum[:])[:24]
}

// verifyGraphjinToken matches verifyGraphjinToken() in packages/llm/src/graphjin/token.ts.
func (a *API) verifyGraphjinToken(token, orgID string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	secret, err := config.SigningSecret(a.SecretsDir, "graphjin:"+orgID)
	if err != nil {
		return nil
	}
	mac := hmac.New(sha256.New, []byte(base64.StdEncoding.EncodeToString(secret)))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal([]byte(parts[2]), []byte(base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))) {
		return nil
	}
	claims := jwtPayload(parts[1])
	if claims == nil || claims["org_id"] != orgID {
		return nil
	}
	if account, ok := claims["account_id"]; ok && account != orgID {
		return nil
	}
	if exp, ok := claims["exp"]; ok && jsValueNumber(exp)*1000 <= float64(time.Now().UnixMilli()) {
		return nil
	}
	return claims
}

func jwtPayload(part string) map[string]any {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(part, "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil
	}
	return claims
}

// packUserConnectionHeaders matches packUserConnectionHeaders() in
// packages/llm/src/graphjin/pack-user-connections.ts: the only bridge from a
// verified actor token to that user's own pack account, for the exact
// registered endpoint, with no fallback to another account.
func (a *API) packUserConnectionHeaders(ctx context.Context, baseURL string, headers map[string]string, expected []connectionBinding) (map[string]string, error) {
	safe := map[string]string{}
	authorization := ""
	for k, v := range headers {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "x-openneko-pack-") {
			continue
		}
		safe[k] = v
		if lower == "authorization" {
			authorization = v
		}
	}
	unavailable := func() (map[string]string, error) {
		if len(expected) > 0 {
			return nil, errApprovedConnectionGone
		}
		return safe, nil
	}
	token, isBearer := strings.CutPrefix(authorization, "Bearer ")
	if !isBearer {
		return unavailable()
	}
	tokenParts := strings.Split(token, ".")
	if len(tokenParts) < 2 {
		return unavailable()
	}
	unverified := jwtPayload(tokenParts[1])
	orgID, ok := unverified["org_id"].(string)
	if !ok {
		return unavailable()
	}
	claims := a.verifyGraphjinToken(token, orgID)
	if claims == nil {
		return unavailable()
	}
	rows, err := a.DB.Query(ctx, `select i.id::text, i.pack_id, i.config from pack_install i
		join data_source d on d.org_id = i.org_id and d.id::text = i.config->'_runtime'->'source'->>'id'
		where i.org_id = $1 and i.status <> 'removed' and d.enabled and d.auth_mode = 'jwt'
		  and d.graphql_url = i.config->'_runtime'->'source'->>'graphqlUrl'
		  and d.auth_mode = i.config->'_runtime'->'source'->>'authMode'
		  and (d.graphql_url = $2 or d.mcp_url = $2)`, orgID, baseURL)
	if err != nil {
		return nil, err
	}
	type personal struct {
		installID, packID string
		conn              packConnection
	}
	var connections []personal
	found, legacy := false, false
	for rows.Next() {
		var id, packID string
		var raw json.RawMessage
		if err := rows.Scan(&id, &packID, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		found = true
		var cfg struct {
			UserOAuth json.RawMessage `json:"_userOAuth"`
			OAuth     any             `json:"_oauth"`
		}
		_ = json.Unmarshal(raw, &cfg)
		var list []json.RawMessage
		if json.Unmarshal(cfg.UserOAuth, &list) != nil || list == nil {
			if m, isMap := cfg.OAuth.(map[string]any); isMap && len(m) > 0 {
				legacy = true
			}
			continue
		}
		for _, c := range parsePackInstall(id, raw).connections {
			connections = append(connections, personal{id, packID, c})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return unavailable()
	}
	if legacy {
		return nil, errors.New("Ask an admin to upgrade the OAuth pack to confirm its account scope")
	}
	if len(connections) == 0 {
		return unavailable()
	}
	userID, ok := claims["sub"].(string)
	if !ok || userID == "service" {
		return unavailable()
	}
	var active bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from app_user where id = $1 and org_id = $2 and disabled_at is null)`,
		userID, orgID).Scan(&active); err != nil {
		return nil, err
	}
	if !active {
		return nil, errors.New("Sign in with an active account to manage personal connections")
	}
	matched := map[connectionBinding]bool{}
	for _, p := range connections {
		var binding *connectionBinding
		for i := range expected {
			if expected[i].InstallID == p.installID && expected[i].ConnectionKey == p.conn.Key {
				binding = &expected[i]
				matched[connectionBinding{InstallID: binding.InstallID, ConnectionKey: binding.ConnectionKey}] = true
				break
			}
		}
		accessToken, err := a.packAccessToken(ctx, orgID, userID, p.installID, p.packID, &p.conn, binding)
		if err != nil {
			if binding != nil {
				return nil, err
			}
			continue
		}
		if accessToken != "" {
			safe[packConnectionHeader(p.packID, p.conn.Key)] = accessToken
		}
	}
	for _, b := range expected {
		if !matched[connectionBinding{InstallID: b.InstallID, ConnectionKey: b.ConnectionKey}] {
			return nil, errApprovedConnectionGone
		}
	}
	return safe, nil
}

// packAccessToken reads the user's token for one connection under the row
// lock and refreshes it when less than a minute remains, so concurrent calls
// refresh once.
func (a *API) packAccessToken(ctx context.Context, orgID, userID, installID, packID string, conn *packConnection, binding *connectionBinding) (string, error) {
	var accessToken string
	err := a.withConnection(ctx, orgID, userID, installID, conn.Key, func(row connectionRow, update func(string, ...any) error) error {
		if binding != nil && (binding.Revision != row.revision || row.credentials == nil) {
			return errors.New("The approved account connection changed; request approval again")
		}
		if row.credentials == nil {
			return nil
		}
		plain, err := config.MaybeDecryptValue(a.SecretsDir, *row.credentials)
		if err != nil {
			return err
		}
		var tokens packTokens
		if err := json.Unmarshal([]byte(plain), &tokens); err != nil {
			return err
		}
		expires, err := time.Parse(time.RFC3339Nano, tokens.ExpiresAt)
		if err != nil || !expires.After(time.Now().Add(time.Minute)) {
			if tokens.RefreshToken == "" {
				return errors.New("Reconnect your account on Integrations")
			}
			store, err := secrets.Read(a.SecretsDir)
			if err != nil {
				return err
			}
			section := store["pack."+packID]
			refreshed, err := refreshPackToken(ctx, conn, section["OAUTH_"+envKey(conn.Key)+"_CLIENT_ID"], section[envKey(conn.ClientSecret)],
				tokens.RefreshToken, tokens.Scopes)
			if err != nil {
				return err
			}
			if !holdsAll(refreshed.Scopes, conn.Scopes) {
				return errors.New("Reconnect your account to grant the required permissions")
			}
			tokens = *refreshed
			raw, err := json.Marshal(tokens)
			if err != nil {
				return err
			}
			sealed, err := config.EncryptValue(a.SecretsDir, string(raw))
			if err != nil {
				return err
			}
			if err := update("credentials = $5", sealed); err != nil {
				return err
			}
		}
		accessToken = tokens.AccessToken
		return nil
	})
	return accessToken, err
}
