package webfront

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

// syncPackOAuthSources is PackService.syncOAuthConnection(): a connected
// account writes the pack's GraphJin sources with its token; a disconnect
// blocks the sources that use that token.
func (a *API) syncPackOAuthSources(ctx context.Context, orgID, packID, connectionKey string, enabled bool) error {
	var row packInstallRow
	var raw []byte
	err := a.DB.QueryRow(ctx, `select version, status, source, config::text from pack_install
		where org_id = $1 and pack_id = $2 and status = 'installed' limit 1`, orgID, packID).
		Scan(&row.Version, &row.Status, &row.Source, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	parsed, err := jsParse(raw)
	if err != nil {
		return err
	}
	row.Config = jsObjOrEmpty(parsed)
	authored, err := installedPackBundle(orgID, packID, &row)
	if err != nil {
		return err
	}
	if authored.Manifest.Artifacts.GraphJin == nil {
		return nil
	}
	runtime := packRuntimeOf(row.Config)
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
	var connection *packs.OAuthConnection
	for i := range bundle.Manifest.OAuth {
		if bundle.Manifest.OAuth[i].Key == connectionKey {
			connection = &bundle.Manifest.OAuth[i]
		}
	}
	if connection == nil {
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
		if secretValues, _, _, err = a.resolvePackSecretState(bundle, &jsObj{vals: map[string]any{}}); err != nil {
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

	token := "{{secret." + connection.AccessToken + "}}"
	owned := map[string]bool{}
	connectedNames := map[string]bool{}
	var connectedTargets []string
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindSource {
			continue
		}
		if bindings[art.Key] != "" {
			continue
		}
		owned[art.TargetRef] = true
		record := art.Record()
		if auth, _ := record["auth"].(map[string]any); auth != nil && auth["token"] == token {
			connectedTargets = append(connectedTargets, art.TargetRef)
			connectedNames[packString(record["name"])] = true
		}
	}
	if !enabled && len(connectedTargets) == 0 {
		return fmt.Errorf("OAuth connection %s is not bound to an installed API source", connectionKey)
	}

	var update map[string]any
	in := packGraphjinUpdate{ownedSources: owned}
	if enabled {
		var api *packAPIArtifacts
		if graphjin.Mode == "api" {
			if api, err = graphjinAPIArtifacts(bundle, inputs); err != nil {
				return err
			}
			in.ownedSavedQueries = map[string]bool{}
			for _, q := range api.savedQueries {
				in.ownedSavedQueries[jsStr(jsObjOrEmpty(jsValueOf(q)).vals["name"])] = true
			}
		}
		if update, err = declarativeGraphjinUpdate(bundle, inputs, secretValues, nil, bindings, api, "authenticated"); err != nil {
			return err
		}
	} else {
		patches := []any{}
		for _, name := range connectedTargets {
			patches = append(patches, map[string]any{"name": name, "read_only": true,
				"access": map[string]any{"read": "blocked", "write": "blocked", "delete": "blocked"}})
		}
		update = map[string]any{"source_patches": patches}
	}
	in.update = jsObjOrEmpty(jsValueOf(update))
	if graphjin.Mode == "api" {
		_, err = a.applyPackGraphjinConfigLive(ctx, graphjin.Endpoint, orgID, in)
	} else {
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
		record := art.Record()
		adapter, _ := record["adapter"].(map[string]any)
		if adapter != nil && adapter["kind"] == "graphjin_api_operation" && connectedNames[packString(adapter["source"])] {
			if kind := packString(record["kind"]); !slices.Contains(kinds, kind) {
				kinds = append(kinds, kind)
			}
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
