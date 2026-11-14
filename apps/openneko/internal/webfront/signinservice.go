package webfront

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const (
	signInSectionName   = "signin"
	scalekitPlugin      = "scalekit"
	scalekitOIDCScopes  = "openid profile email"
	scalekitHTTPTimeout = 20 * time.Second
	scalekitMaxBody     = 1 << 20

	magicLinkMinSecretLen = 32
	magicLinkDefaultTTL   = 600
	magicLinkMinTTL       = 60
	magicLinkMaxTTL       = 600

	deploymentConnectSlot = "deployment"
)

type signInSetting struct {
	Key          string
	Required     bool
	Secret       bool
	Autogenerate bool
}

type signInProviderSpec struct {
	Label             string
	Provisioning      string
	LoginHintRequired bool
	Settings          []signInSetting
}

var (
	scalekitSettings = []signInSetting{
		{Key: "SCALEKIT_ENVIRONMENT_URL", Required: true, Secret: false},
		{Key: "SCALEKIT_CLIENT_ID", Required: true, Secret: false},
		{Key: "SCALEKIT_CLIENT_SECRET", Required: true, Secret: true},
		{Key: "SCALEKIT_ORGANIZATION_ID", Required: false, Secret: false},
		{Key: "SCALEKIT_ENVIRONMENT_ID", Required: false, Secret: false},
		{Key: "SCALEKIT_MCP_URL", Required: false, Secret: false},
	}

	magicLinkSettings = []signInSetting{
		{Key: "MAGIC_LINK_SIGNING_SECRET", Required: true, Secret: true, Autogenerate: true},
		{Key: "MAGIC_LINK_FROM", Required: true, Secret: false},
		{Key: "RESEND_API_KEY", Required: false, Secret: true},
		{Key: "POSTMARK_SERVER_TOKEN", Required: false, Secret: true},
		{Key: "SENDGRID_API_KEY", Required: false, Secret: true},
		{Key: "MAGIC_LINK_TTL_SECONDS", Required: false, Secret: false},
	}

	signInProviders = map[string]signInProviderSpec{
		scalekitPlugin: {
			Label:             "Scalekit",
			Provisioning:      "automatic",
			LoginHintRequired: false,
			Settings:          scalekitSettings,
		},
		magicLinkPlugin: {
			Label:             "Email link",
			Provisioning:      "manual",
			LoginHintRequired: true,
			Settings:          magicLinkSettings,
		},
	}

	legacySignInOrdered = []struct {
		legacy string
		id     string
	}{
		{legacy: "@open-neko/plugin-scalekit", id: scalekitPlugin},
		{legacy: "@open-neko/plugin-magic-link", id: magicLinkPlugin},
	}
)

type signInProviderDescriptor struct {
	PluginName        string `json:"pluginName"`
	ProviderLabel     string `json:"providerLabel"`
	Provisioning      string `json:"provisioning"`
	LoginHintRequired bool   `json:"loginHintRequired"`
}

type signInPendingDescriptor struct {
	PluginName        string   `json:"pluginName"`
	ProviderLabel     string   `json:"providerLabel"`
	Provisioning      string   `json:"provisioning"`
	LoginHintRequired bool     `json:"loginHintRequired"`
	MissingEnv        []string `json:"missingEnv"`
	NeedsAdminUser    bool     `json:"needsAdminUser"`
	ConfiguredEnv     []string `json:"configuredEnv"`
}

func signInProviderSection(id string) string {
	return signInSectionName + "." + id
}

func isSignInProviderID(id string) bool {
	_, ok := signInProviders[id]
	return ok
}

func (a *API) secretsDir() string {
	if a.SecretsDir != "" {
		if a.Gate != nil && a.Gate.SecretsDir == "" {
			a.Gate.SecretsDir = a.SecretsDir
		}
		return a.SecretsDir
	}
	if a.Gate != nil && a.Gate.SecretsDir != "" {
		return a.Gate.SecretsDir
	}
	return ""
}

func (a *API) signInClient() *http.Client {
	if a.Gate != nil && a.Gate.Client != nil {
		return a.Gate.Client
	}
	return http.DefaultClient
}

func (a *API) now() time.Time {
	if a.Gate != nil {
		return a.Gate.now()
	}
	return time.Now()
}

func (g *Gate) fetchStatus(ctx context.Context) (AuthStatus, error) {
	store, err := secrets.Read(g.SecretsDir)
	if err != nil {
		return AuthStatus{}, err
	}
	id := store[signInSectionName]["PROVIDER"]
	spec, ok := signInProviders[id]
	if !ok {
		return AuthStatus{}, nil
	}
	values := store[signInProviderSection(id)]
	missingEnv := []string{}
	configuredEnv := []string{}
	for _, s := range spec.Settings {
		if values[s.Key] != "" {
			configuredEnv = append(configuredEnv, s.Key)
		} else if s.Required {
			missingEnv = append(missingEnv, s.Key)
		}
	}
	envReady := len(missingEnv) == 0
	soloNeeds, err := g.soloAdminNeedsEmail(ctx)
	if err != nil {
		return AuthStatus{}, err
	}
	manual := spec.Provisioning == "manual"
	hasAdmin := true
	if !soloNeeds && manual {
		hasAdmin, err = g.hasProvisionedAdmin(ctx)
		if err != nil {
			return AuthStatus{}, err
		}
	}
	needsAdminUser := soloNeeds || (manual && !hasAdmin)
	if envReady && !needsAdminUser {
		raw, err := json.Marshal(signInProviderDescriptor{
			PluginName:        id,
			ProviderLabel:     spec.Label,
			Provisioning:      spec.Provisioning,
			LoginHintRequired: spec.LoginHintRequired,
		})
		if err != nil {
			return AuthStatus{}, err
		}
		return AuthStatus{Provider: raw}, nil
	}
	raw, err := json.Marshal(signInPendingDescriptor{
		PluginName:        id,
		ProviderLabel:     spec.Label,
		Provisioning:      spec.Provisioning,
		LoginHintRequired: spec.LoginHintRequired,
		MissingEnv:        missingEnv,
		NeedsAdminUser:    needsAdminUser,
		ConfiguredEnv:     configuredEnv,
	})
	if err != nil {
		return AuthStatus{}, err
	}
	return AuthStatus{Pending: raw}, nil
}

func (g *Gate) soloAdminNeedsEmail(ctx context.Context) (bool, error) {
	if g.DB == nil {
		return false, nil
	}
	var email string
	err := g.DB.QueryRow(ctx, `
		select u.email from organization o
		join app_user u on u.id = o.solo_admin_user_id and u.org_id = o.id
		where o.id = (select id from organization order by created_at, id limit 1)`).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.HasSuffix(email, soloEmailDomain), nil
}

func (g *Gate) hasProvisionedAdmin(ctx context.Context) (bool, error) {
	if g.DB == nil {
		return true, nil
	}
	var found bool
	err := g.DB.QueryRow(ctx, `
		select exists (
			select 1 from user_group_membership m
			join user_group g on g.id = m.group_id
			join app_user u on u.id = m.user_id and u.disabled_at is null
			where m.org_id = (select id from organization order by created_at, id limit 1)
			  and g.slug = 'administrators'
		)`).Scan(&found)
	return found, err
}

func (a *API) selectSignInProvider(id string) error {
	if id != "" && !isSignInProviderID(id) {
		return fmt.Errorf("unknown sign-in provider %s", id)
	}
	dir := a.secretsDir()
	store, err := secrets.Read(dir)
	if err != nil {
		return err
	}
	changed := false
	if id != "" {
		backfilled, err := backfillSignInGenerated(store, id)
		if err != nil {
			return err
		}
		if backfilled {
			changed = true
		}
	}
	current := store[signInSectionName]["PROVIDER"]
	if !isSignInProviderID(current) {
		current = ""
	}
	if current != id {
		if id == "" {
			store, _ = secrets.Unset(store, signInSectionName, "PROVIDER")
		} else {
			store, err = secrets.Set(store, signInSectionName, "PROVIDER", id)
			if err != nil {
				return err
			}
		}
		changed = true
	}
	if changed {
		if err := secrets.Write(store, dir); err != nil {
			return err
		}
	}
	if a.Gate != nil {
		a.Gate.Reset()
	}
	return nil
}

func backfillSignInGenerated(store secrets.Store, id string) (bool, error) {
	spec, ok := signInProviders[id]
	if !ok {
		return false, nil
	}
	sec := signInProviderSection(id)
	current := store[sec]
	changed := false
	for _, s := range spec.Settings {
		if s.Autogenerate && current[s.Key] == "" {
			val, err := randomURL(48)
			if err != nil {
				return false, err
			}
			next, err := secrets.Set(store, sec, s.Key, val)
			if err != nil {
				return false, err
			}
			for k, v := range next {
				store[k] = v
			}
			current = store[sec]
			changed = true
		}
	}
	return changed, nil
}

func (a *API) setSignInProviderValues(id string, values map[string]*string) error {
	spec, ok := signInProviders[id]
	if !ok {
		return errors.New("no sign-in provider is chosen")
	}
	declared := make(map[string]bool, len(spec.Settings))
	for _, s := range spec.Settings {
		declared[s.Key] = true
	}
	for k := range values {
		if !declared[k] {
			return fmt.Errorf("%q is not a %s setting", k, spec.Label)
		}
	}
	dir := a.secretsDir()
	store, err := secrets.Read(dir)
	if err != nil {
		return err
	}
	sec := signInProviderSection(id)
	for k, v := range values {
		if v == nil {
			store, _ = secrets.Unset(store, sec, k)
		} else {
			store, err = secrets.Set(store, sec, k, *v)
			if err != nil {
				return err
			}
		}
	}
	if err := secrets.Write(store, dir); err != nil {
		return err
	}
	if a.Gate != nil {
		a.Gate.Reset()
	}
	return nil
}

// MigrateLegacySignInSecrets moves sign-in settings and the Scalekit workspace
// credential once from the plugin package sections to signin.<id> and signin.
func (a *API) MigrateLegacySignInSecrets() ([]string, error) {
	dir := a.secretsDir()
	store, err := secrets.Read(dir)
	if err != nil {
		return nil, err
	}
	var moved []string
	type foundEntry struct {
		id    string
		ready bool
	}
	var found []foundEntry
	changed := false

	for _, item := range legacySignInOrdered {
		old := store[item.legacy]
		if len(old) == 0 {
			continue
		}
		spec := signInProviders[item.id]
		declared := make(map[string]bool, len(spec.Settings))
		for _, s := range spec.Settings {
			declared[s.Key] = true
		}
		sec := signInProviderSection(item.id)
		current := store[sec]
		for k, v := range old {
			if declared[k] && current[k] == "" {
				store, err = secrets.Set(store, sec, k, v)
				if err != nil {
					return nil, err
				}
				current = store[sec]
			}
		}
		delete(store, item.legacy)
		moved = append(moved, item.legacy)
		ready := true
		for _, s := range spec.Settings {
			if s.Required && !s.Autogenerate && old[s.Key] == "" {
				ready = false
				break
			}
		}
		found = append(found, foundEntry{id: item.id, ready: ready})
		changed = true
	}

	implied := ""
	for _, f := range found {
		if f.ready {
			implied = f.id
			break
		}
	}
	if implied == "" && len(found) > 0 {
		implied = found[0].id
	}

	credMoved, err := migrateLegacyScalekitOperatorCredential(dir)
	if err != nil {
		return nil, err
	}
	if credMoved {
		hasScalekit := false
		for _, m := range moved {
			if m == "@open-neko/plugin-scalekit" {
				hasScalekit = true
				break
			}
		}
		if !hasScalekit {
			moved = append(moved, "@open-neko/plugin-scalekit")
		}
	}

	selected := store[signInSectionName]["PROVIDER"]
	if !isSignInProviderID(selected) {
		selected = ""
	}
	if implied != "" && selected == "" {
		if _, err := backfillSignInGenerated(store, implied); err != nil {
			return nil, err
		}
		store, err = secrets.Set(store, signInSectionName, "PROVIDER", implied)
		if err != nil {
			return nil, err
		}
		changed = true
	} else if selected != "" {
		backfilled, err := backfillSignInGenerated(store, selected)
		if err != nil {
			return nil, err
		}
		if backfilled {
			changed = true
		}
	}

	if changed {
		if err := secrets.Write(store, dir); err != nil {
			return nil, err
		}
	}
	if len(moved) > 0 {
		log.Printf("[signin] moved legacy sign-in settings from %s", strings.Join(moved, ", "))
	}
	if a.Gate != nil {
		a.Gate.Reset()
	}
	return moved, nil
}

func migrateLegacyScalekitOperatorCredential(overrideDir string) (bool, error) {
	file := secrets.Path(overrideDir)
	raw, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(raw, &full); err != nil {
		return false, nil
	}
	opsRaw, ok := full[secrets.OperatorsKey]
	if !ok {
		return false, nil
	}
	var ops map[string]map[string]json.RawMessage
	if err := json.Unmarshal(opsRaw, &ops); err != nil {
		return false, nil
	}
	dep := ops[deploymentConnectSlot]
	legacyCred, hasLegacy := dep["@open-neko/plugin-scalekit"]
	if !hasLegacy {
		return false, nil
	}
	if _, hasScalekit := dep[scalekitPlugin]; !hasScalekit {
		dep[scalekitPlugin] = legacyCred
	}
	delete(dep, "@open-neko/plugin-scalekit")
	if len(dep) == 0 {
		delete(ops, deploymentConnectSlot)
	} else {
		ops[deploymentConnectSlot] = dep
	}
	if len(ops) == 0 {
		delete(full, secrets.OperatorsKey)
	} else {
		encodedOps, err := json.Marshal(ops)
		if err != nil {
			return false, err
		}
		full[secrets.OperatorsKey] = encodedOps
	}
	out, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, byte(10))
	if err := os.WriteFile(file, out, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func (a *API) beginSignIn(ctx context.Context, redirectURI, state, loginHint string) (string, error) {
	store, err := secrets.Read(a.secretsDir())
	if err != nil {
		return "", err
	}
	id := store[signInSectionName]["PROVIDER"]
	if !isSignInProviderID(id) {
		return "", errors.New("no sign-in provider is chosen — set one up under Admin → Settings")
	}
	values := store[signInProviderSection(id)]
	if id == scalekitPlugin {
		return beginScalekitAuth(redirectURI, state, loginHint, values)
	}
	return beginMagicLinkAuth(ctx, a.signInClient(), a.now(), redirectURI, state, loginHint, values)
}

func (a *API) completeSignInAuth(ctx context.Context, code, redirectURI, state string) (identity, error) {
	store, err := secrets.Read(a.secretsDir())
	if err != nil {
		return identity{}, err
	}
	id := store[signInSectionName]["PROVIDER"]
	if !isSignInProviderID(id) {
		return identity{}, errors.New("no sign-in provider is chosen — set one up under Admin → Settings")
	}
	values := store[signInProviderSection(id)]
	if id == scalekitPlugin {
		return completeScalekitAuth(ctx, a.signInClient(), code, redirectURI, state, values)
	}
	return completeMagicLinkAuth(a.now(), code, state, values)
}

// ─── Scalekit OIDC sign-in ───────────────────────────────────────────────────

type scalekitResolvedEnv struct {
	EnvironmentURL string
	ClientID       string
	ClientSecret   string
}

func resolveScalekitEnv(env map[string]string) (scalekitResolvedEnv, error) {
	envURL := env["SCALEKIT_ENVIRONMENT_URL"]
	clientID := env["SCALEKIT_CLIENT_ID"]
	clientSecret := env["SCALEKIT_CLIENT_SECRET"]
	var missing []string
	if envURL == "" {
		missing = append(missing, "SCALEKIT_ENVIRONMENT_URL")
	}
	if clientID == "" {
		missing = append(missing, "SCALEKIT_CLIENT_ID")
	}
	if clientSecret == "" {
		missing = append(missing, "SCALEKIT_CLIENT_SECRET")
	}
	if len(missing) > 0 {
		return scalekitResolvedEnv{}, fmt.Errorf("%s not set — enter them on the Single sign-on settings page", strings.Join(missing, ", "))
	}
	normalized, err := normalizeScalekitEnvironmentURL(envURL)
	if err != nil {
		return scalekitResolvedEnv{}, err
	}
	return scalekitResolvedEnv{
		EnvironmentURL: normalized,
		ClientID:       clientID,
		ClientSecret:   clientSecret,
	}, nil
}

func normalizeScalekitEnvironmentURL(raw string) (string, error) {
	trimmed := strings.TrimRight(jsTrim(raw), "/")
	if trimmed == "" {
		return "", errors.New("SCALEKIT_ENVIRONMENT_URL is empty")
	}
	if len(trimmed) < 8 || !strings.EqualFold(trimmed[:8], "https://") {
		return "", fmt.Errorf("SCALEKIT_ENVIRONMENT_URL must be an https URL (got %s)", trimmed)
	}
	return trimmed, nil
}

func beginScalekitAuth(redirectURI, state, loginHint string, env map[string]string) (string, error) {
	if redirectURI == "" {
		return "", errors.New("params.redirectUri is required")
	}
	if state == "" {
		return "", errors.New("params.state is required")
	}
	resolved, err := resolveScalekitEnv(env)
	if err != nil {
		return "", err
	}
	return buildScalekitAuthorizationURL(resolved.EnvironmentURL, resolved.ClientID, redirectURI, state, loginHint, "")
}

func buildScalekitAuthorizationURL(environmentURL, clientID, redirectURI, state, loginHint, scope string) (string, error) {
	envURL, err := normalizeScalekitEnvironmentURL(environmentURL)
	if err != nil {
		return "", err
	}
	if clientID == "" {
		return "", errors.New("SCALEKIT_CLIENT_ID is empty")
	}
	if redirectURI == "" {
		return "", errors.New("redirectUri is required")
	}
	if state == "" {
		return "", errors.New("state is required")
	}
	base, err := url.Parse(envURL)
	if err != nil {
		return "", err
	}
	target := base.ResolveReference(&url.URL{Path: "/oauth/authorize"})
	if scope == "" {
		scope = scalekitOIDCScopes
	}
	pairs := [][2]string{
		{"response_type", "code"},
		{"client_id", clientID},
		{"redirect_uri", redirectURI},
		{"state", state},
		{"scope", scope},
	}
	if loginHint != "" {
		pairs = append(pairs, [2]string{"login_hint", loginHint})
	}
	var q strings.Builder
	for i, p := range pairs {
		if i > 0 {
			q.WriteByte('&')
		}
		q.WriteString(url.QueryEscape(p[0]))
		q.WriteByte('=')
		q.WriteString(url.QueryEscape(p[1]))
	}
	target.RawQuery = q.String()
	return target.String(), nil
}

func completeScalekitAuth(ctx context.Context, client *http.Client, code, redirectURI, state string, env map[string]string) (identity, error) {
	if code == "" {
		return identity{}, errors.New("params.code is required")
	}
	if redirectURI == "" {
		return identity{}, errors.New("params.redirectUri is required")
	}
	if state == "" {
		return identity{}, errors.New("params.state is required")
	}
	resolved, err := resolveScalekitEnv(env)
	if err != nil {
		return identity{}, err
	}
	accessToken, err := exchangeScalekitCode(ctx, client, resolved, code, redirectURI)
	if err != nil {
		return identity{}, err
	}
	rawInfo, err := fetchScalekitUserinfo(ctx, client, resolved.EnvironmentURL, accessToken)
	if err != nil {
		return identity{}, err
	}
	return scalekitUserinfoToIdentity(rawInfo)
}

func exchangeScalekitCode(ctx context.Context, client *http.Client, resolved scalekitResolvedEnv, code, redirectURI string) (string, error) {
	if code == "" {
		return "", errors.New("code is required")
	}
	if redirectURI == "" {
		return "", errors.New("redirectUri is required")
	}
	base, err := url.Parse(resolved.EnvironmentURL)
	if err != nil {
		return "", err
	}
	target := base.ResolveReference(&url.URL{Path: "/oauth/token"}).String()
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	basic := base64.StdEncoding.EncodeToString([]byte(resolved.ClientID + ":" + resolved.ClientSecret))
	headers := map[string]string{
		"Authorization": "Basic " + basic,
		"Content-Type":  "application/x-www-form-urlencoded",
		"Accept":        "application/json",
	}
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := scalekitRequest(ctx, client, scalekitHTTPTimeout, http.MethodPost, target, headers, form.Encode(), "token exchange", &tokens); err != nil {
		return "", err
	}
	if tokens.AccessToken == "" {
		return "", errors.New("Scalekit token exchange returned no access_token")
	}
	return tokens.AccessToken, nil
}

func fetchScalekitUserinfo(ctx context.Context, client *http.Client, environmentURL, accessToken string) (map[string]any, error) {
	if accessToken == "" {
		return nil, errors.New("access_token is required")
	}
	base, err := url.Parse(environmentURL)
	if err != nil {
		return nil, err
	}
	target := base.ResolveReference(&url.URL{Path: "/userinfo"}).String()
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Accept":        "application/json",
	}
	var info map[string]any
	if err := scalekitRequest(ctx, client, scalekitHTTPTimeout, http.MethodGet, target, headers, "", "userinfo", &info); err != nil {
		return nil, err
	}
	sub, _ := info["sub"].(string)
	if sub == "" {
		return nil, errors.New("Scalekit userinfo returned no sub")
	}
	return info, nil
}

func scalekitUserinfoToIdentity(info map[string]any) (identity, error) {
	sub, _ := info["sub"].(string)
	if sub == "" {
		return identity{}, errors.New("Scalekit userinfo returned no sub")
	}
	email, _ := info["email"].(string)
	if email == "" {
		return identity{}, errors.New("Scalekit userinfo returned no email — check the IdP's released scopes")
	}
	displayName, _ := info["name"].(string)
	if displayName == "" {
		given, _ := info["given_name"].(string)
		family, _ := info["family_name"].(string)
		var parts []string
		if given != "" {
			parts = append(parts, given)
		}
		if family != "" {
			parts = append(parts, family)
		}
		displayName = strings.TrimSpace(strings.Join(parts, " "))
	}
	var namePtr *string
	if displayName != "" {
		namePtr = &displayName
	}
	orgID, _ := info["organization_id"].(string)
	seen := map[string]bool{}
	var groupList []any
	for _, field := range []string{"groups", "roles"} {
		items, _ := info[field].([]any)
		for _, item := range items {
			switch v := item.(type) {
			case string:
				if v != "" && !seen[v] {
					seen[v] = true
					groupList = append(groupList, v)
				}
			case map[string]any:
				groupList = append(groupList, v)
			}
		}
	}
	rawIdentity, err := json.Marshal(map[string]any{
		"sub":    sub,
		"email":  email,
		"name":   namePtr,
		"orgId":  orgID,
		"groups": groupList,
	})
	if err != nil {
		return identity{}, err
	}
	return parseIdentity(rawIdentity)
}

func scalekitRequest(ctx context.Context, client *http.Client, timeout time.Duration, method, target string, headers map[string]string, body string, description string, out any) error {
	if client == nil {
		client = http.DefaultClient
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, target, bodyReader)
	if err != nil {
		return fmt.Errorf("Scalekit %s network error: %v", description, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("Scalekit %s timed out after %dms", description, timeout.Milliseconds())
		}
		return fmt.Errorf("Scalekit %s network error: %v", description, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, scalekitMaxBody))
	text := string(raw)
	if text != "" {
		var check any
		if err := json.Unmarshal(raw, &check); err != nil {
			snippet := text
			if len(snippet) > 200 {
				snippet = snippet[:200]
			}
			return fmt.Errorf("Scalekit %s returned non-JSON (HTTP %d): %s", description, res.StatusCode, snippet)
		}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var errBody struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &errBody)
		code := errBody.Error
		if code == "" {
			code = "no error code"
		}
		detail := code
		if errBody.ErrorDescription != "" {
			detail += ": " + errBody.ErrorDescription
		}
		return fmt.Errorf("Scalekit %s returned HTTP %d (%s)", description, res.StatusCode, detail)
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	return json.Unmarshal(raw, out)
}

// ─── Magic-link sign-in (tokens + email delivery) ────────────────────────────

type magicLinkClaims struct {
	Email     string `json:"email"`
	State     string `json:"state"`
	ExpiresAt int64  `json:"expiresAt"`
}

func clampMagicLinkTTL(ttlSeconds float64, hasValue bool) int {
	if !hasValue || math.IsNaN(ttlSeconds) || math.IsInf(ttlSeconds, 0) {
		return magicLinkDefaultTTL
	}
	floored := int(math.Floor(ttlSeconds))
	if floored < magicLinkMinTTL {
		return magicLinkMinTTL
	}
	if floored > magicLinkMaxTTL {
		return magicLinkMaxTTL
	}
	return floored
}

func parseMagicLinkTTL(raw string) int {
	if raw == "" {
		return magicLinkDefaultTTL
	}
	n, err := strconv.ParseFloat(jsTrim(raw), 64)
	if err != nil {
		return magicLinkDefaultTTL
	}
	return clampMagicLinkTTL(n, true)
}

func mintMagicLinkToken(email, state, secret string, ttlSeconds int, now time.Time) (string, error) {
	if len(secret) < magicLinkMinSecretLen {
		return "", fmt.Errorf("MAGIC_LINK_SIGNING_SECRET must be at least %d characters", magicLinkMinSecretLen)
	}
	ttl := clampMagicLinkTTL(float64(ttlSeconds), true)
	claims := magicLinkClaims{
		Email:     email,
		State:     state,
		ExpiresAt: now.Unix() + int64(ttl),
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + macOf(secret, payload), nil
}

func verifyMagicLinkToken(token, state, secret string, now time.Time) (magicLinkClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return magicLinkClaims{}, errors.New("this sign-in link is not valid")
	}
	payload, signature := parts[0], parts[1]
	expected := macOf(secret, payload)
	provided, err1 := decodeBase64URLLoose(signature)
	wanted, err2 := base64.RawURLEncoding.DecodeString(expected)
	if err1 != nil || err2 != nil || len(provided) != len(wanted) || subtle.ConstantTimeCompare(provided, wanted) != 1 {
		return magicLinkClaims{}, errors.New("this sign-in link is not valid")
	}
	decodedPayload, err := decodeBase64URLLoose(payload)
	if err != nil {
		return magicLinkClaims{}, errors.New("this sign-in link is not valid")
	}
	var raw map[string]any
	if err := json.Unmarshal(decodedPayload, &raw); err != nil {
		return magicLinkClaims{}, errors.New("this sign-in link is not valid")
	}
	email, okEmail := raw["email"].(string)
	claimState, okState := raw["state"].(string)
	expFloat, okExp := raw["expiresAt"].(float64)
	if !okEmail || email == "" || !okState || claimState == "" || !okExp {
		return magicLinkClaims{}, errors.New("this sign-in link is not valid")
	}
	if claimState != state {
		return magicLinkClaims{}, errors.New("this sign-in link must be opened in the browser that requested it")
	}
	expiresAt := int64(expFloat)
	if expiresAt <= now.Unix() {
		return magicLinkClaims{}, errors.New("this sign-in link has expired — request a new one")
	}
	return magicLinkClaims{
		Email:     email,
		State:     claimState,
		ExpiresAt: expiresAt,
	}, nil
}

func decodeBase64URLLoose(s string) ([]byte, error) {
	trimmed := strings.TrimRight(s, "=")
	return base64.RawURLEncoding.DecodeString(trimmed)
}

func resolveMagicLinkEnv(env map[string]string) (secret, from string, ttlSeconds int, err error) {
	secret = env["MAGIC_LINK_SIGNING_SECRET"]
	if len(secret) < magicLinkMinSecretLen {
		return "", "", 0, fmt.Errorf("MAGIC_LINK_SIGNING_SECRET must be set to at least %d characters", magicLinkMinSecretLen)
	}
	from = jsTrim(env["MAGIC_LINK_FROM"])
	if from == "" {
		return "", "", 0, errors.New("MAGIC_LINK_FROM must be set")
	}
	return secret, from, parseMagicLinkTTL(env["MAGIC_LINK_TTL_SECONDS"]), nil
}

func beginMagicLinkAuth(ctx context.Context, client *http.Client, now time.Time, redirectURI, state, loginHint string, env map[string]string) (string, error) {
	if redirectURI == "" {
		return "", errors.New("params.redirectUri is required")
	}
	if state == "" {
		return "", errors.New("params.state is required")
	}
	email := strings.ToLower(jsTrim(loginHint))
	if !deliveryTestEmail.MatchString(email) {
		return "", errors.New("magic link sign-in requires the email address as loginHint")
	}
	callback, err := url.Parse(redirectURI)
	if err != nil || callback.Scheme == "" || callback.Host == "" {
		return "", errors.New("params.redirectUri must be an absolute URL")
	}
	secret, from, ttlSeconds, err := resolveMagicLinkEnv(env)
	if err != nil {
		return "", err
	}
	token, err := mintMagicLinkToken(email, state, secret, ttlSeconds, now)
	if err != nil {
		return "", err
	}
	link := *callback
	q := link.Query()
	q.Set("code", token)
	q.Set("state", state)
	link.RawQuery = q.Encode()

	send, err := resolveEmailSender(client, env)
	if err != nil {
		return "", err
	}
	ttlMinutes := int(math.Round(float64(ttlSeconds) / 60.0))
	if ttlMinutes < 1 {
		ttlMinutes = 1
	}
	msg := buildSignInEmail(from, email, link.String(), ttlMinutes)
	if err := send(ctx, msg); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s://%s/signin?notice=link-sent", strings.ToLower(callback.Scheme), callback.Host), nil
}

func completeMagicLinkAuth(now time.Time, code, state string, env map[string]string) (identity, error) {
	if code == "" {
		return identity{}, errors.New("params.code is required")
	}
	if state == "" {
		return identity{}, errors.New("params.state is required")
	}
	secret, _, _, err := resolveMagicLinkEnv(env)
	if err != nil {
		return identity{}, err
	}
	claims, err := verifyMagicLinkToken(code, state, secret, now)
	if err != nil {
		return identity{}, err
	}
	return identity{
		Sub:   "magic-link:" + claims.Email,
		Email: claims.Email,
	}, nil
}

type signInEmailMessage struct {
	From    string
	To      string
	Subject string
	Text    string
	HTML    string
}

type emailSenderFunc func(ctx context.Context, msg signInEmailMessage) error

func resolveEmailSender(client *http.Client, env map[string]string) (emailSenderFunc, error) {
	_, send, err := resolveEmailProvider(client, env["RESEND_API_KEY"], env["POSTMARK_SERVER_TOKEN"], env["SENDGRID_API_KEY"])
	return send, err
}

func resolveEmailProvider(client *http.Client, resendKey, postmarkToken, sendgridKey string) (string, emailSenderFunc, error) {
	var configured []string
	if resendKey != "" {
		configured = append(configured, "resend")
	}
	if postmarkToken != "" {
		configured = append(configured, "postmark")
	}
	if sendgridKey != "" {
		configured = append(configured, "sendgrid")
	}
	if len(configured) == 0 {
		return "", nil, errors.New("no email provider configured. Set one of RESEND_API_KEY, POSTMARK_SERVER_TOKEN, or SENDGRID_API_KEY")
	}
	if len(configured) > 1 {
		return "", nil, fmt.Errorf("multiple email providers configured (%s). Set exactly one API key", strings.Join(configured, ", "))
	}
	switch configured[0] {
	case "resend":
		return "resend", func(ctx context.Context, msg signInEmailMessage) error {
			body, err := json.Marshal(struct {
				From    string   `json:"from"`
				To      []string `json:"to"`
				Subject string   `json:"subject"`
				Text    string   `json:"text"`
				HTML    string   `json:"html"`
			}{
				From:    msg.From,
				To:      []string{msg.To},
				Subject: msg.Subject,
				Text:    msg.Text,
				HTML:    msg.HTML,
			})
			if err != nil {
				return err
			}
			return postSignInEmail(ctx, client, "https://api.resend.com/emails", map[string]string{
				"Authorization": "Bearer " + resendKey,
				"Content-Type":  "application/json",
			}, body, "Resend")
		}, nil
	case "postmark":
		return "postmark", func(ctx context.Context, msg signInEmailMessage) error {
			body, err := json.Marshal(struct {
				From          string `json:"From"`
				To            string `json:"To"`
				Subject       string `json:"Subject"`
				TextBody      string `json:"TextBody"`
				HtmlBody      string `json:"HtmlBody"`
				MessageStream string `json:"MessageStream"`
			}{
				From:          msg.From,
				To:            msg.To,
				Subject:       msg.Subject,
				TextBody:      msg.Text,
				HtmlBody:      msg.HTML,
				MessageStream: "outbound",
			})
			if err != nil {
				return err
			}
			return postSignInEmail(ctx, client, "https://api.postmarkapp.com/email", map[string]string{
				"X-Postmark-Server-Token": postmarkToken,
				"Content-Type":            "application/json",
				"Accept":                  "application/json",
			}, body, "Postmark")
		}, nil
	default:
		return "sendgrid", func(ctx context.Context, msg signInEmailMessage) error {
			fromAddr := parseSignInFrom(msg.From)
			type sgEmail struct {
				Email string `json:"email"`
				Name  string `json:"name,omitempty"`
			}
			type sgContent struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			}
			type sgPersonalization struct {
				To []sgEmail `json:"to"`
			}
			body, err := json.Marshal(struct {
				Personalizations []sgPersonalization `json:"personalizations"`
				From             sgEmail             `json:"from"`
				Subject          string              `json:"subject"`
				Content          []sgContent         `json:"content"`
			}{
				Personalizations: []sgPersonalization{{To: []sgEmail{{Email: msg.To}}}},
				From:             sgEmail{Email: fromAddr.Email, Name: fromAddr.Name},
				Subject:          msg.Subject,
				Content: []sgContent{
					{Type: "text/plain", Value: msg.Text},
					{Type: "text/html", Value: msg.HTML},
				},
			})
			if err != nil {
				return err
			}
			return postSignInEmail(ctx, client, "https://api.sendgrid.com/v3/mail/send", map[string]string{
				"Authorization": "Bearer " + sendgridKey,
				"Content-Type":  "application/json",
			}, body, "SendGrid")
		}, nil
	}
}

func postSignInEmail(ctx context.Context, client *http.Client, target string, headers map[string]string, body []byte, provider string) error {
	if client == nil {
		client = http.DefaultClient
	}
	reqCtx, cancel := context.WithTimeout(ctx, beginTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode <= 299 {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	snippet := string(raw)
	if len(snippet) > 300 {
		snippet = snippet[:300]
	}
	return fmt.Errorf("%s rejected the sign-in email (%d): %s", provider, res.StatusCode, snippet)
}

var fromHeaderRX = regexp.MustCompile(`^[` + jsSpaceClass + `]*(.*?)[` + jsSpaceClass + `]*<([^<>` + jsSpaceClass + `]+@[^<>` + jsSpaceClass + `]+)>[` + jsSpaceClass + `]*$`)

type signInFromAddress struct {
	Email string
	Name  string
}

func parseSignInFrom(from string) signInFromAddress {
	m := fromHeaderRX.FindStringSubmatch(from)
	if len(m) == 3 && m[2] != "" {
		name := m[1]
		name = strings.TrimPrefix(name, `"`)
		name = strings.TrimSuffix(name, `"`)
		name = jsTrim(name)
		return signInFromAddress{Email: m[2], Name: name}
	}
	return signInFromAddress{Email: jsTrim(from)}
}

func buildSignInEmail(from, to, link string, ttlMinutes int) signInEmailMessage {
	text := strings.Join([]string{
		"Sign in to OpenNeko",
		"",
		"Open this link to sign in as " + to + ":",
		"",
		link,
		"",
		fmt.Sprintf("The link works once and expires in %d minutes. Open it in", ttlMinutes),
		"the same browser you requested it from.",
		"",
		"Someone asked for a sign-in link for this address. If that was not",
		"you, ignore this email. Nobody can sign in without the link.",
		"",
		"OpenNeko · AI that helps run your business.",
	}, string(byte(10)))
	return signInEmailMessage{
		From:    from,
		To:      to,
		Subject: "Your OpenNeko sign-in link",
		Text:    text,
		HTML:    signInEmailHTML(to, link, ttlMinutes),
	}
}

func signInEmailHTML(to, link string, ttlMinutes int) string {
	href := escapeSignInHTML(link)
	address := escapeSignInHTML(to)
	minutes := strconv.Itoa(ttlMinutes)
	const sans = "-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif"
	const mono = "ui-monospace,SFMono-Regular,Menlo,Consolas,'Liberation Mono',monospace"
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light">
<meta name="supported-color-schemes" content="light">
<title>Your OpenNeko sign-in link</title>
</head>
<body style="margin:0;padding:0;background:#FAFAF7;">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;">The link works once and expires in ` + minutes + ` minutes.</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#FAFAF7;">
<tr><td align="center" style="padding:32px 16px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:480px;background:#FFFFFF;border:1px solid #EEEBE4;border-radius:14px;">
<tr><td style="padding:32px 32px 8px 32px;font-family:` + sans + `;">
<div style="font-size:15px;font-weight:700;letter-spacing:-0.01em;color:#2D2A24;">OpenNeko</div>
</td></tr>
<tr><td style="padding:16px 32px 0 32px;font-family:` + sans + `;">
<h1 style="margin:0;font-size:22px;line-height:1.3;font-weight:700;letter-spacing:-0.02em;color:#2D2A24;">Sign in to OpenNeko</h1>
<p style="margin:12px 0 0 0;font-size:15px;line-height:1.55;color:#6F6A60;">Select the button to sign in as <span style="color:#2D2A24;font-weight:600;">` + address + `</span>.</p>
</td></tr>
<tr><td style="padding:24px 32px 0 32px;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>
<td align="center" bgcolor="#6B5CE7" style="border-radius:10px;">
<a href="` + href + `" style="display:inline-block;padding:14px 28px;font-family:` + sans + `;font-size:15px;font-weight:600;color:#FFFFFF;text-decoration:none;border-radius:10px;">Sign in</a>
</td></tr></table>
<p style="margin:16px 0 0 0;font-family:` + sans + `;font-size:13px;line-height:1.55;color:#6F6A60;">The link works once and expires in ` + minutes + ` minutes. Open it in the same browser you requested it from.</p>
</td></tr>
<tr><td style="padding:24px 32px 0 32px;font-family:` + sans + `;">
<p style="margin:0 0 8px 0;font-size:13px;color:#6F6A60;">Or paste this link into your browser:</p>
<div style="padding:12px 14px;background:#F7F4ED;border:1px solid #EEEBE4;border-radius:10px;font-family:` + mono + `;font-size:12px;line-height:1.6;color:#2D2A24;word-break:break-all;">` + href + `</div>
</td></tr>
<tr><td style="padding:24px 32px 32px 32px;font-family:` + sans + `;">
<div style="height:1px;background:#EEEBE4;font-size:0;line-height:0;">&nbsp;</div>
<p style="margin:20px 0 0 0;font-size:12px;line-height:1.6;color:#756F65;">Someone asked for a sign-in link for this address. If that was not you, ignore this email. Nobody can sign in without the link.</p>
<p style="margin:12px 0 0 0;font-size:12px;line-height:1.6;color:#756F65;">OpenNeko · AI that helps run your business.</p>
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`
}

func escapeSignInHTML(value string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(value)
}
