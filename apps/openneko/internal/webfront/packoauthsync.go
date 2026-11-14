package webfront

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const (
	packOAuthRefreshEvery  = time.Minute
	packOAuthRefreshMargin = 5 * time.Minute
)

func (a *API) installedPackInstall(ctx context.Context, orgID, packID string) (*packInstallRow, error) {
	var row packInstallRow
	var raw []byte
	err := a.DB.QueryRow(ctx, `select version, status, source, config::text from pack_install
		where org_id = $1 and pack_id = $2 and status = 'installed' limit 1`, orgID, packID).Scan(&row.Version, &row.Status, &row.Source, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsed, err := jsParse(raw)
	if err != nil {
		return nil, err
	}
	row.Config = jsObjOrEmpty(parsed)
	return &row, nil
}

// syncPackOAuth is PackService.syncOAuthConnection(): it applies a
// deployment-scope token to the installed pack's GraphJin sources, or blocks
// those sources, and sets the readiness of the actions that use them.
func (a *API) syncPackOAuth(ctx context.Context, orgID, packID, connectionKey string, enabled bool) error {
	row, err := a.installedPackInstall(ctx, orgID, packID)
	if err != nil || row == nil {
		return err
	}
	authored, err := installedPackBundle(orgID, packID, row)
	if err != nil {
		return err
	}
	if authored.Manifest.Artifacts.GraphJin == nil {
		return nil
	}
	runtime, _ := plainJSON(row.Config.vals["_runtime"]).(map[string]any)
	selection, _ := runtime["source"].(map[string]any)
	if selection == nil {
		return fmt.Errorf("installed pack %s has no data source binding", packID)
	}
	bindings := map[string]string{}
	if m, ok := runtime["bindings"].(map[string]any); ok {
		for k, v := range m {
			bindings[k], _ = v.(string)
		}
	}
	_, boundQueries, err := bindPackQueries(authored, bindings)
	if err != nil {
		return err
	}
	bundle := boundPackBundle(authored, boundQueries)
	var conn *packs.OAuthConnection
	for i := range bundle.Manifest.OAuth {
		if bundle.Manifest.OAuth[i].Key == connectionKey {
			conn = &bundle.Manifest.OAuth[i]
		}
	}
	if conn == nil {
		return fmt.Errorf("installed pack %s does not declare OAuth connection %s", packID, connectionKey)
	}
	supplied := &jsObj{vals: map[string]any{}}
	for _, k := range row.Config.ordered() {
		if bundle.Manifest.InputDeclared(k) {
			supplied.set(k, row.Config.vals[k])
		}
	}
	resolved, err := resolvePackInputs(bundle, supplied)
	if err != nil {
		return err
	}
	inputs, _ := plainJSON(resolved).(map[string]any)
	var secretValues map[string]string
	if enabled {
		if secretValues, err = a.resolvePackSecrets(bundle, &jsObj{vals: map[string]any{}}); err != nil {
			return err
		}
	}
	source, err := a.resolveSelectedPackSource(ctx, orgID, selection)
	if err != nil {
		return err
	}
	graphjin, err := a.resolvePackGraphjinTarget(ctx, orgID, source.GraphqlURL)
	if err != nil {
		return err
	}

	tokenRef := "{{secret." + conn.AccessToken + "}}"
	owned := map[string]bool{}
	connected := map[string]bool{}
	var patches []any
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindSource || bindings[art.Key] != "" {
			continue
		}
		owned[art.TargetRef] = true
		auth, _ := art.Record()["auth"].(map[string]any)
		if auth["token"] != tokenRef {
			continue
		}
		connected[packString(art.Record()["name"])] = true
		patches = append(patches, map[string]any{"name": art.TargetRef, "read_only": true,
			"access": map[string]any{"read": "blocked", "write": "blocked", "delete": "blocked"}})
	}
	if !enabled && len(patches) == 0 {
		return fmt.Errorf("OAuth connection %s is not bound to an installed API source", connectionKey)
	}
	access := "authenticated"
	if graphjin.Mode == "api" && graphjin.Anonymous {
		access = "public"
	}
	in := packGraphjinUpdate{ownedSources: owned}
	update := map[string]any{"source_patches": patches}
	if graphjin.Mode == "api" {
		var api *packAPIArtifacts
		in.ownedSavedQueries = map[string]bool{}
		if enabled {
			if api, err = graphjinAPIArtifacts(bundle, inputs); err != nil {
				return err
			}
			for _, q := range api.savedQueries {
				m, _ := q.(map[string]any)
				in.ownedSavedQueries[packString(m["name"])] = true
			}
			if update, err = declarativeGraphjinUpdate(bundle, inputs, secretValues, nil, bindings, api, access); err != nil {
				return err
			}
		}
		in.update = jsObjOrEmpty(jsValueOf(update))
		_, err = a.applyPackGraphjinConfigLive(ctx, graphjin.Endpoint, orgID, in)
	} else {
		if enabled {
			if update, err = declarativeGraphjinUpdate(bundle, inputs, secretValues, nil, bindings, nil, access); err != nil {
				return err
			}
		}
		in.update = jsObjOrEmpty(jsValueOf(update))
		_, err = a.applyPackGraphjinConfig(ctx, graphjinGraphQLEndpoint(source.GraphqlURL), orgID, graphjin.ConfigFile, in, true)
	}
	if err != nil {
		return err
	}

	var kinds []string
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindAction {
			continue
		}
		adapter, _ := art.Record()["adapter"].(map[string]any)
		if adapter["kind"] == "graphjin_api_operation" && connected[packString(adapter["source"])] {
			kinds = append(kinds, packString(art.Record()["kind"]))
		}
	}
	if len(kinds) == 0 {
		return nil
	}
	readiness, reason := "ready", (*string)(nil)
	if !enabled {
		readiness, reason = "blocked", new("oauth_not_connected")
	}
	_, err = a.DB.Exec(ctx, `update pack_action_definition set readiness = $1, readiness_reason = $2, updated_at = now()
		where org_id = $3 and kind = any($4)`, readiness, reason, orgID, kinds)
	return err
}

// StartPackOAuthRefresh runs PackOAuthService.refreshDue() each minute until
// ctx ends.
func (a *API) StartPackOAuthRefresh(ctx context.Context) {
	go func() {
		tick := time.NewTicker(packOAuthRefreshEvery)
		defer tick.Stop()
		for {
			if n, err := a.RefreshPackOAuth(ctx); err != nil {
				log.Printf("[packs] OAuth refresh failed: %v", err)
			} else if n > 0 {
				log.Printf("[packs] refreshed %d OAuth connection(s)", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

// RefreshPackOAuth refreshes each deployment-scope token that ends within
// five minutes, and applies a token whose sync did not finish.
func (a *API) RefreshPackOAuth(ctx context.Context) (int, error) {
	orgID, err := a.existingOrgID(ctx)
	if err != nil || orgID == "" {
		return 0, err
	}
	rows, err := a.DB.Query(ctx, `select pack_id from pack_install where org_id = $1 and status = 'installed' order by pack_id`, orgID)
	if err != nil {
		return 0, err
	}
	packIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	refreshed := 0
	var failures []error
	for _, packID := range packIDs {
		n, err := a.refreshPackOAuth(ctx, orgID, packID)
		refreshed += n
		if err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return refreshed, fmt.Errorf("%d pack OAuth refresh operation(s) failed: %w", len(failures), errors.Join(failures...))
	}
	return refreshed, nil
}

func (a *API) refreshPackOAuth(ctx context.Context, orgID, packID string) (int, error) {
	row, err := a.installedPackInstall(ctx, orgID, packID)
	if err != nil || row == nil {
		return 0, err
	}
	bundle, err := installedPackBundle(orgID, packID, row)
	if err != nil {
		return 0, err
	}
	refreshed := 0
	for _, c := range bundle.Manifest.OAuth {
		if c.Scope == "user" {
			continue
		}
		p := &packOAuth{a: a, orgID: orgID, packID: packID, conn: c, installed: true, graphjin: bundle.Manifest.Artifacts.GraphJin != nil}
		store, err := secrets.Read(a.SecretsDir)
		if err != nil {
			return refreshed, err
		}
		section := p.section(store)
		pendingKey := oauthStateKey(c.Key, "SYNC_PENDING")
		pending := section[pendingKey] == "true"
		expiresAt, err := time.Parse(time.RFC3339Nano, section[oauthStateKey(c.Key, "EXPIRES_AT")])
		fresh := err == nil && expiresAt.After(time.Now().Add(packOAuthRefreshMargin))
		if !pending && fresh {
			continue
		}
		clientID := packString(row.Config.vals[c.ClientIDInput])
		if row.Config.vals[c.ClientIDInput] == nil {
			clientID = section[p.clientIDKey()]
		}
		clientSecret, refreshToken := section[p.clientSecretKey()], section[envKey(c.RefreshToken)]
		if clientID == "" || clientSecret == "" || refreshToken == "" {
			continue
		}
		if !fresh {
			scopes := c.Scopes
			if s, ok := section[oauthStateKey(c.Key, "SCOPES")]; ok {
				scopes = strings.Fields(s)
			}
			tokens, err := refreshPackToken(ctx, p.connection(), clientID, clientSecret, refreshToken, scopes)
			if err != nil {
				return refreshed, err
			}
			if _, err := p.updateSection(func(s map[string]string) {
				s[envKey(c.AccessToken)] = tokens.AccessToken
				s[envKey(c.RefreshToken)] = tokens.RefreshToken
				s[oauthStateKey(c.Key, "EXPIRES_AT")] = tokens.ExpiresAt
				s[oauthStateKey(c.Key, "SCOPES")] = strings.Join(tokens.Scopes, " ")
				s[pendingKey] = "true"
			}); err != nil {
				return refreshed, err
			}
		}
		if err := a.syncPackOAuth(ctx, orgID, packID, c.Key, true); err != nil {
			return refreshed, err
		}
		if _, err := p.updateSection(func(s map[string]string) { delete(s, pendingKey) }); err != nil {
			return refreshed, err
		}
		if !fresh {
			refreshed++
		}
	}
	return refreshed, nil
}
