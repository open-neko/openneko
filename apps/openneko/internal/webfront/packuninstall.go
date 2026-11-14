package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

type stagedRemoval struct {
	restore, commit func() error
}

// stageOwnedRemoval moves a pack-owned path aside; commit deletes it.
func stageOwnedRemoval(path string) (stagedRemoval, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return stagedRemoval{restore: func() error { return nil }, commit: func() error { return nil }}, nil
	}
	if err != nil {
		return stagedRemoval{}, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return stagedRemoval{}, fmt.Errorf("pack-owned removal target is a symlink: %s", path)
	}
	backup := path + "." + uuid.NewString() + ".pack-remove-backup"
	if err := os.Rename(path, backup); err != nil {
		return stagedRemoval{}, err
	}
	return stagedRemoval{
		restore: func() error {
			if _, err := os.Lstat(backup); err != nil {
				return nil
			}
			return os.Rename(backup, path)
		},
		commit: func() error { return os.RemoveAll(backup) },
	}, nil
}

func assertOwnedPath(root, target, label string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s target escapes its managed root: %s", label, target)
	}
	return absTarget, nil
}

// replayPackOperation is replayIdempotentOperation().
func (a *API) replayPackOperation(ctx context.Context, orgID, packID, key string) (*jsObj, error) {
	if key == "" {
		return nil, nil
	}
	var priorPack, status string
	var message *string
	err := a.DB.QueryRow(ctx, `select i.pack_id, o.status, o.error from pack_operation o join pack_install i on i.id = o.pack_install_id
		where o.org_id = $1 and o.idempotency_key = $2 limit 1`, orgID, key).Scan(&priorPack, &status, &message)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if priorPack != packID {
		return nil, fmt.Errorf("idempotency key was already used for pack %s", priorPack)
	}
	switch status {
	case "succeeded":
		out, err := a.packStatus(ctx, orgID, packID)
		if err == nil && out == nil {
			err = fmt.Errorf("pack %s status is missing for completed operation", packID)
		}
		return out, err
	case "failed":
		if message != nil && *message != "" {
			return nil, errors.New(*message)
		}
		return nil, fmt.Errorf("the previous %s operation failed", packID)
	}
	return nil, fmt.Errorf("the %s operation for this idempotency key is still %s", packID, status)
}

type packInstallState struct {
	ID, Version, Status, Source string
	OperationID                 *string
	Config                      *jsObj
}

func (a *API) packInstallState(ctx context.Context, orgID, packID string) (*packInstallState, error) {
	var row packInstallState
	var raw []byte
	err := a.DB.QueryRow(ctx, `select id::text, version, status, source, operation_id::text, config::text from pack_install
		where org_id = $1 and pack_id = $2 order by created_at desc limit 1`, orgID, packID).
		Scan(&row.ID, &row.Version, &row.Status, &row.Source, &row.OperationID, &raw)
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

type packArtifactRow struct {
	ID, Kind, Key, TargetRef string
	Metadata                 *jsObj
}

func (r packArtifactRow) identity() string { return r.Kind + ":" + r.Key }

func (r packArtifactRow) materializedTarget() (string, bool) {
	s, ok := r.Metadata.vals["materializedTarget"].(string)
	return s, ok
}

func (a *API) packArtifactRows(ctx context.Context, installID string) ([]packArtifactRow, error) {
	rows, err := a.DB.Query(ctx, `select id::text, artifact_kind, artifact_key, target_ref, metadata::text from pack_artifact where pack_install_id = $1`, installID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (packArtifactRow, error) {
		var r packArtifactRow
		var raw []byte
		if err := row.Scan(&r.ID, &r.Kind, &r.Key, &r.TargetRef, &raw); err != nil {
			return r, err
		}
		parsed, err := jsParse(raw)
		r.Metadata = jsObjOrEmpty(parsed)
		return r, err
	})
}

func (a *API) servePackUninstall(w http.ResponseWriter, r *http.Request, act actor, packID string, body *jsObj) {
	key, _ := body.vals["idempotencyKey"].(string)
	out, err := a.packUninstall(context.WithoutCancel(r.Context()), act.OrgID, packID, nullable(act.UserID), key)
	if err != nil {
		sendPackFailure(w, http.StatusBadRequest, err, "uninstall")
		return
	}
	sendJSON(w, http.StatusOK, out)
}

func packRuntimeOf(config *jsObj) map[string]any {
	runtime, _ := plainJSON(config.vals["_runtime"]).(map[string]any)
	return runtime
}

// packUninstall is PackService.uninstall(): GraphJin sources are revoked but
// kept, owned files are removed, native rows are switched off and the
// receipts are retired.
func (a *API) packUninstall(ctx context.Context, orgID, packID string, actorUserID any, key string) (*jsObj, error) {
	if replay, err := a.replayPackOperation(ctx, orgID, packID, key); replay != nil || err != nil {
		return replay, err
	}
	installation, err := a.packInstallState(ctx, orgID, packID)
	if err != nil {
		return nil, err
	}
	if installation == nil {
		return nil, fmt.Errorf("pack %s is not installed", packID)
	}
	if installation.Status == "removed" {
		return a.removedPackStatus(ctx, orgID, packID)
	}
	bundle, err := installedPackBundle(orgID, packID, &packInstallRow{Version: installation.Version, Status: installation.Status, Source: installation.Source, Config: installation.Config})
	if err != nil {
		return nil, err
	}
	usesGraphjin := bundle.Manifest.Artifacts.GraphJin != nil || packRuntimeOf(installation.Config)["source"] != nil

	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	lockKey := "pack:" + orgID + ":" + packID
	if _, err := conn.Exec(ctx, `select pg_advisory_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return nil, err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `select pg_advisory_unlock(hashtextextended($1, 0))`, lockKey)

	if replay, err := a.replayPackOperation(ctx, orgID, packID, key); replay != nil || err != nil {
		return replay, err
	}
	u := &packUninstallRun{api: a, orgID: orgID, packID: packID, installation: installation}
	out, err := u.run(ctx, bundle, usesGraphjin, actorUserID, key)
	if err != nil {
		u.fail(ctx, err)
		return nil, err
	}
	return out, nil
}

func (a *API) removedPackStatus(ctx context.Context, orgID, packID string) (*jsObj, error) {
	out, err := a.packStatus(ctx, orgID, packID)
	if err == nil && out == nil {
		err = fmt.Errorf("pack %s removal status is missing", packID)
	}
	return out, err
}

type packUninstallRun struct {
	api           *API
	orgID, packID string
	installation  *packInstallState
	operationID   string
	graphjin      func(context.Context) error
	secrets       func() error
	removals      []stagedRemoval
}

func (u *packUninstallRun) run(ctx context.Context, bundle *packs.Bundle, usesGraphjin bool, actorUserID any, key string) (*jsObj, error) {
	a, orgID, packID := u.api, u.orgID, u.packID
	installation, err := a.packInstallState(ctx, orgID, packID)
	if err != nil {
		return nil, err
	}
	if installation == nil {
		return nil, fmt.Errorf("pack %s is not installed", packID)
	}
	u.installation = installation
	if installation.Status == "removed" {
		return a.removedPackStatus(ctx, orgID, packID)
	}
	if slices.Contains([]string{"installing", "upgrading", "removing"}, installation.Status) {
		return nil, fmt.Errorf("pack %s already has an operation in progress", packID)
	}
	usesGraphjin = usesGraphjin && !jsTruthy(installation.Config.vals["_definitionOnly"])
	plan, err := a.packPlan(ctx, orgID, bundle)
	if err != nil {
		return nil, err
	}
	conflicts := map[string]bool{}
	for _, entry := range plan.Entries {
		if entry.Action == "conflict" {
			conflicts[string(entry.Kind)+":"+entry.Key] = true
		}
	}
	if len(conflicts) > 0 {
		if _, err := a.DB.Exec(ctx, `update pack_artifact set ownership = 'modified', updated_at = now()
			where pack_install_id = $1 and artifact_kind || ':' || artifact_key = any($2)`, installation.ID, slices.Collect(maps.Keys(conflicts))); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("pack uninstall has %d drift conflict(s); modified artifacts were preserved", len(conflicts))
	}
	if _, err := a.DB.Exec(ctx, `update pack_install set status = 'removing', last_error = null, updated_at = now() where id = $1`, installation.ID); err != nil {
		return nil, err
	}
	if key == "" {
		key = uuid.NewString()
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if err := a.DB.QueryRow(ctx, `insert into pack_operation (pack_install_id, org_id, operation_type, actor_user_id, status,
		requested_version, idempotency_key, plan, plan_hash, phase, started_at)
		values ($1, $2, 'uninstall', $3, 'running', $4, $5, $6::jsonb, $7, 'removing_graphjin', now()) returning id::text`,
		installation.ID, orgID, actorUserID, installation.Version, key, string(planJSON), plan.Hash).Scan(&u.operationID); err != nil {
		return nil, err
	}
	if _, err := a.DB.Exec(ctx, `update pack_install set operation_id = $1 where id = $2`, u.operationID, installation.ID); err != nil {
		return nil, err
	}

	runtime := packRuntimeOf(installation.Config)
	tables, _ := runtime["tables"].([]any)
	if err := verifyPackQueryTables(tables); err != nil {
		return nil, err
	}
	graphqlURL := ""
	if selection, ok := runtime["source"].(map[string]any); ok {
		source, err := a.resolveSelectedPackSource(ctx, orgID, selection)
		if err != nil {
			return nil, err
		}
		graphqlURL = source.GraphqlURL
	} else if usesGraphjin {
		err := a.DB.QueryRow(ctx, `select coalesce(graphql_url, '') from data_source where org_id = $1 and enabled
			order by is_default desc, created_at limit 1`, orgID).Scan(&graphqlURL)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if usesGraphjin && graphqlURL == "" {
		return nil, errors.New("The GraphJin data connection for this pack is unavailable")
	}
	var graphjin *packGraphjinTarget
	if usesGraphjin {
		if graphjin, err = a.resolvePackGraphjinTarget(ctx, orgID, graphqlURL); err != nil {
			return nil, err
		}
	}
	configFile := ""
	if graphjin != nil && graphjin.Mode == "files" {
		configFile = graphjin.ConfigFile
	}
	provenance, err := a.packArtifactRows(ctx, installation.ID)
	if err != nil {
		return nil, err
	}
	desired := map[string]*packs.Artifact{}
	for i := range bundle.Artifacts {
		artifact := &bundle.Artifacts[i]
		desired[string(artifact.Kind)+":"+artifact.Key] = artifact
	}

	sourceNames := map[string]bool{}
	revoked, removedQueries := []any{}, []any{}
	for _, artifact := range provenance {
		if artifact.Kind == "source" && !jsTruthy(artifact.Metadata.vals["borrowedSource"]) {
			sourceNames[artifact.TargetRef] = true
			revoked = append(revoked, jsObjOf("name", artifact.TargetRef, "read_only", true,
				"access", jsObjOf("read", "blocked", "write", "blocked", "delete", "blocked")))
		}
		if target, ok := artifact.materializedTarget(); ok && strings.HasPrefix(target, "graphjin:saved_query:") {
			removedQueries = append(removedQueries, strings.TrimPrefix(target, "graphjin:saved_query:"))
		}
	}
	switch {
	case graphjin != nil && graphjin.Mode == "api":
		update := jsObjOf("source_patches", revoked)
		if len(removedQueries) > 0 {
			update.set("remove_saved_queries", removedQueries)
		}
		applied, err := a.applyPackGraphjinConfigLive(ctx, graphjin.Endpoint, orgID, packGraphjinUpdate{update: update, ownedSources: sourceNames})
		if err != nil {
			return nil, err
		}
		u.graphjin = applied.restore
	case usesGraphjin:
		applied, err := a.applyPackGraphjinConfig(ctx, graphjinGraphQLEndpoint(graphqlURL), orgID, configFile,
			packGraphjinUpdate{update: jsObjOf("source_patches", revoked), ownedSources: sourceNames}, true)
		if err != nil {
			return nil, err
		}
		u.graphjin = applied.restore
	}

	configRoot := filepath.Dir(configFile)
	workspace, err := orgWorkspace(orgID)
	if err != nil {
		return nil, err
	}
	for _, artifact := range provenance {
		target, ok := artifact.materializedTarget()
		if !ok || strings.HasPrefix(target, "graphjin:") {
			continue
		}
		root, label := "", ""
		switch artifact.Kind {
		case "spec", "saved_query":
			root, label = configRoot, "GraphJin pack file"
		case "skill":
			root, label = workspace.Skills, "pack skill"
		default:
			continue
		}
		path, err := assertOwnedPath(root, target, label)
		if err != nil {
			return nil, err
		}
		removal, err := stageOwnedRemoval(path)
		if err != nil {
			return nil, err
		}
		u.removals = append(u.removals, removal)
	}

	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return nil, err
	}
	next := maps.Clone(store)
	delete(next, "pack."+packID)
	if err := secrets.Write(next, a.SecretsDir); err != nil {
		return nil, err
	}
	u.secrets = func() error { return secrets.Write(store, a.SecretsDir) }

	retired := map[string]string{}
	var target *packs.GraphjinTarget
	if graphjin != nil {
		target = &graphjin.GraphjinTarget
	}
	for _, artifact := range provenance {
		materialized, _ := artifact.materializedTarget()
		if artifact.Kind != "source" && artifact.Kind != "relationships" && (artifact.Kind != "spec" || !strings.HasPrefix(materialized, "graphjin:spec:")) {
			continue
		}
		metadata, _ := plainJSON(artifact.Metadata).(map[string]any)
		current, err := packs.InspectInstalled(ctx, a.nativePackRow(orgID), packs.StateInput{Kind: packs.ArtifactKind(artifact.Kind),
			TargetRef: artifact.TargetRef, Metadata: metadata, Graphjin: target, Fallback: desired[artifact.identity()]})
		if err != nil {
			return nil, err
		}
		if current == nil {
			return nil, fmt.Errorf("pack GraphJin artifact %s disappeared during uninstall", artifact.identity())
		}
		retired[artifact.identity()] = *current
	}
	if err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		return u.retire(ctx, tx, provenance, desired, retired)
	}); err != nil {
		return nil, err
	}
	for _, removal := range u.removals {
		if err := removal.commit(); err != nil {
			log.Printf("[packs] uninstall cleanup warning: %v", err)
		}
	}
	out, err := a.packStatus(ctx, orgID, packID)
	if err == nil && out == nil {
		err = errors.New("pack status missing after uninstall")
	}
	return out, err
}

var retireNative = map[packs.ArtifactKind]string{
	packs.KindMetric: `update metric t set active = false, updated_at = now() from (select id from metric where org_id = $1 and role = $2 and slug = $3 limit 1) m
		where t.id = m.id returning to_jsonb(t)::text`,
	packs.KindWatcher: `update watcher t set enabled = false, updated_at = now() from (select id from watcher where org_id = $1 and name = $2 limit 1) m
		where t.id = m.id returning to_jsonb(t)::text`,
	packs.KindWorkflow: `update workflow_definition t set enabled = false, cron_enabled = false, updated_at = now()
		from (select id from workflow_definition where org_id = $1 and owner_user_id = '' and name = $2 limit 1) m
		where t.id = m.id returning to_jsonb(t)::text`,
	packs.KindPolicy: `update action_policy t set enabled = false, updated_at = now() from (select id from action_policy where org_id = $1 and name = $2 limit 1) m
		where t.id = m.id returning to_jsonb(t)::text`,
	packs.KindAction: `update pack_action_definition t set enabled = false, readiness = 'blocked', readiness_reason = 'pack_uninstalled', updated_at = now()
		from (select id from pack_action_definition where org_id = $1 and kind = $2 limit 1) m
		where t.id = m.id returning to_jsonb(t)::text`,
}

func (u *packUninstallRun) retire(ctx context.Context, tx pgx.Tx, provenance []packArtifactRow, desired map[string]*packs.Artifact, retired map[string]string) error {
	for _, artifact := range provenance {
		identity := artifact.identity()
		kind := packs.ArtifactKind(artifact.Kind)
		if query, native := retireNative[kind]; native {
			metadata, _ := plainJSON(artifact.Metadata).(map[string]any)
			locator := packs.LocatorFromMetadata(metadata, desired[identity])
			args := []any{u.orgID}
			switch kind {
			case packs.KindMetric:
				role := packs.LocatorValue(locator, "role", "")
				if role == "" {
					return fmt.Errorf("pack metric %s has no uninstall locator", artifact.Key)
				}
				args = append(args, role, packs.LocatorValue(locator, "slug", artifact.TargetRef))
			case packs.KindAction:
				args = append(args, packs.LocatorValue(locator, "kind", artifact.TargetRef))
			default:
				args = append(args, packs.LocatorValue(locator, "name", artifact.TargetRef))
			}
			var raw string
			if err := tx.QueryRow(ctx, query, args...).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("pack %s %s disappeared during uninstall", artifact.Kind, artifact.TargetRef)
			} else if err != nil {
				return err
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(raw), &row); err != nil {
				return err
			}
			hash, err := packs.NativeStateHash(kind, row)
			if err != nil {
				return err
			}
			retired[identity] = hash
		}
		hash, has := retired[identity]
		if !has {
			var err error
			if hash, err = packs.CanonicalHash(map[string]any{"retired": true, "identity": identity}); err != nil {
				return err
			}
		}
		metadata := jsSpread(artifact.Metadata)
		metadata.set("stateHashVersion", float64(1))
		metadata.set("retiredMissing", !has)
		if kind == packs.KindSource || kind == packs.KindRelationships {
			metadata.set("retainedForGraphjinCompatibility", true)
		}
		if _, err := tx.Exec(ctx, `update pack_artifact set last_applied_hash = $1, ownership = 'retired', readiness = 'not_applicable',
			readiness_reason = null, metadata = $2::jsonb, updated_at = now() where id = $3`, hash, jsStringify(metadata, false), artifact.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `delete from pack_user_connection where org_id = $1 and pack_install_id = $2`, u.orgID, u.installation.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update pack_install set status = 'removed', removed_at = now(), updated_at = now() where id = $1`, u.installation.ID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `update pack_operation set status = 'succeeded', phase = 'complete', compensation_status = 'not_required',
		completed_at = now(), updated_at = now() where id = $1`, u.operationID)
	return err
}

// fail undoes what the run changed, in reverse, and records the failure.
func (u *packUninstallRun) fail(ctx context.Context, cause error) {
	ctx = context.WithoutCancel(ctx)
	compensationFailed := false
	if u.secrets != nil && u.secrets() != nil {
		compensationFailed = true
	}
	for i := len(u.removals) - 1; i >= 0; i-- {
		if u.removals[i].restore() != nil {
			compensationFailed = true
		}
	}
	if u.graphjin != nil && u.graphjin(ctx) != nil {
		compensationFailed = true
	}
	message := cutUTF16(cause.Error(), 1000)
	compensation := "succeeded"
	if compensationFailed {
		compensation = "failed"
	}
	if u.operationID != "" {
		_, _ = u.api.DB.Exec(ctx, `update pack_operation set status = 'failed', failure_phase = 'uninstall', error = $1,
			compensation_status = $2, completed_at = now(), updated_at = now() where id = $3`, message, compensation, u.operationID)
	}
	if u.installation == nil {
		return
	}
	status := u.installation.Status
	if compensationFailed {
		status = "failed"
	}
	_, _ = u.api.DB.Exec(ctx, `update pack_install set status = $1, operation_id = $2::uuid, last_error = $3, updated_at = now() where id = $4`,
		status, u.installation.OperationID, message, u.installation.ID)
}
