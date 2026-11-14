package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const scalekitWorkspaceLabel = "Scalekit workspace"

var scalekitWorkspaceScopes = []string{"wks:read", "wks:write", "env:read", "env:write", "org:read", "org:write"}

type connectBegin struct {
	RedirectURI  string
	State        string
	CodeVerifier string
}

type connectComplete struct {
	Code         string
	RedirectURI  string
	CodeVerifier string
	OAuthState   string
}

// mcpOAuthState matches OauthState in the worker's former mcp-oauth.ts, so a
// flow begun before an upgrade ends.
type mcpOAuthState struct {
	MCPURL                 string   `json:"mcpUrl"`
	AuthorizationServerURL string   `json:"authorizationServerUrl"`
	TokenEndpoint          string   `json:"tokenEndpoint"`
	ClientID               string   `json:"clientId"`
	ClientSecret           *string  `json:"clientSecret"`
	CodeVerifier           string   `json:"codeVerifier"`
	Scopes                 []string `json:"scopes"`
	RedirectURI            string   `json:"redirectUri"`
}

func (a *API) connectProviders() []connectProvider {
	return []connectProvider{{
		PluginID: scalekitPlugin, PluginName: scalekitPlugin, ProviderLabel: scalekitWorkspaceLabel,
		Scopes: append([]string(nil), scalekitWorkspaceScopes...), Flow: "mcp-oauth", CredentialScope: "deployment",
	}}
}

func (a *API) deploymentConnectStatus() []connectStatus {
	cred, err := readScalekitOperatorCredential(a.secretsDir())
	if err != nil || cred == nil {
		return nil
	}
	return []connectStatus{{PluginName: scalekitPlugin, ConnectedAt: cred.ConnectedAt}}
}

func requireConnector(name string) error {
	if name != scalekitPlugin {
		return fmt.Errorf("unknown connector %s", name)
	}
	return nil
}

func (a *API) beginConnect(ctx context.Context, name string, p connectBegin) (string, string, error) {
	if err := requireConnector(name); err != nil {
		return "", "", err
	}
	if p.RedirectURI == "" {
		return "", "", errors.New("params.redirectUri is required")
	}
	if p.State == "" {
		return "", "", errors.New("params.state is required")
	}
	store := secrets.ReadSoft(a.secretsDir(), nil)
	mcpURL := resolveScalekitMCPURL(store[signInProviderSection(scalekitPlugin)])
	resourceURL, err := mcpResourceMetadataURL(mcpURL)
	if err != nil {
		return "", "", err
	}
	var resource map[string]any
	if err := a.mcpOAuthCall(ctx, http.MethodGet, resourceURL, "", nil, "resource discovery", &resource); err != nil {
		return "", "", err
	}
	servers, scopes := nonEmptyStrings(resource["authorization_servers"]), nonEmptyStrings(resource["scopes_supported"])
	if len(servers) == 0 {
		return "", "", errors.New("protected resource metadata had no authorization_servers")
	}
	asURL := servers[0]
	as, err := a.discoverAuthorizationServer(ctx, asURL)
	if err != nil {
		return "", "", err
	}
	clientID := ""
	var clientSecret *string
	if as.RegistrationEndpoint != "" {
		body, _ := json.Marshal(map[string]any{
			"client_name": "openneko-scalekit", "redirect_uris": []string{p.RedirectURI},
			"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
			"token_endpoint_auth_method": "none",
		})
		var registered map[string]any
		if err := a.mcpOAuthCall(ctx, http.MethodPost, as.RegistrationEndpoint, "application/json", body, "dynamic client registration", &registered); err != nil {
			return "", "", err
		}
		clientID, _ = registered["client_id"].(string)
		if clientID == "" {
			return "", "", errors.New("DCR response had no client_id")
		}
		if secret, ok := registered["client_secret"].(string); ok {
			clientSecret = &secret
		}
	}
	if clientID == "" {
		return "", "", errors.New("Scalekit MCP authorization server did not support client registration (DCR)")
	}
	verifier := p.CodeVerifier
	if verifier == "" {
		if verifier, err = randomURL(32); err != nil {
			return "", "", err
		}
	}
	authURL, err := mcpAuthorizationURL(as.AuthorizationEndpoint, clientID, p.RedirectURI, p.State, verifier, scopes)
	if err != nil {
		return "", "", err
	}
	raw, err := json.Marshal(mcpOAuthState{mcpURL, asURL, as.TokenEndpoint, clientID, clientSecret, verifier, scopes, p.RedirectURI})
	if err != nil {
		return "", "", err
	}
	return authURL, base64.RawURLEncoding.EncodeToString(raw), nil
}

func (a *API) completeConnect(ctx context.Context, name string, p connectComplete) error {
	if err := requireConnector(name); err != nil {
		return err
	}
	if p.Code == "" {
		return errors.New("params.code is required")
	}
	state, err := parseMCPOAuthState(p.OAuthState)
	if err != nil {
		return err
	}
	redirectURI, verifier := p.RedirectURI, p.CodeVerifier
	if redirectURI == "" {
		redirectURI = state.RedirectURI
	}
	if verifier == "" {
		verifier = state.CodeVerifier
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", state.ClientID)
	form.Set("code", p.Code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	var tokens map[string]any
	if err := a.mcpOAuthCall(ctx, http.MethodPost, state.TokenEndpoint, "application/x-www-form-urlencoded", []byte(form.Encode()), "code exchange", &tokens); err != nil {
		return err
	}
	access, _ := tokens["access_token"].(string)
	if access == "" {
		return errors.New("token endpoint returned no access_token")
	}
	now := time.Now()
	var expiresAt any
	if seconds, ok := tokens["expires_in"].(float64); ok {
		expiresAt = strconv.FormatInt(now.UnixMilli()+int64(seconds*1000), 10)
	}
	clientSecret := ""
	if state.ClientSecret != nil {
		clientSecret = *state.ClientSecret
	}
	refresh := tokens["refresh_token"]
	cred := &scalekitOperatorCredential{
		Tokens: map[string]any{
			"access_token": access, "refresh_token": refresh, "expires_at": expiresAt,
			"token_endpoint": state.TokenEndpoint, "client_id": state.ClientID, "client_secret": clientSecret,
		},
		ProviderLabel: scalekitWorkspaceLabel,
		ConnectedAt:   isoMillis(now),
	}
	if len(state.Scopes) > 0 {
		cred.Scopes = state.Scopes
	}
	return writeScalekitOperatorCredential(a.secretsDir(), cred)
}

func (a *API) disconnectConnector(name string) (bool, error) {
	if name != scalekitPlugin {
		return false, nil
	}
	return removeDeploymentCredential(a.secretsDir(), scalekitPlugin)
}

func removeDeploymentCredential(overrideDir, name string) (bool, error) {
	file := secrets.Path(overrideDir)
	raw, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	full := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &full) != nil {
		return false, nil
	}
	operators := map[string]map[string]json.RawMessage{}
	if opRaw, ok := full[secrets.OperatorsKey]; !ok || json.Unmarshal(opRaw, &operators) != nil {
		return false, nil
	}
	slot := operators[scalekitDeploymentOperatorID]
	if _, ok := slot[name]; !ok {
		return false, nil
	}
	delete(slot, name)
	if len(slot) == 0 {
		delete(operators, scalekitDeploymentOperatorID)
	}
	opBytes, err := json.Marshal(operators)
	if err != nil {
		return false, err
	}
	full[secrets.OperatorsKey] = opBytes
	out, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		return false, err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", file, os.Getpid())
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, file); err != nil {
		return false, err
	}
	return true, nil
}

type mcpAuthorizationServer struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

func (a *API) discoverAuthorizationServer(ctx context.Context, asURL string) (*mcpAuthorizationServer, error) {
	base := strings.TrimRight(asURL, "/")
	wellKnown := base
	if !strings.HasSuffix(base, "/.well-known/oauth-authorization-server") {
		wellKnown = base + "/.well-known/oauth-authorization-server"
	}
	var as mcpAuthorizationServer
	err := a.mcpOAuthCall(ctx, http.MethodGet, wellKnown, "", nil, "authorization server discovery", &as)
	var httpErr *mcpOAuthHTTPError
	if errors.As(err, &httpErr) && httpErr.status == http.StatusNotFound && wellKnown != asURL {
		as = mcpAuthorizationServer{}
		err = a.mcpOAuthCall(ctx, http.MethodGet, asURL, "", nil, "authorization server discovery (raw URL fallback)", &as)
	}
	if err != nil {
		return nil, err
	}
	if as.AuthorizationEndpoint == "" || as.TokenEndpoint == "" {
		return nil, errors.New("authorization server metadata missing endpoints")
	}
	return &as, nil
}

type mcpOAuthHTTPError struct {
	what    string
	status  int
	nonJSON bool
}

func (e *mcpOAuthHTTPError) Error() string {
	if e.nonJSON {
		return fmt.Sprintf("MCP OAuth %s returned non-JSON (HTTP %d)", e.what, e.status)
	}
	return fmt.Sprintf("MCP OAuth %s returned HTTP %d", e.what, e.status)
}

// mcpOAuthCall keeps the response body out of every error: a token endpoint
// may echo the code or a token in it.
func (a *API) mcpOAuthCall(ctx context.Context, method, target, contentType string, body []byte, what string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, scalekitHTTPTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("MCP OAuth %s network error: invalid URL", what)
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := a.signInClient().Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("MCP OAuth %s timed out after %dms", what, scalekitHTTPTimeout.Milliseconds())
		}
		return fmt.Errorf("MCP OAuth %s network error: %s", what, networkCause(err))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if len(raw) > 0 && !json.Valid(raw) {
		return &mcpOAuthHTTPError{what, res.StatusCode, true}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return &mcpOAuthHTTPError{what, res.StatusCode, false}
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("MCP OAuth %s returned an unexpected body (HTTP %d)", what, res.StatusCode)
	}
	return nil
}

func networkCause(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}

func mcpResourceMetadataURL(mcpURL string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(mcpURL), "/")
	if trimmed == "" {
		return "", errors.New("SCALEKIT_MCP_URL is empty")
	}
	if !strings.HasPrefix(strings.ToLower(trimmed), "https://") {
		return "", fmt.Errorf("SCALEKIT_MCP_URL must be an https URL (got %s)", trimmed)
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("SCALEKIT_MCP_URL must be an https URL (got %s)", trimmed)
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment, u.User = "/.well-known/oauth-protected-resource", "", "", "", nil
	return u.String(), nil
}

// mcpAuthorizationURL matches buildAuthorizationUrl() of the worker's former
// mcp-oauth.ts, with URLSearchParams order and encoding.
func mcpAuthorizationURL(endpoint, clientID, redirectURI, state, verifier string, scopes []string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("Invalid URL")
	}
	sum := sha256.Sum256([]byte(verifier))
	params := parseSearch(u.RawQuery)
	for _, kv := range [][2]string{
		{"response_type", "code"}, {"client_id", clientID}, {"redirect_uri", redirectURI}, {"state", state},
		{"code_challenge", base64.RawURLEncoding.EncodeToString(sum[:])}, {"code_challenge_method", "S256"},
	} {
		params = params.set(kv[0], kv[1])
	}
	if len(scopes) > 0 {
		params = params.set("scope", strings.Join(scopes, " "))
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.RawQuery = params.encode()
	u.ForceQuery = false
	return u.String(), nil
}

func parseMCPOAuthState(raw string) (*mcpOAuthState, error) {
	if raw == "" {
		return nil, errors.New("missing oauthState — begin_connect was not run (or its state was lost)")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "="))
	if err != nil {
		return nil, errors.New("oauthState is not valid base64url JSON")
	}
	var v map[string]any
	if json.Unmarshal(decoded, &v) != nil || v == nil {
		return nil, errors.New("oauthState is not valid base64url JSON")
	}
	str := func(k string) string { s, _ := v[k].(string); return s }
	s := &mcpOAuthState{
		MCPURL: str("mcpUrl"), AuthorizationServerURL: str("authorizationServerUrl"), TokenEndpoint: str("tokenEndpoint"),
		ClientID: str("clientId"), CodeVerifier: str("codeVerifier"), RedirectURI: str("redirectUri"), Scopes: []string{},
	}
	if s.TokenEndpoint == "" || s.ClientID == "" || s.CodeVerifier == "" {
		return nil, errors.New("oauthState is missing required fields")
	}
	if secret, ok := v["clientSecret"].(string); ok {
		s.ClientSecret = &secret
	}
	if list, ok := v["scopes"].([]any); ok {
		for _, item := range list {
			if str, ok := item.(string); ok {
				s.Scopes = append(s.Scopes, str)
			}
		}
	}
	return s, nil
}

func nonEmptyStrings(v any) []string {
	out := []string{}
	list, _ := v.([]any)
	for _, item := range list {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
