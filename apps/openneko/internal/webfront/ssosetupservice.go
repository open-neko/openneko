package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const (
	scalekitDeploymentOperatorID = "deployment"
	defaultScalekitMCPURL        = "https://mcp.scalekit.com"
	scalekitMCPProtocolVersion   = "2025-03-26"
	scalekitExpiryLead           = time.Minute
)

type SSOSetupConnection struct {
	Status   string  `json:"status"`
	Provider *string `json:"provider"`
	Enabled  bool    `json:"enabled"`
}

type SSOSetupStatusResult struct {
	Status              string              `json:"status"`
	PortalLink          *string             `json:"portalLink"`
	PortalLinkExpiresAt *string             `json:"portalLinkExpiresAt"`
	Provider            *string             `json:"provider"`
	Connection          *SSOSetupConnection `json:"connection"`
	LastError           *string             `json:"lastError"`
	SetupCompletedAt    *string             `json:"setupCompletedAt"`
	EnvironmentID       *string             `json:"environmentId"`
	OrganizationID      *string             `json:"organizationId"`
	EnvironmentTier     *string             `json:"environmentTier"`
	SignInConfigured    bool                `json:"signInConfigured"`
}

type SSOEnvironmentRow struct {
	ID     string  `json:"id"`
	Name   *string `json:"name"`
	Tier   *string `json:"tier"`
	Domain *string `json:"domain"`
}

type SSOOrganizationRow struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
}

type SSOEnvironmentCredentials struct {
	EnvironmentURL string `json:"environmentUrl"`
	ClientID       string `json:"clientId"`
}

type SSOPortalLinkResult struct {
	PortalLink string  `json:"portalLink"`
	ExpiresAt  *string `json:"expiresAt"`
}

type ssoSetupRow struct {
	status           string
	portalLink       *string
	portalLinkExp    *time.Time
	provider         *string
	lastError        *string
	setupCompletedAt *time.Time
	environmentID    *string
	organizationID   *string
	environmentTier  *string
}

type scalekitOperatorCredential struct {
	Tokens        map[string]any `json:"tokens"`
	Scopes        []string       `json:"scopes,omitempty"`
	ProviderLabel string         `json:"providerLabel,omitempty"`
	ConnectedAt   string         `json:"connectedAt"`
	RefreshedAt   string         `json:"refreshedAt,omitempty"`
}

var (
	rxEnvURL       = regexp.MustCompile(`SCALEKIT_ENVIRONMENT_URL=(https://\S+)`)
	rxClientID     = regexp.MustCompile(`SCALEKIT_CLIENT_ID=(\S+)`)
	rxEnvRowID     = regexp.MustCompile(`ID:\s*(\S+)`)
	rxEnvRowName   = regexp.MustCompile(`Name:\s*([^|]+?)\s*\|`)
	rxEnvRowTier   = regexp.MustCompile(`Type:\s*(\w+)`)
	rxEnvRowDomain = regexp.MustCompile(`Domain:\s*([^|]+?)\s*\|`)
	rxOrgSplit     = regexp.MustCompile(`Organization \d+:`)
	rxOrgRowName   = regexp.MustCompile(`Name:\s*([^
]+)`)
	rxPortalLink   = regexp.MustCompile(`Link:\s*(https?://[^\s]+)`)
	rxConnStatus   = regexp.MustCompile(`status:\s*(\w+)`)
	rxConnProvider = regexp.MustCompile(`provider:\s*(\w+)`)
)

func formatJSISO(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05.000Z")
	return &s
}

func (a *API) ssoSignInConfigured() bool {
	store := secrets.ReadSoft(a.secretsDir(), nil)
	values := store[signInProviderSection(scalekitPlugin)]
	return values["SCALEKIT_ENVIRONMENT_URL"] != "" &&
		values["SCALEKIT_CLIENT_ID"] != "" &&
		values["SCALEKIT_CLIENT_SECRET"] != ""
}

func (a *API) readSSOSetupRow(ctx context.Context, orgID string) (*ssoSetupRow, error) {
	if a.DB == nil {
		return nil, nil
	}
	var r ssoSetupRow
	err := a.DB.QueryRow(ctx, `
		select status, portal_link, portal_link_expires_at, provider, last_error,
		       setup_completed_at, scalekit_environment_id, scalekit_organization_id,
		       scalekit_environment_tier
		from sso_setup
		where org_id = $1
		limit 1`, orgID).Scan(
		&r.status,
		&r.portalLink,
		&r.portalLinkExp,
		&r.provider,
		&r.lastError,
		&r.setupCompletedAt,
		&r.environmentID,
		&r.organizationID,
		&r.environmentTier,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (a *API) ssoGetStatus(ctx context.Context, orgID string) (SSOSetupStatusResult, error) {
	row, err := a.readSSOSetupRow(ctx, orgID)
	if err != nil {
		return SSOSetupStatusResult{}, err
	}
	signInConfigured := a.ssoSignInConfigured()
	if row == nil {
		return SSOSetupStatusResult{
			Status:           "not_started",
			SignInConfigured: signInConfigured,
		}, nil
	}
	return SSOSetupStatusResult{
		Status:              row.status,
		PortalLink:          row.portalLink,
		PortalLinkExpiresAt: formatJSISO(row.portalLinkExp),
		Provider:            row.provider,
		Connection:          nil,
		LastError:           row.lastError,
		SetupCompletedAt:    formatJSISO(row.setupCompletedAt),
		EnvironmentID:       row.environmentID,
		OrganizationID:      row.organizationID,
		EnvironmentTier:     row.environmentTier,
		SignInConfigured:    signInConfigured,
	}, nil
}

func (a *API) ssoGetEnvironmentCredentials(ctx context.Context, orgID, environmentID string) (SSOEnvironmentCredentials, error) {
	_ = orgID
	text, err := a.callScalekitWorkspaceTool(ctx, "get_environment_credentials", map[string]any{
		"environmentId": environmentID,
	})
	if err != nil {
		return SSOEnvironmentCredentials{}, err
	}
	var envURL, clientID string
	if m := rxEnvURL.FindStringSubmatch(text); len(m) > 1 {
		envURL = m[1]
	}
	if m := rxClientID.FindStringSubmatch(text); len(m) > 1 {
		clientID = m[1]
	}
	if envURL == "" || clientID == "" {
		return SSOEnvironmentCredentials{}, fmt.Errorf("could not parse credentials from Scalekit's response: %s", jsSlice(text, 300))
	}
	return SSOEnvironmentCredentials{
		EnvironmentURL: envURL,
		ClientID:       clientID,
	}, nil
}

func (a *API) ssoListEnvironments(ctx context.Context, orgID string) ([]SSOEnvironmentRow, error) {
	_ = orgID
	text, err := a.callScalekitWorkspaceTool(ctx, "list_environments", map[string]any{})
	if err != nil {
		return nil, err
	}
	out := []SSOEnvironmentRow{}
	for _, line := range strings.Split(text, string(byte(10))) {
		var id string
		if m := rxEnvRowID.FindStringSubmatch(line); len(m) > 1 {
			id = m[1]
		}
		if id == "" {
			continue
		}
		var name, tier, domain *string
		if m := rxEnvRowName.FindStringSubmatch(line); len(m) > 1 {
			if s := strings.TrimSpace(m[1]); s != "" {
				name = &s
			}
		}
		if m := rxEnvRowTier.FindStringSubmatch(line); len(m) > 1 && m[1] != "" {
			s := m[1]
			tier = &s
		}
		if m := rxEnvRowDomain.FindStringSubmatch(line); len(m) > 1 {
			if s := strings.TrimSpace(m[1]); s != "" {
				domain = &s
			}
		}
		out = append(out, SSOEnvironmentRow{
			ID:     id,
			Name:   name,
			Tier:   tier,
			Domain: domain,
		})
	}
	return out, nil
}

func (a *API) ssoListOrganizations(ctx context.Context, orgID, environmentID string) ([]SSOOrganizationRow, error) {
	_ = orgID
	text, err := a.callScalekitWorkspaceTool(ctx, "list_organizations", map[string]any{
		"environmentId": environmentID,
	})
	if err != nil {
		return nil, err
	}
	out := []SSOOrganizationRow{}
	for _, block := range rxOrgSplit.Split(text, -1) {
		var id string
		if m := rxEnvRowID.FindStringSubmatch(block); len(m) > 1 {
			id = m[1]
		}
		if id == "" {
			continue
		}
		var name *string
		if m := rxOrgRowName.FindStringSubmatch(block); len(m) > 1 {
			if s := strings.TrimSpace(m[1]); s != "" {
				name = &s
			}
		}
		out = append(out, SSOOrganizationRow{
			ID:   id,
			Name: name,
		})
	}
	return out, nil
}

func (a *API) ssoSetScalekitIDs(ctx context.Context, orgID, environmentID, organizationID, tier string) error {
	if environmentID == "" || organizationID == "" {
		return errors.New("environmentId and organizationId are required")
	}
	if tier == "" {
		tier = "dev"
	}
	if a.DB != nil {
		if _, err := a.DB.Exec(ctx, `
			insert into sso_setup (
				org_id, scalekit_environment_id, scalekit_organization_id, scalekit_environment_tier, updated_at
			) values ($1, $2, $3, $4, now())
			on conflict (org_id) do update set
				scalekit_environment_id = excluded.scalekit_environment_id,
				scalekit_organization_id = excluded.scalekit_organization_id,
				scalekit_environment_tier = excluded.scalekit_environment_tier,
				updated_at = excluded.updated_at`,
			orgID, environmentID, organizationID, tier); err != nil {
			return err
		}
		if _, err := a.DB.Exec(ctx, `
			update organization
			set scalekit_org_id = $2, updated_at = now()
			where id = $1`, orgID, organizationID); err != nil {
			return err
		}
	}
	return a.setSignInProviderValues(scalekitPlugin, map[string]*string{
		"SCALEKIT_ORGANIZATION_ID": &organizationID,
		"SCALEKIT_ENVIRONMENT_ID":  &environmentID,
	})
}

func (a *API) ssoSetSignInCredentials(ctx context.Context, orgID, environmentURL, clientID, clientSecret string) error {
	_ = orgID
	if environmentURL == "" || clientID == "" || clientSecret == "" {
		return errors.New("environmentUrl, clientId and clientSecret are required")
	}
	if err := a.setSignInProviderValues(scalekitPlugin, map[string]*string{
		"SCALEKIT_ENVIRONMENT_URL": &environmentURL,
		"SCALEKIT_CLIENT_ID":       &clientID,
		"SCALEKIT_CLIENT_SECRET":   &clientSecret,
	}); err != nil {
		return err
	}
	return a.selectSignInProvider(scalekitPlugin)
}

func (a *API) resolveSSOSetupIDs(ctx context.Context, orgID string) (string, string, error) {
	row, err := a.readSSOSetupRow(ctx, orgID)
	if err != nil {
		return "", "", err
	}
	if row != nil && row.environmentID != nil && *row.environmentID != "" && row.organizationID != nil && *row.organizationID != "" {
		return *row.environmentID, *row.organizationID, nil
	}
	store := secrets.ReadSoft(a.secretsDir(), nil)
	env := store[signInProviderSection(scalekitPlugin)]
	if env["SCALEKIT_ENVIRONMENT_ID"] != "" && env["SCALEKIT_ORGANIZATION_ID"] != "" {
		return env["SCALEKIT_ENVIRONMENT_ID"], env["SCALEKIT_ORGANIZATION_ID"], nil
	}
	return "", "", errors.New("Scalekit environment/organization not selected yet — run `get_environment_credentials` + `list_organizations` via the agent, or set them on the SSO settings page")
}

func (a *API) ssoGeneratePortalLink(ctx context.Context, orgID string) (SSOPortalLinkResult, error) {
	environmentID, organizationID, err := a.resolveSSOSetupIDs(ctx, orgID)
	if err != nil {
		return SSOPortalLinkResult{}, err
	}
	text, err := a.callScalekitWorkspaceTool(ctx, "generate_admin_portal_link", map[string]any{
		"environmentId":  environmentID,
		"organizationId": organizationID,
	})
	if err != nil {
		return SSOPortalLinkResult{}, err
	}
	m := rxPortalLink.FindStringSubmatch(text)
	if len(m) < 2 || m[1] == "" {
		return SSOPortalLinkResult{}, fmt.Errorf("Scalekit returned no portal link (%s). Verify the organization id.", jsSlice(text, 200))
	}
	portalLink := m[1]
	if a.DB != nil {
		if _, err := a.DB.Exec(ctx, `
			insert into sso_setup (
				org_id, status, portal_link, portal_link_expires_at, last_error, updated_at
			) values ($1, 'awaiting_portal', $2, null, null, now())
			on conflict (org_id) do update set
				status = 'awaiting_portal',
				portal_link = excluded.portal_link,
				portal_link_expires_at = null,
				last_error = null,
				updated_at = excluded.updated_at`,
			orgID, portalLink); err != nil {
			return SSOPortalLinkResult{}, err
		}
	}
	return SSOPortalLinkResult{
		PortalLink: portalLink,
		ExpiresAt:  nil,
	}, nil
}

func (a *API) ssoCheckConnection(ctx context.Context, orgID string) (SSOSetupStatusResult, error) {
	var (
		connection *SSOSetupConnection
		nextStatus string
		lastError  *string
	)
	environmentID, organizationID, err := a.resolveSSOSetupIDs(ctx, orgID)
	if err == nil {
		var text string
		text, err = a.callScalekitWorkspaceTool(ctx, "list_organization_connections", map[string]any{
			"environmentId":  environmentID,
			"organizationId": organizationID,
		})
		if err == nil {
			statusMatches := rxConnStatus.FindAllStringSubmatch(text, -1)
			var providerPtr *string
			if pm := rxConnProvider.FindStringSubmatch(text); len(pm) > 1 && pm[1] != "" {
				p := pm[1]
				providerPtr = &p
			}
			completed := false
			for _, sm := range statusMatches {
				if len(sm) > 1 && sm[1] == "COMPLETED" {
					completed = true
					break
				}
			}
			switch {
			case completed:
				connection = &SSOSetupConnection{
					Status:   "connected",
					Provider: providerPtr,
					Enabled:  true,
				}
				nextStatus = "connected"
			case len(statusMatches) > 0:
				connection = &SSOSetupConnection{
					Status:   "in_progress",
					Provider: providerPtr,
					Enabled:  false,
				}
				nextStatus = "polling"
			default:
				nextStatus = "awaiting_portal"
			}
		}
	}
	if err != nil {
		nextStatus = "failed"
		msg := err.Error()
		lastError = &msg
	}

	var provider *string
	if connection != nil {
		provider = connection.Provider
	}
	var completedAt *time.Time
	if nextStatus == "connected" {
		now := time.Now().UTC()
		completedAt = &now
	}
	if a.DB != nil {
		if _, dbErr := a.DB.Exec(ctx, `
			insert into sso_setup (
				org_id, status, provider, last_polled_at, last_error, setup_completed_at, updated_at
			) values ($1, $2, $3, now(), $4, $5, now())
			on conflict (org_id) do update set
				status = excluded.status,
				provider = excluded.provider,
				last_polled_at = excluded.last_polled_at,
				last_error = excluded.last_error,
				setup_completed_at = excluded.setup_completed_at,
				updated_at = excluded.updated_at`,
			orgID, nextStatus, provider, lastError, completedAt); dbErr != nil {
			return SSOSetupStatusResult{}, dbErr
		}
	}
	return SSOSetupStatusResult{
		Status:           nextStatus,
		Provider:         provider,
		Connection:       connection,
		LastError:        lastError,
		SetupCompletedAt: formatJSISO(completedAt),
		SignInConfigured: a.ssoSignInConfigured(),
	}, nil
}

func (a *API) ssoPoll(ctx context.Context, orgID string) (SSOSetupStatusResult, error) {
	current, err := a.ssoGetStatus(ctx, orgID)
	if err != nil {
		return SSOSetupStatusResult{}, err
	}
	if current.Status != "awaiting_portal" && current.Status != "polling" {
		return current, nil
	}
	return a.ssoCheckConnection(ctx, orgID)
}

// SSOSetupPoll checks pending SSO portal setups for all organisations in
// awaiting_portal or polling state.
func (a *API) SSOSetupPoll(ctx context.Context) error {
	if a.DB == nil {
		return nil
	}
	rows, err := a.DB.Query(ctx, `
		select org_id from sso_setup
		where status in ('awaiting_portal', 'polling')
		order by org_id`)
	if err != nil {
		return err
	}
	var orgIDs []string
	for rows.Next() {
		var orgID string
		if err := rows.Scan(&orgID); err != nil {
			rows.Close()
			return err
		}
		orgIDs = append(orgIDs, orgID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, orgID := range orgIDs {
		if _, err := a.ssoPoll(ctx, orgID); err != nil {
			return err
		}
	}
	return nil
}

func readScalekitOperatorCredential(overrideDir string) (*scalekitOperatorCredential, error) {
	file := secrets.Path(overrideDir)
	raw, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var parsed struct {
		Operators map[string]map[string]json.RawMessage `json:"_operators"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, nil
	}
	credRaw, ok := parsed.Operators[scalekitDeploymentOperatorID][scalekitPlugin]
	if !ok || len(credRaw) == 0 {
		return nil, nil
	}
	var sealed string
	if json.Unmarshal(credRaw, &sealed) == nil {
		plain, err := config.MaybeDecryptValue(overrideDir, sealed)
		if err != nil {
			return nil, err
		}
		credRaw = json.RawMessage(plain)
	}
	var cred scalekitOperatorCredential
	if err := json.Unmarshal(credRaw, &cred); err != nil || cred.Tokens == nil || cred.ConnectedAt == "" {
		return nil, nil
	}
	return &cred, nil
}

func writeScalekitOperatorCredential(overrideDir string, cred *scalekitOperatorCredential) error {
	file := secrets.Path(overrideDir)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	full := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(file); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &full)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	operators := map[string]map[string]json.RawMessage{}
	if opRaw, ok := full[secrets.OperatorsKey]; ok && len(opRaw) > 0 {
		_ = json.Unmarshal(opRaw, &operators)
	}
	if operators[scalekitDeploymentOperatorID] == nil {
		operators[scalekitDeploymentOperatorID] = map[string]json.RawMessage{}
	}
	credBytes, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	enc, err := config.EncryptValue(overrideDir, string(credBytes))
	if err != nil {
		return err
	}
	encJSON, err := json.Marshal(enc)
	if err != nil {
		return err
	}
	operators[scalekitDeploymentOperatorID][scalekitPlugin] = encJSON
	opBytes, err := json.Marshal(operators)
	if err != nil {
		return err
	}
	full[secrets.OperatorsKey] = opBytes
	tmpRaw, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", file, os.Getpid())
	if err := os.WriteFile(tmp, append(tmpRaw, byte(10)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func scalekitCredentialNearExpiry(cred *scalekitOperatorCredential, now time.Time) bool {
	if cred == nil || cred.Tokens == nil {
		return false
	}
	var expMs int64
	switch v := cred.Tokens["expires_at"].(type) {
	case float64:
		expMs = int64(v)
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return false
		}
		expMs = n
	default:
		return false
	}
	return expMs <= now.Add(scalekitExpiryLead).UnixMilli()
}

func (a *API) refreshScalekitCredential(ctx context.Context, current *scalekitOperatorCredential) (*scalekitOperatorCredential, error) {
	tokenEndpoint, _ := current.Tokens["token_endpoint"].(string)
	clientID, _ := current.Tokens["client_id"].(string)
	refreshToken, _ := current.Tokens["refresh_token"].(string)
	clientSecret, _ := current.Tokens["client_secret"].(string)
	if tokenEndpoint == "" || clientID == "" || refreshToken == "" {
		return nil, errors.New("stored credential is missing token_endpoint/client_id/refresh_token — reconnect the workspace")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	reqCtx, cancel := context.WithTimeout(ctx, scalekitHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := a.signInClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, fmt.Errorf("MCP OAuth refresh_token grant returned HTTP %d", res.StatusCode)
	}
	var payload struct {
		AccessToken  string   `json:"access_token"`
		RefreshToken string   `json:"refresh_token"`
		ExpiresIn    *float64 `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.AccessToken == "" {
		return nil, errors.New("MCP OAuth refresh_token grant returned no access_token")
	}
	nextTokens := make(map[string]any, len(current.Tokens))
	maps.Copy(nextTokens, current.Tokens)
	nextTokens["access_token"] = payload.AccessToken
	if payload.RefreshToken != "" {
		nextTokens["refresh_token"] = payload.RefreshToken
	} else {
		nextTokens["refresh_token"] = refreshToken
	}
	if payload.ExpiresIn != nil && *payload.ExpiresIn > 0 {
		nextTokens["expires_at"] = strconv.FormatInt(time.Now().UnixMilli()+int64(*payload.ExpiresIn*1000), 10)
	}
	next := *current
	next.Tokens = nextTokens
	next.RefreshedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	return &next, nil
}

func resolveScalekitMCPURL(values map[string]string) string {
	raw := strings.TrimSpace(values["SCALEKIT_MCP_URL"])
	if raw == "" {
		raw = defaultScalekitMCPURL
	}
	return strings.TrimRight(raw, "/")
}

// scalekitCredentialMu covers each read-change-write of the deployment
// credential: a refresh token that the server rotates works only once.
var scalekitCredentialMu sync.Mutex

func (a *API) scalekitAccessToken(ctx context.Context) (string, error) {
	scalekitCredentialMu.Lock()
	defer scalekitCredentialMu.Unlock()
	dir := a.secretsDir()
	cred, err := readScalekitOperatorCredential(dir)
	if err != nil {
		return "", err
	}
	if cred != nil && scalekitCredentialNearExpiry(cred, time.Now()) {
		if refreshed, err := a.refreshScalekitCredential(ctx, cred); err == nil {
			cred = refreshed
			if err := writeScalekitOperatorCredential(dir, cred); err != nil {
				log.Printf("[signin] Scalekit workspace token not saved: %v", err)
			}
		} else {
			log.Printf("[signin] Scalekit workspace token refresh failed: %v", err)
		}
	}
	accessToken := ""
	if cred != nil && cred.Tokens != nil {
		accessToken, _ = cred.Tokens["access_token"].(string)
	}
	return accessToken, nil
}

func (a *API) callScalekitWorkspaceTool(ctx context.Context, tool string, args map[string]any) (string, error) {
	accessToken, err := a.scalekitAccessToken(ctx)
	if err != nil {
		return "", err
	}
	if accessToken == "" {
		return "", errors.New("Scalekit workspace is not connected — authorize it on the Single sign-on settings page first")
	}
	dir := a.secretsDir()
	store := secrets.ReadSoft(dir, nil)
	mcpURL := resolveScalekitMCPURL(store[signInProviderSection(scalekitPlugin)])
	return a.invokeScalekitMCPTool(ctx, mcpURL, accessToken, tool, args)
}

func (a *API) invokeScalekitMCPTool(ctx context.Context, mcpURL, accessToken, tool string, args map[string]any) (string, error) {
	client := a.signInClient()
	sessionID := ""
	protocolVersion := scalekitMCPProtocolVersion

	sendRPC := func(payload map[string]any, expectReply bool) (*jsObj, http.Header, error) {
		rawBody, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
		reqCtx, cancel := context.WithTimeout(ctx, ssoListTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, mcpURL, bytes.NewReader(rawBody))
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		if protocolVersion != "" {
			req.Header.Set("Mcp-Protocol-Version", protocolVersion)
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, res.Header, err
		}
		if res.StatusCode < 200 || res.StatusCode > 299 {
			return nil, res.Header, fmt.Errorf("Scalekit MCP HTTP %d", res.StatusCode)
		}
		if !expectReply || len(bytes.TrimSpace(raw)) == 0 {
			return nil, res.Header, nil
		}
		reply, err := parseMCPResponse(string(raw), res.Header.Get("Content-Type"))
		if err != nil {
			return nil, res.Header, err
		}
		if e, ok := reply.vals["error"].(*jsObj); ok && e != nil {
			msg, _ := e.vals["message"].(string)
			if msg == "" {
				msg = "Scalekit MCP error"
			}
			return nil, res.Header, errors.New(msg)
		}
		return reply, res.Header, nil
	}

	initReply, initHeader, err := sendRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": scalekitMCPProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "openneko-scalekit",
				"version": "1.0.0",
			},
		},
	}, true)
	if err != nil {
		return "", err
	}
	if sid := strings.TrimSpace(initHeader.Get("Mcp-Session-Id")); sid != "" {
		sessionID = sid
	}
	if initReply != nil {
		if resObj, ok := initReply.vals["result"].(*jsObj); ok && resObj != nil {
			if pv, ok := resObj.vals["protocolVersion"].(string); ok && pv != "" {
				protocolVersion = pv
			}
		}
	}
	defer func() {
		if sessionID == "" {
			return
		}
		delCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if req, err := http.NewRequestWithContext(delCtx, http.MethodDelete, mcpURL, nil); err == nil {
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Mcp-Session-Id", sessionID)
			if res, err := client.Do(req); err == nil {
				_ = res.Body.Close()
			}
		}
	}()

	_, _, _ = sendRPC(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}, false)

	if args == nil {
		args = map[string]any{}
	}
	callReply, _, err := sendRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      tool,
			"arguments": args,
		},
	}, true)
	if err != nil {
		return "", err
	}
	resObj, _ := callReply.vals["result"].(*jsObj)
	if resObj == nil {
		return "", fmt.Errorf("Scalekit tool %s returned no result — is the workspace connected?", tool)
	}
	var parts []string
	if content, ok := resObj.vals["content"].([]any); ok {
		for _, item := range content {
			cObj, ok := item.(*jsObj)
			if !ok || cObj == nil {
				continue
			}
			typ, _ := cObj.vals["type"].(string)
			txt, ok := cObj.vals["text"].(string)
			if typ == "text" && ok {
				parts = append(parts, txt)
			}
		}
	}
	text := strings.Join(parts, string([]byte{10, 10}))
	if isErr, _ := resObj.vals["isError"].(bool); isErr {
		if text != "" {
			return "", errors.New(text)
		}
		return "", fmt.Errorf("Scalekit tool %s failed", tool)
	}
	if text == "" {
		return "", fmt.Errorf("Scalekit tool %s returned no result — is the workspace connected?", tool)
	}
	return text, nil
}
