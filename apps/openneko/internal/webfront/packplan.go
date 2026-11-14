package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

func graphjinGraphQLEndpoint(url string) string {
	clean := strings.TrimRight(url, "/")
	if strings.HasSuffix(clean, "/api/v1/graphql") {
		return clean
	}
	return clean + "/api/v1/graphql"
}

// packGraphjinTarget is GraphjinTarget in apps/worker/src/packs/graphjin-target.ts.
type packGraphjinTarget struct {
	packs.GraphjinTarget
	Endpoint           string
	Anonymous          bool
	KeystoreConfigured *bool
}

// packGraphjinQuery is graphjinQuery() with configurationOnly, as an
// admin of the org, with the data as plain JSON values.
func (a *API) packGraphjinQuery(ctx context.Context, endpoint, orgID, query string, variables map[string]any, timeout time.Duration) (any, []string, error) {
	token, err := a.mintGraphjinToken(graphjinClaims{orgID: orgID, userID: "pack-installer", role: "admin"}, 120*time.Second)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	vars := &jsObj{vals: map[string]any{}}
	for k, v := range variables {
		vars.set(k, v)
	}
	data, messages, err := gjPostVars(ctx, endpoint, token, query, vars)
	if err != nil {
		return nil, nil, err
	}
	var plain any
	if data != nil {
		if err := json.Unmarshal([]byte(jsStringify(data, false)), &plain); err != nil {
			return nil, nil, err
		}
	}
	return plain, messages, nil
}

// gjConfigRow is `Array.isArray(x) ? x[0] : x` on a gj_config or gj_catalog reply.
func gjConfigRow(data any, field string) map[string]any {
	m, _ := data.(map[string]any)
	switch v := m[field].(type) {
	case map[string]any:
		return v
	case []any:
		if len(v) > 0 {
			row, _ := v[0].(map[string]any)
			return row
		}
	}
	return nil
}

// resolvePackGraphjinTarget is resolveGraphjinTarget().
func (a *API) resolvePackGraphjinTarget(ctx context.Context, orgID, graphqlURL string) (*packGraphjinTarget, error) {
	endpoint := graphjinGraphQLEndpoint(graphqlURL)
	data, messages, err := a.packGraphjinQuery(ctx, endpoint, orgID, `query PackGraphjinTarget { gj_config(id: "current") { serv } }`, nil, 30*time.Second)
	if err != nil {
		return nil, err
	}
	row := gjConfigRow(data, "gj_config")
	if len(messages) > 0 || row == nil {
		cause := "gj_config is unavailable"
		if len(messages) > 0 {
			cause = strings.Join(messages, "; ")
		}
		return nil, fmt.Errorf("OpenNeko cannot read the GraphJin configuration at %s: %s. Allow config.read and gj_config access for OpenNeko on that GraphJin.", endpoint, cause)
	}
	serv, _ := row["serv"].(map[string]any)
	if text, ok := row["serv"].(string); ok {
		if err := json.Unmarshal([]byte(text), &serv); err != nil {
			return nil, err
		}
	}
	if serv["production"] != true {
		auth, _ := serv["auth"].(map[string]any)
		authType := auth["type"]
		if authType == nil {
			authType = auth["Type"]
		}
		kind := strings.ToLower(strings.TrimSpace(packString(authType)))
		t := &packGraphjinTarget{GraphjinTarget: packs.GraphjinTarget{Mode: "api"}, Endpoint: endpoint, Anonymous: kind == "" || kind == "none"}
		if ks, ok := serv["secrets_keystore_configured"].(bool); ok {
			t.KeystoreConfigured = &ks
		}
		t.Live = a.liveGraphjinConfigOnce(endpoint, orgID)
		t.SavedQuery = func(ctx context.Context, name string) (*string, error) {
			return a.readLiveSavedQuery(ctx, endpoint, orgID, name)
		}
		return t, nil
	}
	if file := strings.TrimSpace(os.Getenv("OPENNEKO_GRAPHJIN_CONFIG")); file != "" {
		if _, err := os.Stat(file); err == nil {
			return &packGraphjinTarget{GraphjinTarget: packs.GraphjinTarget{Mode: "files", ConfigFile: file}, Endpoint: endpoint}, nil
		}
	}
	return nil, fmt.Errorf("The GraphJin at %s runs in production mode, so it does not keep configuration changes made through its API. Set production: false on that GraphJin, or connect OpenNeko to the GraphJin whose config folder it manages.", endpoint)
}

// liveGraphjinConfigOnce is readLiveGraphjinConfig() with one read per target.
func (a *API) liveGraphjinConfigOnce(endpoint, orgID string) func(context.Context) (map[string]any, error) {
	var once sync.Once
	var out map[string]any
	var err error
	return func(ctx context.Context) (map[string]any, error) {
		once.Do(func() { out, err = a.readLiveGraphjinConfig(ctx, endpoint, orgID) })
		return out, err
	}
}

func (a *API) readLiveGraphjinConfig(ctx context.Context, endpoint, orgID string) (map[string]any, error) {
	query := fmt.Sprintf(`query PackLiveConfig_%d { gj_config(id: "current") { catalog_revision sources tables relationships } }`, time.Now().UnixMilli())
	data, messages, err := a.packGraphjinQuery(ctx, endpoint, orgID, query, nil, 60*time.Second)
	if err != nil {
		return nil, err
	}
	row := gjConfigRow(data, "gj_config")
	if len(messages) > 0 || row == nil {
		cause := "no gj_config row"
		if len(messages) > 0 {
			cause = strings.Join(messages, "; ")
		}
		return nil, fmt.Errorf("GraphJin configuration is unavailable: %s", cause)
	}
	revision, _ := row["catalog_revision"].(string)
	var rev any
	if revision != "" {
		rev = revision
	}
	return map[string]any{
		"catalogRevision": rev,
		"sources":         gjRows(row["sources"]),
		"tables":          gjRows(row["tables"]),
		"relationships":   gjRows(row["relationships"]),
	}, nil
}

func gjRows(v any) []any {
	if text, ok := v.(string); ok {
		var parsed any
		if json.Unmarshal([]byte(text), &parsed) != nil {
			return []any{}
		}
		return gjRows(parsed)
	}
	out := []any{}
	list, _ := v.([]any)
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (a *API) readLiveSavedQuery(ctx context.Context, endpoint, orgID, name string) (*string, error) {
	query := fmt.Sprintf(`query PackSavedQuery_%d($id: String!) { gj_catalog(id: $id) { id graphql_query } }`, time.Now().UnixMilli())
	data, _, err := a.packGraphjinQuery(ctx, endpoint, orgID, query, map[string]any{"id": "saved_query:" + name}, 30*time.Second)
	if err != nil {
		return nil, err
	}
	text, _ := gjConfigRow(data, "gj_catalog")["graphql_query"].(string)
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	return &text, nil
}

// resolveSelectedPackSource is resolvePackSource(): the stored data source,
// still enabled and unchanged.
func (a *API) resolveSelectedPackSource(ctx context.Context, orgID string, selected map[string]any) (*packSource, error) {
	id, _ := selected["id"].(string)
	var got packSource
	var url, mode *string
	err := a.DB.QueryRow(ctx, `select id::text, graphql_url, auth_mode from data_source where org_id = $1 and id::text = $2 and enabled`, orgID, id).
		Scan(&got.ID, &url, &mode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err != nil || url == nil || mode == nil || selected["graphqlUrl"] != *url || selected["authMode"] != *mode {
		return nil, errors.New("Pack data source changed or is unavailable; reconfigure and revalidate the pack")
	}
	got.GraphqlURL, got.AuthMode = *url, *mode
	return &got, nil
}

func (a *API) installedPackGraphjinTarget(ctx context.Context, orgID string, config map[string]any) *packGraphjinTarget {
	runtime, _ := config["_runtime"].(map[string]any)
	selection, _ := runtime["source"].(map[string]any)
	if selection == nil {
		return nil
	}
	source, err := a.resolveSelectedPackSource(ctx, orgID, selection)
	if err != nil {
		return nil
	}
	target, err := a.resolvePackGraphjinTarget(ctx, orgID, source.GraphqlURL)
	if err != nil {
		return nil
	}
	return target
}

var nativeStateQuery = map[packs.ArtifactKind]string{
	packs.KindMetric:   `select to_jsonb(t)::text from metric t where org_id = $1 and role = $2 and slug = $3 limit 1`,
	packs.KindWorkflow: `select to_jsonb(t)::text from workflow_definition t where org_id = $1 and owner_user_id = '' and name = $2 limit 1`,
	packs.KindWatcher:  `select to_jsonb(t)::text from watcher t where org_id = $1 and name = $2 limit 1`,
	packs.KindPolicy:   `select to_jsonb(t)::text from action_policy t where org_id = $1 and name = $2 limit 1`,
	packs.KindAction:   `select to_jsonb(t)::text from pack_action_definition t where org_id = $1 and kind = $2 limit 1`,
}

// nativePackRow is the row lookup of inspectNative().
func (a *API) nativePackRow(orgID string) packs.NativeLookup {
	return func(ctx context.Context, kind packs.ArtifactKind, locator map[string]any, targetRef string) (map[string]any, error) {
		args := []any{orgID}
		switch kind {
		case packs.KindMetric:
			args = append(args, packs.LocatorValue(locator, "role", ""), packs.LocatorValue(locator, "slug", targetRef))
		case packs.KindAction:
			args = append(args, packs.LocatorValue(locator, "kind", targetRef))
		default:
			args = append(args, packs.LocatorValue(locator, "name", targetRef))
		}
		var raw string
		err := a.DB.QueryRow(ctx, nativeStateQuery[kind], args...).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var row map[string]any
		return row, json.Unmarshal([]byte(raw), &row)
	}
}

var graphjinStateKinds = map[string]bool{"source": true, "relationships": true, "spec": true, "saved_query": true}

// packPlan is PackService.planBundle(): the bundle against what the org's
// install wrote and what the targets hold now.
func (a *API) packPlan(ctx context.Context, orgID string, bundle *packs.Bundle) (*packs.Plan, error) {
	var installID string
	var rawConfig []byte
	err := a.DB.QueryRow(ctx, `select id::text, config::text from pack_install where org_id = $1 and pack_id = $2 order by created_at desc limit 1`,
		orgID, bundle.Manifest.Metadata.ID).Scan(&installID, &rawConfig)
	if errors.Is(err, pgx.ErrNoRows) {
		plan := packs.PlanBundle(bundle, nil)
		return &plan, nil
	}
	if err != nil {
		return nil, err
	}
	type row struct {
		kind, key, target, desired, applied, ownership string
		metadata                                       map[string]any
	}
	rows, err := a.DB.Query(ctx, `select artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash, ownership, metadata::text
		from pack_artifact where pack_install_id = $1 order by created_at, id`, installID)
	if err != nil {
		return nil, err
	}
	var artifacts []row
	needsGraphjin := false
	for rows.Next() {
		var r row
		var meta string
		if err := rows.Scan(&r.kind, &r.key, &r.target, &r.desired, &r.applied, &r.ownership, &meta); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(meta), &r.metadata); err != nil {
			rows.Close()
			return nil, err
		}
		if r.metadata == nil {
			r.metadata = map[string]any{}
		}
		needsGraphjin = needsGraphjin || graphjinStateKinds[r.kind]
		artifacts = append(artifacts, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var graphjin *packs.GraphjinTarget
	if needsGraphjin {
		var config map[string]any
		_ = json.Unmarshal(rawConfig, &config)
		if t := a.installedPackGraphjinTarget(ctx, orgID, config); t != nil {
			graphjin = &t.GraphjinTarget
		}
	}
	desired := map[string]*packs.Artifact{}
	for i := range bundle.Artifacts {
		art := &bundle.Artifacts[i]
		desired[string(art.Kind)+":"+art.Key] = art
	}
	native := a.nativePackRow(orgID)
	installed := make([]packs.InstalledArtifact, 0, len(artifacts))
	for _, r := range artifacts {
		var current *string
		if graphjin != nil || !graphjinStateKinds[r.kind] {
			current, err = packs.InspectInstalled(ctx, native, packs.StateInput{
				Kind: packs.ArtifactKind(r.kind), TargetRef: r.target, Metadata: r.metadata,
				Graphjin: graphjin, Fallback: desired[r.kind+":"+r.key], LastApplied: r.applied,
			})
			if err != nil {
				return nil, err
			}
		}
		baseline := r.applied
		if version, _ := r.metadata["stateHashVersion"].(float64); version != 1 && current != nil {
			baseline = *current
		}
		installed = append(installed, packs.InstalledArtifact{
			Kind: packs.ArtifactKind(r.kind), Key: r.key, TargetRef: r.target,
			DesiredHash: r.desired, LastAppliedHash: baseline, CurrentHash: current, Ownership: r.ownership,
		})
	}
	plan := packs.PlanBundle(bundle, installed)
	return &plan, nil
}
