package webfront

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const (
	directoryMaxPages         = 500
	scalekitDirectoryPageSize = 100
)

var scalekitDirectoryCapabilities = DirectoryCapabilities{
	ProviderLabel: "Scalekit directory",
	Read:          DirectoryReadCapabilities{Users: true, Groups: true, Memberships: true},
	Write:         DirectoryWriteCapabilities{CreateUser: true, DeactivateUser: false},
}

type DirectoryUser struct {
	ExternalID string  `json:"externalId"`
	Sub        *string `json:"sub"`
	Email      string  `json:"email"`
	Name       *string `json:"name"`
	Active     bool    `json:"active"`
}

type DirectoryGroup struct {
	ExternalID string  `json:"externalId"`
	Name       *string `json:"name"`
}

type DirectoryMembership struct {
	UserExternalID  string `json:"userExternalId"`
	GroupExternalID string `json:"groupExternalId"`
}

type ListDirectoryResult struct {
	TenantID    string                `json:"tenantId"`
	Users       []DirectoryUser       `json:"users"`
	Groups      []DirectoryGroup      `json:"groups"`
	Memberships []DirectoryMembership `json:"memberships"`
	NextCursor  *string               `json:"nextCursor"`
}

type DirectoryChange struct {
	Op         string
	Email      string
	Name       *string
	ExternalID string
}

type ApplyDirectoryChangeResult struct {
	User *DirectoryUser `json:"user"`
}

type DirectoryReadCapabilities struct {
	Users       bool `json:"users"`
	Groups      bool `json:"groups"`
	Memberships bool `json:"memberships"`
}

type DirectoryWriteCapabilities struct {
	CreateUser     bool `json:"createUser"`
	DeactivateUser bool `json:"deactivateUser"`
}

type DirectoryCapabilities struct {
	ProviderLabel string                     `json:"providerLabel"`
	Read          DirectoryReadCapabilities  `json:"read"`
	Write         DirectoryWriteCapabilities `json:"write"`
}

func directoryChangeAllowed(capabilities DirectoryCapabilities, change DirectoryChange) bool {
	if change.Op == "create_user" {
		return capabilities.Write.CreateUser
	}
	return capabilities.Write.DeactivateUser
}

type DirectoryProviderInfo struct {
	ID           string                `json:"id"`
	Capabilities DirectoryCapabilities `json:"capabilities"`
}

type DirectorySource interface {
	GetDirectoryProvider() *DirectoryProviderInfo
	ListDirectory(ctx context.Context, cursor *string) (ListDirectoryResult, error)
	ApplyDirectoryChange(ctx context.Context, change DirectoryChange) (ApplyDirectoryChangeResult, error)
}

type DirectoryProviderSummary struct {
	PluginName         string `json:"pluginName"`
	ProviderLabel      string `json:"providerLabel"`
	CanCreateUsers     bool   `json:"canCreateUsers"`
	CanDeactivateUsers bool   `json:"canDeactivateUsers"`
}

type DirectorySyncStateView struct {
	Status     string         `json:"status"`
	StartedAt  *string        `json:"startedAt"`
	FinishedAt *string        `json:"finishedAt"`
	LastError  *string        `json:"lastError"`
	Stats      map[string]any `json:"stats"`
}

type DirectoryStatus struct {
	Provider *DirectoryProviderSummary `json:"provider"`
	State    DirectorySyncStateView    `json:"state"`
}

type DirectorySnapshotGroup struct {
	ExternalID  string  `json:"externalId"`
	DisplayName *string `json:"displayName"`
}

type DirectorySnapshot struct {
	OrgID       string                   `json:"orgId"`
	Provider    string                   `json:"provider"`
	TenantID    string                   `json:"tenantId"`
	CreateUsers bool                     `json:"createUsers"`
	Users       []DirectoryUser          `json:"users"`
	Groups      []DirectorySnapshotGroup `json:"groups"`
	Memberships []DirectoryMembership    `json:"memberships"`
}

type DirectorySyncStats struct {
	UsersCreated      int `json:"usersCreated"`
	UsersDisabled     int `json:"usersDisabled"`
	UsersEnabled      int `json:"usersEnabled"`
	UsersMatched      int `json:"usersMatched"`
	Groups            int `json:"groups"`
	GroupsDeactivated int `json:"groupsDeactivated"`
	Memberships       int `json:"memberships"`
}

type DirectorySyncError struct {
	Code    string
	Message string
}

func (e *DirectorySyncError) Error() string {
	return e.Message
}

func directorySyncErr(code, message string) error {
	return &DirectorySyncError{Code: code, Message: message}
}

func (a *API) directorySource() DirectorySource {
	if a.DirectorySource != nil {
		return a.DirectorySource
	}
	return &scalekitDirectorySource{api: a}
}

func (a *API) DirectoryStatus(ctx context.Context, orgID string) (DirectoryStatus, error) {
	source := a.directorySource()
	var provider *DirectoryProviderSummary
	if info := source.GetDirectoryProvider(); info != nil {
		provider = &DirectoryProviderSummary{
			PluginName:         info.ID,
			ProviderLabel:      info.Capabilities.ProviderLabel,
			CanCreateUsers:     info.Capabilities.Write.CreateUser,
			CanDeactivateUsers: info.Capabilities.Write.DeactivateUser,
		}
	}
	state := DirectorySyncStateView{
		Status: "never",
		Stats:  map[string]any{},
	}
	if a.DB != nil && orgID != "" {
		var status string
		var startedAt, finishedAt *time.Time
		var lastError *string
		var rawStats []byte
		err := a.DB.QueryRow(ctx, `
			select status, started_at, finished_at, last_error, stats
			from directory_sync_state
			where org_id = $1
			limit 1`, orgID).Scan(&status, &startedAt, &finishedAt, &lastError, &rawStats)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return DirectoryStatus{}, err
		}
		if err == nil {
			state.Status = status
			state.StartedAt = formatJSISO(startedAt)
			state.FinishedAt = formatJSISO(finishedAt)
			state.LastError = lastError
			if len(rawStats) > 0 {
				var parsed map[string]any
				if json.Unmarshal(rawStats, &parsed) == nil && parsed != nil {
					state.Stats = parsed
				}
			}
		}
	}
	return DirectoryStatus{
		Provider: provider,
		State:    state,
	}, nil
}

func CollectDirectorySnapshot(ctx context.Context, source DirectorySource, orgID string) (DirectorySnapshot, error) {
	var provider *DirectoryProviderInfo
	if source != nil {
		provider = source.GetDirectoryProvider()
	}
	if provider == nil {
		return DirectorySnapshot{}, directorySyncErr("no_provider", "no directory provider is configured")
	}
	snapshot := DirectorySnapshot{
		OrgID:       orgID,
		Provider:    provider.ID,
		TenantID:    "",
		CreateUsers: provider.Capabilities.Read.Users,
		Users:       []DirectoryUser{},
		Groups:      []DirectorySnapshotGroup{},
		Memberships: []DirectoryMembership{},
	}
	var cursor *string
	for page := 0; page < directoryMaxPages; page++ {
		result, err := source.ListDirectory(ctx, cursor)
		if err != nil {
			return DirectorySnapshot{}, err
		}
		if snapshot.TenantID != "" && snapshot.TenantID != result.TenantID {
			return DirectorySnapshot{}, directorySyncErr("invalid", "directory provider changed tenant between pages")
		}
		snapshot.TenantID = result.TenantID
		snapshot.Users = append(snapshot.Users, result.Users...)
		for _, g := range result.Groups {
			snapshot.Groups = append(snapshot.Groups, DirectorySnapshotGroup{
				ExternalID:  g.ExternalID,
				DisplayName: g.Name,
			})
		}
		snapshot.Memberships = append(snapshot.Memberships, result.Memberships...)
		if result.NextCursor == nil || *result.NextCursor == "" {
			return snapshot, nil
		}
		next := *result.NextCursor
		cursor = &next
	}
	return DirectorySnapshot{}, directorySyncErr("invalid", fmt.Sprintf("directory provider returned more than %d pages", directoryMaxPages))
}

func (a *API) ReconcileDirectorySnapshot(ctx context.Context, snapshot DirectorySnapshot) (DirectorySyncStats, error) {
	orgID := snapshot.OrgID
	provider := snapshot.Provider
	tenantID := snapshot.TenantID
	var stats DirectorySyncStats
	err := a.withAdministratorGuard(ctx, orgID, func(tx pgx.Tx) error {
		stats = DirectorySyncStats{
			Groups: len(snapshot.Groups),
		}
		groupIDs := make(map[string]string, len(snapshot.Groups))
		keepSet := make(map[string]bool, len(snapshot.Groups))
		keep := make([]string, 0, len(snapshot.Groups))
		for _, group := range snapshot.Groups {
			var id string
			err := tx.QueryRow(ctx, `
				insert into sso_group (org_id, provider, tenant_id, external_id, display_name, active, updated_at)
				values ($1, $2, $3, $4, $5, true, now())
				on conflict (org_id, provider, tenant_id, external_id)
				do update set display_name = excluded.display_name, active = true, updated_at = now()
				returning id::text`,
				orgID, provider, tenantID, group.ExternalID, group.DisplayName).Scan(&id)
			if err != nil {
				return err
			}
			groupIDs[group.ExternalID] = id
			if !keepSet[id] {
				keepSet[id] = true
				keep = append(keep, id)
			}
		}

		ct, err := tx.Exec(ctx, `
			update sso_group set active = false, updated_at = now()
			where org_id = $1 and provider = $2 and tenant_id = $3 and active
			  and not (id = any($4::uuid[]))`,
			orgID, provider, tenantID, keep)
		if err != nil {
			return err
		}
		stats.GroupsDeactivated = int(ct.RowsAffected())

		userIDs := make(map[string]string, len(snapshot.Users))
		seenSet := make(map[string]bool, len(snapshot.Users))
		seen := make([]string, 0, len(snapshot.Users))
		addSeen := func(id string) {
			if !seenSet[id] {
				seenSet[id] = true
				seen = append(seen, id)
			}
		}

		for _, user := range snapshot.Users {
			email := strings.ToLower(jsTrim(user.Email))
			var matchID, matchSource string
			var matchDisabled bool
			err := tx.QueryRow(ctx, `
				select id, source, disabled_at is not null as disabled from app_user
				where org_id = $1
				  and (($2::text is not null and sub = $2::text) or lower(email) = $3)
				order by (sub = $2::text) desc nulls last
				limit 1`,
				orgID, user.Sub, email).Scan(&matchID, &matchSource, &matchDisabled)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				stats.UsersMatched++
				addSeen(matchID)
				userIDs[user.ExternalID] = matchID
				if matchSource == provider && matchDisabled == user.Active {
					if user.Active {
						if _, err := tx.Exec(ctx, `update app_user set disabled_at = null, updated_at = now() where id = $1`, matchID); err != nil {
							return err
						}
						stats.UsersEnabled++
					} else {
						if _, err := tx.Exec(ctx, `update app_user set disabled_at = now(), updated_at = now() where id = $1`, matchID); err != nil {
							return err
						}
						stats.UsersDisabled++
					}
				}
				continue
			}
			if !user.Active || !snapshot.CreateUsers {
				continue
			}
			suffix, err := randomURL(9)
			if err != nil {
				return err
			}
			id := "usr_" + suffix
			if _, err := tx.Exec(ctx, `
				insert into app_user (id, org_id, email, name, sub, source)
				values ($1, $2, $3, $4, $5, $6)`,
				id, orgID, email, user.Name, user.Sub, provider); err != nil {
				return err
			}
			stats.UsersCreated++
			addSeen(id)
			userIDs[user.ExternalID] = id
		}

		goneCt, err := tx.Exec(ctx, `
			update app_user set disabled_at = now(), updated_at = now()
			where org_id = $1 and source = $2 and disabled_at is null
			  and not (id = any($3::text[]))`,
			orgID, provider, seen)
		if err != nil {
			return err
		}
		stats.UsersDisabled += int(goneCt.RowsAffected())

		if len(keep) > 0 {
			if _, err := tx.Exec(ctx, `
				delete from sso_group_membership
				where org_id = $1 and group_id = any($2::uuid[])`,
				orgID, keep); err != nil {
				return err
			}
		}
		for _, m := range snapshot.Memberships {
			userID := userIDs[m.UserExternalID]
			groupID := groupIDs[m.GroupExternalID]
			if userID == "" || groupID == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `
				insert into sso_group_membership (org_id, group_id, user_id, synced_at)
				values ($1, $2::uuid, $3, now()) on conflict do nothing`,
				orgID, groupID, userID); err != nil {
				return err
			}
			stats.Memberships++
		}
		return recomputeRuleMemberships(ctx, tx, orgID, nil)
	})
	return stats, err
}

func (a *API) RunDirectorySync(ctx context.Context, orgID string) (DirectorySyncStats, error) {
	source := a.directorySource()
	provider := source.GetDirectoryProvider()
	if provider == nil {
		return DirectorySyncStats{}, directorySyncErr("no_provider", "no directory provider is configured")
	}
	var claimed string
	err := a.DB.QueryRow(ctx, `
		insert into directory_sync_state (org_id, provider, status, started_at, finished_at, last_error, updated_at)
		values ($1, $2, 'running', now(), null, null, now())
		on conflict (org_id) do update set
		  provider = excluded.provider, status = 'running', started_at = now(), finished_at = null, last_error = null, updated_at = now()
		where directory_sync_state.status <> 'running' or directory_sync_state.started_at < now() - interval '1 hour'
		returning org_id`, orgID, provider.ID).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return DirectorySyncStats{}, directorySyncErr("running", "a directory sync is already running")
	}
	if err != nil {
		return DirectorySyncStats{}, err
	}

	markFailed := func(syncErr error) {
		_, _ = a.DB.Exec(context.WithoutCancel(ctx), `
			update directory_sync_state
			set status = 'failed', finished_at = now(), last_error = $2, updated_at = now()
			where org_id = $1`, orgID, syncErr.Error())
	}

	snapshot, err := CollectDirectorySnapshot(ctx, source, orgID)
	if err != nil {
		markFailed(err)
		return DirectorySyncStats{}, err
	}
	stats, err := a.ReconcileDirectorySnapshot(ctx, snapshot)
	if err != nil {
		markFailed(err)
		return DirectorySyncStats{}, err
	}
	statsJSON, _ := json.Marshal(stats)
	if _, err := a.DB.Exec(context.WithoutCancel(ctx), `
		update directory_sync_state
		set status = 'ok', finished_at = now(), stats = $2::jsonb, updated_at = now()
		where org_id = $1`, orgID, string(statsJSON)); err != nil {
		return DirectorySyncStats{}, err
	}
	return stats, nil
}

func CreateDirectoryUser(ctx context.Context, source DirectorySource, email string, name *string) (ApplyDirectoryChangeResult, error) {
	var provider *DirectoryProviderInfo
	if source != nil {
		provider = source.GetDirectoryProvider()
	}
	if provider == nil {
		return ApplyDirectoryChangeResult{}, directorySyncErr("no_provider", "no directory provider is configured")
	}
	if !provider.Capabilities.Write.CreateUser {
		return ApplyDirectoryChangeResult{}, directorySyncErr("unsupported", provider.Capabilities.ProviderLabel+" does not create users")
	}
	return source.ApplyDirectoryChange(ctx, DirectoryChange{
		Op:    "create_user",
		Email: email,
		Name:  name,
	})
}

func (a *API) CreateDirectoryUser(ctx context.Context, email string, name *string) (ApplyDirectoryChangeResult, error) {
	cleanedEmail := strings.ToLower(jsTrim(email))
	if !strings.Contains(cleanedEmail, "@") {
		return ApplyDirectoryChangeResult{}, errors.New("a valid email address is required")
	}
	var cleanedName *string
	if name != nil {
		if trimmed := jsTrim(*name); trimmed != "" {
			cleanedName = &trimmed
		}
	}
	return CreateDirectoryUser(ctx, a.directorySource(), cleanedEmail, cleanedName)
}

func (a *API) directoryFailure(w http.ResponseWriter, _ *http.Request, err error) {
	var code string
	var de *DirectorySyncError
	var ge *groupError
	if errors.As(err, &de) {
		code = de.Code
	} else if errors.As(err, &ge) {
		code = ge.code
	}
	status := http.StatusInternalServerError
	switch code {
	case "no_provider":
		status = http.StatusNotFound
	case "running", "lockout":
		status = http.StatusConflict
	case "unsupported":
		status = http.StatusBadRequest
	}
	var codeVal *string
	if code != "" {
		codeVal = &code
	}
	sendJSON(w, status, struct {
		Error string  `json:"error"`
		Code  *string `json:"code"`
	}{err.Error(), codeVal})
}

type scalekitDirectorySource struct {
	api *API
}

func (s *scalekitDirectorySource) GetDirectoryProvider() *DirectoryProviderInfo {
	store := secrets.ReadSoft(s.api.secretsDir(), nil)
	if store[signInSectionName]["PROVIDER"] != scalekitPlugin {
		return nil
	}
	return &DirectoryProviderInfo{
		ID:           scalekitPlugin,
		Capabilities: scalekitDirectoryCapabilities,
	}
}

func (s *scalekitDirectorySource) clientForEnv() (*scalekitDirectory, error) {
	if s.GetDirectoryProvider() == nil {
		return nil, errors.New("no directory provider is configured")
	}
	store := secrets.ReadSoft(s.api.secretsDir(), nil)
	env := store[signInProviderSection(scalekitPlugin)]
	resolved, err := resolveScalekitEnv(env)
	if err != nil {
		return nil, err
	}
	orgID := jsTrim(env["SCALEKIT_ORGANIZATION_ID"])
	if orgID == "" {
		return nil, errors.New("SCALEKIT_ORGANIZATION_ID not set — select the organization on the Single sign-on settings page")
	}
	return newScalekitDirectory(scalekitDirectoryOptions{
		EnvironmentURL: resolved.EnvironmentURL,
		ClientID:       resolved.ClientID,
		ClientSecret:   resolved.ClientSecret,
		OrganizationID: orgID,
		Client:         s.api.signInClient(),
		Timeout:        scalekitHTTPTimeout,
	})
}

func (s *scalekitDirectorySource) ListDirectory(ctx context.Context, cursor *string) (ListDirectoryResult, error) {
	dir, err := s.clientForEnv()
	if err != nil {
		return ListDirectoryResult{}, err
	}
	return dir.List(ctx, cursor)
}

func (s *scalekitDirectorySource) ApplyDirectoryChange(ctx context.Context, change DirectoryChange) (ApplyDirectoryChangeResult, error) {
	provider := s.GetDirectoryProvider()
	if provider == nil {
		return ApplyDirectoryChangeResult{}, errors.New("no directory provider is configured")
	}
	if !directoryChangeAllowed(provider.Capabilities, change) {
		return ApplyDirectoryChangeResult{}, fmt.Errorf("%s does not accept %q", provider.Capabilities.ProviderLabel, change.Op)
	}
	dir, err := s.clientForEnv()
	if err != nil {
		return ApplyDirectoryChangeResult{}, err
	}
	return dir.Apply(ctx, change)
}

type scalekitDirectoryOptions struct {
	EnvironmentURL string
	ClientID       string
	ClientSecret   string
	OrganizationID string
	Client         *http.Client
	Timeout        time.Duration
}

type scalekitDirectory struct {
	environmentURL string
	clientID       string
	clientSecret   string
	organizationID string
	orgPath        string
	client         *http.Client
	timeout        time.Duration

	tokenMu sync.Mutex
	token   string
}

type scalekitDirectoryCursor struct {
	Directory int    `json:"directory"`
	Phase     string `json:"phase"`
	PageToken string `json:"pageToken"`
}

func newScalekitDirectory(opts scalekitDirectoryOptions) (*scalekitDirectory, error) {
	envURL, err := normalizeScalekitEnvironmentURL(opts.EnvironmentURL)
	if err != nil {
		return nil, err
	}
	orgID := jsTrim(opts.OrganizationID)
	if orgID == "" {
		return nil, errors.New("SCALEKIT_ORGANIZATION_ID is empty")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = scalekitHTTPTimeout
	}
	client := opts.Client
	if client == nil {
		client = defaultSignInClient
	}
	return &scalekitDirectory{
		environmentURL: envURL,
		clientID:       opts.ClientID,
		clientSecret:   opts.ClientSecret,
		organizationID: orgID,
		orgPath:        "/api/v1/organizations/" + url.PathEscape(orgID),
		client:         client,
		timeout:        timeout,
	}, nil
}

func (d *scalekitDirectory) accessToken(ctx context.Context) (string, error) {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	if d.token != "" {
		return d.token, nil
	}
	base, err := url.Parse(d.environmentURL)
	if err != nil {
		return "", err
	}
	target := base.ResolveReference(&url.URL{Path: "/oauth/token"}).String()
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", d.clientID)
	form.Set("client_secret", d.clientSecret)
	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Accept":       "application/json",
	}
	var result map[string]any
	if err := scalekitRequest(ctx, d.client, d.timeout, http.MethodPost, target, headers, form.Encode(), "client credentials token", &result); err != nil {
		return "", err
	}
	value := scalekitText(result["access_token"])
	if value == "" {
		return "", errors.New("Scalekit client credentials token returned no access_token")
	}
	d.token = value
	return value, nil
}

func (d *scalekitDirectory) api(ctx context.Context, method, path, description, body string, query map[string]string, out any) error {
	base, err := url.Parse(d.environmentURL)
	if err != nil {
		return err
	}
	target, err := base.Parse(path)
	if err != nil {
		return err
	}
	q := target.Query()
	for k, v := range query {
		if v != "" {
			q.Set(k, v)
		}
	}
	target.RawQuery = q.Encode()
	token, err := d.accessToken(ctx)
	if err != nil {
		return err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/json",
	}
	if body != "" {
		headers["Content-Type"] = "application/json"
	}
	return scalekitRequest(ctx, d.client, d.timeout, method, target.String(), headers, body, description, out)
}

func (d *scalekitDirectory) directoryIDs(ctx context.Context) ([]string, error) {
	var result map[string]any
	if err := d.api(ctx, http.MethodGet, d.orgPath+"/directories", "list directories", "", nil, &result); err != nil {
		return nil, err
	}
	rawList, _ := result["directories"].([]any)
	ids := make([]string, 0, len(rawList))
	for _, item := range rawList {
		if enabled, ok := scalekitField(item, "enabled", "enabled").(bool); ok && !enabled {
			continue
		}
		if id := scalekitText(scalekitField(item, "id", "id")); id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (d *scalekitDirectory) List(ctx context.Context, rawCursor *string) (ListDirectoryResult, error) {
	cursor, err := decodeScalekitDirectoryCursor(rawCursor)
	if err != nil {
		return ListDirectoryResult{}, err
	}
	ids, err := d.directoryIDs(ctx)
	if err != nil {
		return ListDirectoryResult{}, err
	}
	empty := ListDirectoryResult{
		TenantID:    d.organizationID,
		Users:       []DirectoryUser{},
		Groups:      []DirectoryGroup{},
		Memberships: []DirectoryMembership{},
		NextCursor:  nil,
	}
	if cursor.Directory < 0 || cursor.Directory >= len(ids) {
		return empty, nil
	}
	directoryID := ids[cursor.Directory]
	query := map[string]string{
		"page_size":  fmt.Sprintf("%d", scalekitDirectoryPageSize),
		"page_token": cursor.PageToken,
	}
	if cursor.Phase == "users" {
		query["include_detail"] = "true"
	}
	path := fmt.Sprintf("%s/directories/%s/%s", d.orgPath, url.PathEscape(directoryID), cursor.Phase)
	var page map[string]any
	if err := d.api(ctx, http.MethodGet, path, "list directory "+cursor.Phase, "", query, &page); err != nil {
		return ListDirectoryResult{}, err
	}
	pageToken := scalekitText(scalekitField(page, "next_page_token", "nextPageToken"))
	var next *scalekitDirectoryCursor
	if pageToken != "" {
		next = &scalekitDirectoryCursor{Directory: cursor.Directory, Phase: cursor.Phase, PageToken: pageToken}
	} else if cursor.Phase == "groups" {
		next = &scalekitDirectoryCursor{Directory: cursor.Directory, Phase: "users", PageToken: ""}
	} else if cursor.Directory+1 < len(ids) {
		next = &scalekitDirectoryCursor{Directory: cursor.Directory + 1, Phase: "groups", PageToken: ""}
	}
	var nextCursor *string
	if next != nil {
		encoded := encodeScalekitDirectoryCursor(*next)
		nextCursor = &encoded
	}
	if cursor.Phase == "groups" {
		rawGroups, _ := page["groups"].([]any)
		groups := make([]DirectoryGroup, 0, len(rawGroups))
		for _, g := range rawGroups {
			extID := scalekitGroupKey(g)
			if extID == "" {
				continue
			}
			var namePtr *string
			if dn := scalekitText(scalekitField(g, "display_name", "displayName")); dn != "" {
				namePtr = &dn
			}
			groups = append(groups, DirectoryGroup{
				ExternalID: extID,
				Name:       namePtr,
			})
		}
		empty.Groups = groups
		empty.NextCursor = nextCursor
		return empty, nil
	}

	rawUsers, _ := page["users"].([]any)
	users := make([]DirectoryUser, 0, len(rawUsers))
	memberships := []DirectoryMembership{}
	for _, raw := range rawUsers {
		user := scalekitToDirectoryUser(raw)
		if user == nil {
			continue
		}
		users = append(users, *user)
		rawGroups, _ := scalekitField(raw, "groups", "groups").([]any)
		for _, g := range rawGroups {
			if key := scalekitGroupKey(g); key != "" {
				memberships = append(memberships, DirectoryMembership{
					UserExternalID:  user.ExternalID,
					GroupExternalID: key,
				})
			}
		}
	}
	empty.Users = users
	empty.Memberships = memberships
	empty.NextCursor = nextCursor
	return empty, nil
}

func (d *scalekitDirectory) Apply(ctx context.Context, change DirectoryChange) (ApplyDirectoryChangeResult, error) {
	if change.Op != "create_user" {
		return ApplyDirectoryChangeResult{}, fmt.Errorf("Scalekit directory does not support %q; the identity provider owns user status", change.Op)
	}
	payload := map[string]any{
		"email": change.Email,
	}
	if change.Name != nil && *change.Name != "" {
		payload["user_profile"] = map[string]any{"name": *change.Name}
	}
	rawBody, err := json.Marshal(payload)
	if err != nil {
		return ApplyDirectoryChangeResult{}, err
	}
	var result map[string]any
	if err := d.api(ctx, http.MethodPost, d.orgPath+"/users", "create organization user", string(rawBody), map[string]string{"send_invitation_email": "true"}, &result); err != nil {
		return ApplyDirectoryChangeResult{}, err
	}
	user, _ := result["user"].(map[string]any)
	id := scalekitText(user["id"])
	if id == "" {
		return ApplyDirectoryChangeResult{User: nil}, nil
	}
	email := scalekitText(user["email"])
	if email == "" {
		email = change.Email
	}
	profile := scalekitField(user, "user_profile", "userProfile")
	nameStr := scalekitText(scalekitField(profile, "name", "name"))
	var namePtr *string
	if nameStr != "" {
		namePtr = &nameStr
	} else if change.Name != nil && *change.Name != "" {
		n := *change.Name
		namePtr = &n
	}
	return ApplyDirectoryChangeResult{
		User: &DirectoryUser{
			ExternalID: id,
			Sub:        nil,
			Email:      email,
			Name:       namePtr,
			Active:     true,
		},
	}, nil
}

func scalekitField(obj any, snake, camel string) any {
	m, ok := obj.(map[string]any)
	if !ok || m == nil {
		return nil
	}
	if v, exists := m[snake]; exists && v != nil {
		return v
	}
	return m[camel]
}

func scalekitText(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return jsTrim(s)
}

func scalekitGroupKey(group any) string {
	if dn := scalekitText(scalekitField(group, "display_name", "displayName")); dn != "" {
		return dn
	}
	return scalekitText(scalekitField(group, "id", "id"))
}

func scalekitToDirectoryUser(raw any) *DirectoryUser {
	email := scalekitText(scalekitField(raw, "email", "email"))
	if email == "" {
		if emails, ok := scalekitField(raw, "emails", "emails").([]any); ok && len(emails) > 0 {
			email = scalekitText(emails[0])
		}
	}
	email = strings.ToLower(email)
	id := scalekitText(scalekitField(raw, "id", "id"))
	if id == "" || email == "" {
		return nil
	}
	given := scalekitText(scalekitField(raw, "given_name", "givenName"))
	family := scalekitText(scalekitField(raw, "family_name", "familyName"))
	var parts []string
	if given != "" {
		parts = append(parts, given)
	}
	if family != "" {
		parts = append(parts, family)
	}
	nameStr := strings.Join(parts, " ")
	if nameStr == "" {
		nameStr = scalekitText(scalekitField(raw, "preferred_username", "preferredUsername"))
	}
	var namePtr *string
	if nameStr != "" {
		namePtr = &nameStr
	}
	active := true
	if detail := scalekitField(raw, "user_detail", "userDetail"); detail != nil {
		if b, ok := scalekitField(detail, "active", "active").(bool); ok && !b {
			active = false
		}
	}
	return &DirectoryUser{
		ExternalID: id,
		Sub:        nil,
		Email:      email,
		Name:       namePtr,
		Active:     active,
	}
}

func encodeScalekitDirectoryCursor(cursor scalekitDirectoryCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeScalekitDirectoryCursor(raw *string) (scalekitDirectoryCursor, error) {
	if raw == nil || *raw == "" {
		return scalekitDirectoryCursor{Directory: 0, Phase: "groups", PageToken: ""}, nil
	}
	decoded, err := decodeBase64URLLoose(*raw)
	if err == nil {
		var parsed map[string]any
		if json.Unmarshal(decoded, &parsed) == nil {
			dirFloat, okDir := parsed["directory"].(float64)
			phase, _ := parsed["phase"].(string)
			if okDir && !math.IsNaN(dirFloat) && !math.IsInf(dirFloat, 0) && math.Trunc(dirFloat) == dirFloat && (phase == "groups" || phase == "users") {
				return scalekitDirectoryCursor{
					Directory: int(dirFloat),
					Phase:     phase,
					PageToken: scalekitText(parsed["pageToken"]),
				}, nil
			}
		}
	}
	return scalekitDirectoryCursor{}, errors.New("directory cursor is not valid")
}
