package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/packs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

type priorPackInstall struct {
	ID, Status, Version, Source, ManifestHash string
	OperationID                               *string
	RawConfig                                 []byte
	Config                                    *jsObj
	InstalledAt, RemovedAt                    *time.Time
}

func (a *API) priorPackInstall(ctx context.Context, orgID, packID string) (*priorPackInstall, error) {
	var row priorPackInstall
	err := a.DB.QueryRow(ctx, `select id::text, status, version, source, manifest_hash, operation_id::text, config::text, installed_at, removed_at
		from pack_install where org_id = $1 and pack_id = $2 order by created_at desc limit 1`, orgID, packID).
		Scan(&row.ID, &row.Status, &row.Version, &row.Source, &row.ManifestHash, &row.OperationID, &row.RawConfig, &row.InstalledAt, &row.RemovedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsed, err := jsParse(row.RawConfig)
	row.Config = jsObjOrEmpty(parsed)
	return &row, err
}

// mergePackTables keeps the pack's earlier table aliases and adds the bound ones.
func mergePackTables(prior *jsObj, bound []map[string]string) []any {
	tables := []any{}
	var names []string
	merge := func(t any) {
		name := jsValueString(jsObjOrEmpty(t).vals["name"])
		if i := slices.Index(names, name); i >= 0 {
			tables[i] = t
			return
		}
		names, tables = append(names, name), append(tables, t)
	}
	if prior != nil {
		old, _ := prior.vals["tables"].([]any)
		for _, t := range old {
			merge(t)
		}
	}
	for _, t := range bound {
		merge(jsObjOf("name", t["name"], "table", t["table"], "source", t["source"]))
	}
	return tables
}

func packJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(raw)
}

func (a *API) servePackApply(w http.ResponseWriter, r *http.Request, act actor, packID, action string, body *jsObj) {
	out, err := a.packApply(context.WithoutCancel(r.Context()), act.OrgID, packID, action, body)
	if err != nil {
		sendPackFailure(w, http.StatusBadRequest, err, action)
		return
	}
	sendJSON(w, http.StatusOK, out)
}

// packApply is PackService.install(), configure() and upgrade().
func (a *API) packApply(ctx context.Context, orgID, packID, operation string, request *jsObj) (*jsObj, error) {
	if operation != "install" {
		current, err := a.packStatus(ctx, orgID, packID)
		if err != nil {
			return nil, err
		}
		if current == nil || current.vals["status"] != "installed" {
			return nil, fmt.Errorf("pack %s must be installed before it can be %sd", packID, operation)
		}
	}
	key, _ := request.vals["idempotencyKey"].(string)
	if replay, err := a.replayPackOperation(ctx, orgID, packID, key); replay != nil || err != nil {
		return replay, err
	}
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
	run := &packApplyRun{api: a, orgID: orgID, packID: packID, operation: operation, request: request, key: key}
	defer func() {
		if run.cleanup != nil {
			run.cleanup()
		}
	}()
	out, err := run.run(ctx)
	if err != nil {
		run.fail(ctx, err)
		return nil, err
	}
	return out, nil
}

type packApplyRun struct {
	api                       *API
	orgID, packID, operation  string
	request                   *jsObj
	key                       string
	prior                     *priorPackInstall
	installID, operationID    string
	cleanup                   func()
	skills, files, retirement *packMaterialized
	graphjin                  func(context.Context) error
	secrets                   func() error
}

func (p *packApplyRun) actor() any { return p.request.vals["actorUserId"] }

func (p *packApplyRun) idempotencyKey() string {
	if p.key == "" {
		p.key = uuid.NewString()
	}
	return p.key
}

func (p *packApplyRun) loadBundle() (*packs.Bundle, error) {
	version := ""
	if packTruthy(p.request.vals["version"]) {
		version = packString(p.request.vals["version"])
	}
	if p.operation == "configure" && version != "" && version != p.prior.Version {
		return nil, errors.New("configure cannot change the installed pack version; review an upgrade instead")
	}
	var bundle *packs.Bundle
	var err error
	if p.operation == "configure" && p.prior != nil {
		bundle, err = installedPackBundle(p.orgID, p.packID, &packInstallRow{Version: p.prior.Version, Status: p.prior.Status, Source: p.prior.Source, Config: p.prior.Config})
	} else {
		bundle, err = packBundle(p.orgID, p.packID, version)
	}
	if err != nil || bundle.Upload == nil {
		return bundle, err
	}
	snapshot, cleanup, err := snapshotUploadedPack(packUploadsRoot(p.orgID), bundle)
	if err != nil {
		return nil, err
	}
	p.cleanup = cleanup
	return snapshot, nil
}

func userOAuth(bundle *packs.Bundle) []any {
	out := []any{}
	list, _ := bundle.Manifest.Normalized()["oauth"].([]any)
	for _, c := range list {
		if m, ok := c.(map[string]any); ok && m["scope"] == "user" {
			out = append(out, m)
		}
	}
	return out
}

func (p *packApplyRun) installDefinition(ctx context.Context, bundle *packs.Bundle) (*jsObj, error) {
	a := p.api
	if p.operation != "install" || bundle.Upload != nil {
		return nil, errors.New("Only built-in packs support installation before configuration")
	}
	if v := p.request.vals["version"]; packTruthy(v) && packString(v) != bundle.Manifest.Metadata.Version {
		return nil, errors.New("The built-in pack version changed; reload the pack list")
	}
	if p.prior != nil && p.prior.Status == "installed" {
		return a.packStatus(ctx, p.orgID, p.packID)
	}
	if p.prior != nil && p.prior.Status != "removed" {
		return nil, errors.New("Resolve the previous pack operation before installing again")
	}
	config := packJSON(map[string]any{"_definitionOnly": true, "_userOAuth": userOAuth(bundle)})
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `insert into pack_install (org_id, pack_id, version, manifest_hash, source, status, installed_by_user_id, installed_at, config)
			values ($1, $2, $3, $4, 'embedded', 'installed', $5, now(), $6::jsonb) returning id::text`,
			p.orgID, p.packID, bundle.Manifest.Metadata.Version, bundle.ManifestHash, p.actor(), config).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into pack_operation (org_id, pack_install_id, operation_type, actor_user_id, status, phase,
			requested_version, idempotency_key, plan_hash, completed_at)
			values ($1, $2, 'install', $3, 'succeeded', 'definition_installed', $4, $5, $6, now())`,
			p.orgID, id, p.actor(), bundle.Manifest.Metadata.Version, p.idempotencyKey(), bundle.ManifestHash)
		return err
	})
	if err != nil {
		return nil, err
	}
	return a.packStatus(ctx, p.orgID, p.packID)
}

func (p *packApplyRun) run(ctx context.Context) (*jsObj, error) {
	a, orgID, packID := p.api, p.orgID, p.packID
	prior, err := a.priorPackInstall(ctx, orgID, packID)
	if err != nil {
		return nil, err
	}
	p.prior = prior
	if p.operation != "install" && (prior == nil || prior.Status != "installed") {
		return nil, fmt.Errorf("pack %s must be installed before it can be %sd", packID, p.operation)
	}
	if prior != nil && slices.Contains([]string{"installing", "upgrading", "removing"}, prior.Status) {
		return nil, fmt.Errorf("pack %s already has an operation in progress", packID)
	}
	authored, err := p.loadBundle()
	if err != nil {
		return nil, err
	}
	if packTruthy(p.request.vals["deferConfiguration"]) {
		return p.installDefinition(ctx, authored)
	}
	if packID == "magento" {
		return nil, errors.New("the Magento pack applies through the worker")
	}

	var priorRuntime *jsObj
	priorConfig := &jsObj{vals: map[string]any{}}
	if prior != nil {
		priorConfig = prior.Config
		priorRuntime, _ = prior.Config.vals["_runtime"].(*jsObj)
	}
	runtime, err := a.packRuntime(ctx, orgID, authored, p.request, priorRuntime)
	if err != nil {
		return nil, err
	}
	bindings := map[string]string{}
	for k, v := range jsObjOrEmpty(runtime.vals["bindings"]).vals {
		bindings[k], _ = v.(string)
	}
	boundTables, boundQueries, err := bindPackQueries(authored, bindings)
	if err != nil {
		return nil, err
	}
	bundle := boundPackBundle(authored, boundQueries)
	runtime.set("tables", mergePackTables(priorRuntime, boundTables))

	priorInputs := &jsObj{vals: map[string]any{}}
	for _, k := range priorConfig.ordered() {
		if bundle.Manifest.InputDeclared(k) {
			priorInputs.set(k, priorConfig.vals[k])
		}
	}
	supplied := jsSpread(priorInputs)
	given := jsObjOrEmpty(p.request.vals["inputs"])
	for _, k := range given.ordered() {
		supplied.set(k, given.vals[k])
	}
	inputs, err := resolvePackInputs(bundle, supplied)
	if err != nil {
		return nil, err
	}
	plainInputs, _ := plainJSON(inputs).(map[string]any)
	secretValues, cleared, store, err := a.resolvePackSecretState(bundle, p.request)
	if err != nil {
		return nil, err
	}
	plan, err := a.packPlan(ctx, orgID, authored)
	if err != nil {
		return nil, err
	}
	reviewHash, err := a.packReviewHash(orgID, authored, plan, p.operation, p.actor(), p.request, inputs, secretValues, runtime)
	if err != nil {
		return nil, err
	}
	if given := p.request.vals["reviewHash"]; (authored.Upload != nil || packTruthy(given)) && given != reviewHash {
		return nil, errors.New("pack review is missing or stale; review the exact bundle, configuration, and plan before installing")
	}
	if err := declarativePackChecks(bundle, plainInputs, secretValues, bindings); err != nil {
		return nil, err
	}
	conflicts := 0
	allNoop := true
	for _, e := range plan.Entries {
		if e.Action == "conflict" {
			conflicts++
		}
		allNoop = allNoop && e.Action == "noop"
	}
	if conflicts > 0 {
		return nil, fmt.Errorf("pack install has %d drift conflict(s); run pack plan for details", conflicts)
	}
	section := "pack." + packID
	stored := store[section]
	secretsChanged := false
	for k, v := range secretValues {
		if current, ok := stored[secretEnvKey(k)]; !ok || current != v {
			secretsChanged = true
		}
	}
	for k := range cleared {
		if _, ok := stored[secretEnvKey(k)]; ok {
			secretsChanged = true
		}
	}
	configChanged := true
	if prior != nil {
		normalized, err := resolvePackInputs(bundle, priorInputs)
		if err != nil {
			return nil, err
		}
		h1, err1 := packs.CanonicalHash(plainJSON(normalized))
		h2, err2 := packs.CanonicalHash(plainJSON(inputs))
		h3, err3 := packs.CanonicalHash(plainJSON(priorRuntime))
		h4, err4 := packs.CanonicalHash(plainJSON(runtime))
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			return nil, err
		}
		configChanged = h1 != h2 || h3 != h4
	}
	if prior != nil && prior.Status == "installed" && prior.Version == bundle.Manifest.Metadata.Version &&
		prior.ManifestHash == bundle.ManifestHash && allNoop && !configChanged && !secretsChanged {
		if err := a.persistLegacyStateBaselines(ctx, prior.ID, plan, bundle); err != nil {
			return nil, err
		}
		out, err := a.packStatus(ctx, orgID, packID)
		if err == nil && out == nil {
			err = fmt.Errorf("pack %s status is missing after no-op plan", packID)
		}
		return out, err
	}

	var priorArtifacts []packArtifactRow
	if prior != nil {
		if priorArtifacts, err = a.packArtifactRows(ctx, prior.ID); err != nil {
			return nil, err
		}
	}
	retire := map[string]bool{}
	for _, e := range plan.Entries {
		if e.Action == "retire" {
			retire[string(e.Kind)+":"+e.Key] = true
		}
	}
	var retired []packArtifactRow
	for _, row := range priorArtifacts {
		if retire[row.identity()] {
			retired = append(retired, row)
		}
	}
	if err := a.assertNativeTargetsAvailable(ctx, orgID, bundle, plan); err != nil {
		return nil, err
	}
	ownedFiles, ownedSkills, ownedSources := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, row := range priorArtifacts {
		if target, ok := row.materializedTarget(); ok {
			if row.Kind == "skill" {
				ownedSkills[target] = true
			} else {
				ownedFiles[target] = true
			}
		}
		if row.Kind == "source" {
			ownedSources[row.TargetRef] = true
		}
	}

	sourceName := "embedded"
	config := maps.Clone(plainInputs)
	config["_runtime"] = plainJSON(runtime)
	if authored.Upload != nil {
		sourceName = "uploaded"
		config["_bundle"] = plainJSON(jsValueOf(authored.Upload))
	}
	if prior != nil {
		p.installID = prior.ID
		status := "installing"
		if p.operation == "upgrade" {
			status = "upgrading"
		}
		if _, err := a.DB.Exec(ctx, `update pack_install set version = $1, status = $2, manifest_hash = $3, source = $4, config = $5::jsonb,
			last_error = null, removed_at = null, updated_at = now() where id = $6`,
			bundle.Manifest.Metadata.Version, status, bundle.ManifestHash, sourceName, packJSON(config), prior.ID); err != nil {
			return nil, err
		}
	} else if err := a.DB.QueryRow(ctx, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, source, config, installed_by_user_id)
		values ($1, $2, $3, 'installing', $4, $5, $6::jsonb, $7) returning id::text`,
		orgID, packID, bundle.Manifest.Metadata.Version, bundle.ManifestHash, sourceName, packJSON(config), p.actor()).Scan(&p.installID); err != nil {
		return nil, err
	}
	operationPlan, _ := plainJSON(jsValueOf(plan)).(map[string]any)
	operationPlan["inputs"], operationPlan["runtime"], operationPlan["reviewHash"] = plainJSON(inputs), plainJSON(runtime), reviewHash
	planHash, err := packs.CanonicalHash(operationPlan)
	if err != nil {
		return nil, err
	}
	if err := a.DB.QueryRow(ctx, `insert into pack_operation (pack_install_id, org_id, operation_type, actor_user_id, status, requested_version,
		idempotency_key, plan, plan_hash, phase, started_at)
		values ($1, $2, $3, $4, 'running', $5, $6, $7::jsonb, $8, 'preflight_complete', now()) returning id::text`,
		p.installID, orgID, p.operation, p.actor(), bundle.Manifest.Metadata.Version, p.idempotencyKey(), packJSON(operationPlan), planHash).
		Scan(&p.operationID); err != nil {
		return nil, err
	}
	if _, err := a.DB.Exec(ctx, `update pack_install set operation_id = $1 where id = $2`, p.operationID, p.installID); err != nil {
		return nil, err
	}

	nextSection := maps.Clone(stored)
	if nextSection == nil {
		nextSection = map[string]string{}
	}
	for k := range cleared {
		delete(nextSection, secretEnvKey(k))
	}
	for k, v := range secretValues {
		nextSection[secretEnvKey(k)] = v
	}
	nextStore := maps.Clone(store)
	if nextStore == nil {
		nextStore = secrets.Store{}
	}
	nextStore[section] = nextSection
	if err := secrets.Write(nextStore, a.SecretsDir); err != nil {
		return nil, err
	}
	p.secrets = func() error { return secrets.Write(store, a.SecretsDir) }

	usesGraphjin := bundle.Manifest.Artifacts.GraphJin != nil
	selection, _ := runtime.vals["source"].(*jsObj)
	graphqlURL := ""
	if selection != nil {
		graphqlURL = jsStr(selection.vals["graphqlUrl"])
	}
	if usesGraphjin && graphqlURL == "" {
		return nil, errors.New("Choose a GraphJin data connection before installing this pack")
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

	var retiredQueries []any
	var removals []func() error
	var commits []func() error
	stage := func(root, target, label string) error {
		owned, err := assertOwnedPath(root, target, label)
		if err != nil {
			return err
		}
		removal, err := stageOwnedRemoval(owned)
		if err != nil {
			return err
		}
		removals, commits = append(removals, removal.restore), append(commits, removal.commit)
		return nil
	}
	var workspaceSkills string
	for _, row := range retired {
		if row.Kind == "skill" {
			workspace, err := orgWorkspace(orgID)
			if err != nil {
				return nil, err
			}
			workspaceSkills = workspace.Skills
			break
		}
	}
	for _, row := range retired {
		target, ok := row.materializedTarget()
		if !ok {
			continue
		}
		var err error
		switch {
		case strings.HasPrefix(target, "graphjin:saved_query:"):
			retiredQueries = append(retiredQueries, strings.TrimPrefix(target, "graphjin:saved_query:"))
		case strings.HasPrefix(target, "graphjin:"):
		case row.Kind == "spec" || row.Kind == "saved_query":
			err = stage(filepath.Dir(configFile), target, "GraphJin pack file")
		case row.Kind == "skill" && workspaceSkills != "":
			err = stage(workspaceSkills, target, "pack skill")
		}
		if err != nil {
			_ = undoAll(removals)
			return nil, err
		}
	}
	p.retirement = &packMaterialized{restore: func() error { return undoAll(removals) }, commit: func() error {
		var first error
		for _, c := range commits {
			if err := c(); err != nil && first == nil {
				first = err
			}
		}
		return first
	}}
	desiredSources := map[string]bool{}
	for _, art := range bundle.Artifacts {
		if art.Kind == packs.KindSource {
			desiredSources[art.TargetRef] = true
		}
	}
	var retiredSources []string
	for _, row := range retired {
		if row.Kind == "source" && !jsTruthy(row.Metadata.vals["borrowedSource"]) && !desiredSources[row.TargetRef] {
			retiredSources = append(retiredSources, row.TargetRef)
		}
	}
	if _, err := a.DB.Exec(ctx, `update pack_operation set phase = 'graphjin_files', updated_at = now() where id = $1`, p.operationID); err != nil {
		return nil, err
	}
	var apiArtifacts *packAPIArtifacts
	switch {
	case graphjin != nil && graphjin.Mode == "api":
		if apiArtifacts, err = graphjinAPIArtifacts(bundle, plainInputs); err != nil {
			return nil, err
		}
		p.files = &packMaterialized{targets: apiArtifacts.targets, restore: func() error { return nil }, commit: func() error { return nil }}
	case graphjin != nil:
		if p.files, err = installGraphjinFiles(bundle, configFile, plainInputs, ownedFiles); err != nil {
			return nil, err
		}
	default:
		p.files = &packMaterialized{targets: map[string]string{}, restore: func() error { return nil }, commit: func() error { return nil }}
	}

	recheck := jsSpread(runtime)
	recheck.set("tables", nil)
	if priorRuntime != nil {
		recheck.set("tables", priorRuntime.vals["tables"])
	}
	if _, err := a.packRuntime(ctx, orgID, authored, &jsObj{vals: map[string]any{}}, recheck); err != nil {
		return nil, err
	}
	ownedTables := map[string]bool{}
	if priorRuntime != nil {
		old, _ := priorRuntime.vals["tables"].([]any)
		for _, t := range old {
			ownedTables[jsValueString(jsObjOrEmpty(t).vals["name"])] = true
		}
	}
	if usesGraphjin {
		access := "authenticated"
		if graphjin.Mode == "api" && graphjin.Anonymous {
			access = "public"
		}
		update, err := declarativeGraphjinUpdate(bundle, plainInputs, secretValues, retiredSources, bindings, apiArtifacts, access)
		if err != nil {
			return nil, err
		}
		tables := []any{}
		for _, t := range runtime.vals["tables"].([]any) {
			table, _ := plainJSON(t).(map[string]any)
			table["database"] = table["source"]
			tables = append(tables, table)
		}
		update["tables"] = tables
		in := packGraphjinUpdate{ownedSources: ownedSources, ownedTables: ownedTables}
		var applied *appliedPackGraphjin
		if graphjin.Mode == "api" {
			if len(retiredQueries) > 0 {
				update["remove_saved_queries"] = retiredQueries
			}
			in.ownedSavedQueries = map[string]bool{}
			for target := range ownedFiles {
				if name, ok := strings.CutPrefix(target, "graphjin:saved_query:"); ok {
					in.ownedSavedQueries[name] = true
				}
			}
			in.update = jsObjOrEmpty(jsValueOf(update))
			applied, err = a.applyPackGraphjinConfigLive(ctx, graphjin.Endpoint, orgID, in)
		} else {
			in.update = jsObjOrEmpty(jsValueOf(update))
			applied, err = a.applyPackGraphjinConfig(ctx, graphjinGraphQLEndpoint(graphqlURL), orgID, configFile, in, true)
		}
		if err != nil {
			return nil, err
		}
		p.graphjin = applied.restore
		if err := a.packReadPreflight(ctx, bundle, boundQueries, graphqlURL, orgID, plainInputs); err != nil {
			return nil, err
		}
	}

	var target *packs.GraphjinTarget
	if graphjin != nil {
		target = &graphjin.GraphjinTarget
	}
	retiredHashes := map[string]string{}
	for _, row := range retired {
		if row.Kind != "source" && row.Kind != "relationships" {
			continue
		}
		metadata, _ := plainJSON(row.Metadata).(map[string]any)
		current, err := packs.InspectInstalled(ctx, a.nativePackRow(orgID), packs.StateInput{Kind: packs.ArtifactKind(row.Kind),
			TargetRef: row.TargetRef, Metadata: metadata, Graphjin: target})
		if err != nil {
			return nil, err
		}
		if current != nil {
			retiredHashes[row.identity()] = *current
		}
	}
	if p.skills, err = installPackSkills(orgID, bundle, ownedSkills); err != nil {
		return nil, err
	}
	materializedTarget := func(art packs.Artifact) string {
		identity := string(art.Kind) + ":" + art.Key
		if t, ok := p.files.targets[identity]; ok {
			return t
		}
		if t, ok := p.skills.targets[identity]; ok {
			return t
		}
		return art.TargetRef
	}
	hashes := map[string]string{}
	for i, art := range bundle.Artifacts {
		switch art.Kind {
		case packs.KindSource, packs.KindRelationships, packs.KindSpec, packs.KindSavedQuery, packs.KindSkill:
		default:
			continue
		}
		metadata := map[string]any{"materializedTarget": materializedTarget(art), "locator": boundPackLocator(bundle, art, bindings), "borrowedSource": bindings[art.Key] != ""}
		current, err := packs.InspectInstalled(ctx, a.nativePackRow(orgID), packs.StateInput{Kind: art.Kind, TargetRef: art.TargetRef,
			Metadata: metadata, Graphjin: target, Fallback: &bundle.Artifacts[i]})
		if err != nil {
			return nil, err
		}
		if current == nil {
			return nil, fmt.Errorf("pack artifact %s was not materialized", art.Key)
		}
		hashes[string(art.Kind)+":"+art.Key] = *current
	}
	if _, err := a.packRuntime(ctx, orgID, authored, &jsObj{vals: map[string]any{}}, runtime); err != nil {
		return nil, err
	}

	if err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if err := p.retireOnUpgrade(ctx, tx, retired, bundle, retiredHashes); err != nil {
			return err
		}
		if err := p.writeNative(ctx, tx, bundle, plainInputs, hashes); err != nil {
			return err
		}
		for _, art := range bundle.Artifacts {
			hash, ok := hashes[string(art.Kind)+":"+art.Key]
			if !ok {
				return fmt.Errorf("pack artifact %s has no materialized state hash", art.Key)
			}
			metadata := packJSON(map[string]any{"path": art.Path, "materializedTarget": materializedTarget(art),
				"locator": boundPackLocator(bundle, art, bindings), "borrowedSource": bindings[art.Key] != "", "stateHashVersion": 1})
			if _, err := tx.Exec(ctx, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash,
				last_applied_hash, ownership, readiness, readiness_reason, metadata)
				values ($1, $2, $3, $4, $5, $6, $7, 'managed', 'ready', null, $8::jsonb)
				on conflict (org_id, artifact_kind, target_ref) do update set pack_install_id = excluded.pack_install_id,
				artifact_key = excluded.artifact_key, desired_hash = excluded.desired_hash, last_applied_hash = excluded.last_applied_hash,
				ownership = 'managed', readiness = 'ready', readiness_reason = null, metadata = excluded.metadata, updated_at = now()`,
				p.installID, orgID, string(art.Kind), art.Key, art.TargetRef, art.Hash, hash, metadata); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `delete from pack_user_connection where org_id = $1 and pack_install_id = $2`, orgID, p.installID); err != nil {
			return err
		}
		final := maps.Clone(config)
		final["_userOAuth"] = userOAuth(bundle)
		if len(bundle.Manifest.OAuth) > 0 {
			bound := map[string]any{}
			for _, c := range bundle.Manifest.OAuth {
				if c.Scope == "user" {
					continue
				}
				if b := (&packOAuth{conn: c}).binding(nextSection); b != nil {
					bound[c.Key] = b
				}
			}
			final["_oauth"] = bound
		}
		if _, err := tx.Exec(ctx, `update pack_install set status = 'installed', config = $1::jsonb, installed_at = now(), updated_at = now() where id = $2`,
			packJSON(final), p.installID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update pack_operation set status = 'succeeded', phase = 'complete', compensation_status = 'not_required',
			completed_at = now(), updated_at = now() where id = $1`, p.operationID)
		return err
	}); err != nil {
		return nil, err
	}
	if err := p.skills.commit(); err != nil {
		log.Printf("[packs] installed %s, but could not remove the skill backup: %v", packID, err)
	}
	if err := p.retirement.commit(); err != nil {
		log.Printf("[packs] installed %s, but could not remove a retired-artifact backup: %v", packID, err)
	}
	a.queuePackMetricRefreshes(ctx, orgID, bundle)
	out, err := a.packStatus(ctx, orgID, packID)
	if err == nil && out == nil {
		err = errors.New("pack status missing after install")
	}
	return out, err
}

var retireOnUpgradeNative = func() map[packs.ArtifactKind]string {
	out := maps.Clone(retireNative)
	out[packs.KindAction] = strings.Replace(out[packs.KindAction], "'pack_uninstalled'", "'removed_from_pack'", 1)
	return out
}()

func (p *packApplyRun) retireOnUpgrade(ctx context.Context, tx pgx.Tx, retired []packArtifactRow, bundle *packs.Bundle, hashes map[string]string) error {
	for _, row := range retired {
		identity := row.identity()
		kind := packs.ArtifactKind(row.Kind)
		if query, native := retireOnUpgradeNative[kind]; native {
			locator, _ := plainJSON(row.Metadata.vals["locator"]).(map[string]any)
			args := []any{p.orgID}
			switch kind {
			case packs.KindMetric:
				role := packs.LocatorValue(locator, "role", "")
				if role == "" {
					return fmt.Errorf("pack metric %s has no retirement locator", row.Key)
				}
				args = append(args, role, packs.LocatorValue(locator, "slug", row.TargetRef))
			case packs.KindAction:
				args = append(args, packs.LocatorValue(locator, "kind", row.TargetRef))
			default:
				args = append(args, packs.LocatorValue(locator, "name", row.TargetRef))
			}
			var raw string
			if err := tx.QueryRow(ctx, query, args...).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("pack %s %s disappeared during upgrade", row.Kind, row.TargetRef)
			} else if err != nil {
				return err
			}
			var state map[string]any
			if err := json.Unmarshal([]byte(raw), &state); err != nil {
				return err
			}
			hash, err := packs.NativeStateHash(kind, state)
			if err != nil {
				return err
			}
			hashes[identity] = hash
		}
		hash, has := hashes[identity]
		if !has {
			var err error
			if hash, err = packs.CanonicalHash(map[string]any{"retired": true, "identity": identity}); err != nil {
				return err
			}
		}
		metadata := jsSpread(row.Metadata)
		metadata.set("stateHashVersion", float64(1))
		metadata.set("retiredMissing", !has)
		if kind == packs.KindSource || kind == packs.KindRelationships {
			metadata.set("retainedForGraphjinCompatibility", true)
		}
		if _, err := tx.Exec(ctx, `update pack_artifact set last_applied_hash = $1, ownership = 'retired', readiness = 'not_applicable',
			readiness_reason = null, metadata = $2::jsonb, updated_at = now() where id = $3`, hash, jsStringify(metadata, false), row.ID); err != nil {
			return err
		}
	}
	return nil
}

func nativeHash(kind packs.ArtifactKind, raw string) (string, error) {
	var row map[string]any
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return "", err
	}
	return packs.NativeStateHash(kind, row)
}

func stringList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, jsValueString(item))
	}
	return out
}

func optionalJSON(v any, present bool) *string {
	if !present {
		return nil
	}
	text := packJSON(v)
	return &text
}

func packPolicyMode(mode any) string {
	switch mode {
	case "ask":
		return "approval_required"
	case "auto":
		return "auto_approve"
	case "draft":
		return "draft_only"
	}
	return jsValueString(mode)
}

// packPolicyControlsWrite is packPolicyControlsWrite() of declarative.ts.
func packPolicyControlsWrite(bundle *packs.Bundle, policy map[string]any) bool {
	kinds := stringList(policy["appliesToKinds"])
	for _, art := range bundle.Artifacts {
		action := art.Record()
		adapter, _ := action["adapter"].(map[string]any)
		if art.Kind == packs.KindAction && slices.Contains(kinds, packString(action["kind"])) &&
			slices.Contains([]string{"graphjin_api_operation", "magento_governed_operation", "magento_changeset"}, packString(adapter["kind"])) {
			return true
		}
	}
	return false
}

// writeNative writes the workflows, metrics, watchers, policies and actions
// and records the hash of each row as written.
func (p *packApplyRun) writeNative(ctx context.Context, tx pgx.Tx, bundle *packs.Bundle, inputs map[string]any, hashes map[string]string) error {
	orgID := p.orgID
	workflows := map[string]string{}
	record := func(art packs.Artifact, raw string) error {
		hash, err := nativeHash(art.Kind, raw)
		hashes[string(art.Kind)+":"+art.Key] = hash
		return err
	}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindWorkflow {
			continue
		}
		value := art.Record()
		name := packString(value["name"])
		schedule, _ := value["schedule"].(map[string]any)
		var cron *string
		timezone := any("UTC")
		if schedule != nil {
			text := packString(schedule["cron"])
			cron = &text
			timezone = schedule["timezoneInput"]
			if v, ok := inputs[packString(schedule["timezoneInput"])]; ok && v != nil {
				timezone = v
			}
			if timezone == nil {
				timezone = "UTC"
			}
		}
		contract, hasContract := value["outputContract"]
		args := []any{orgID, name, packString(value["description"]), packTruthy(value["enabled"]), packString(value["status"]), packString(value["goal"]),
			cron, jsValueString(timezone), packTruthy(schedule["enabled"]), stringList(value["networkHosts"]),
			optionalJSON(contract, hasContract)}
		var id string
		err := tx.QueryRow(ctx, `select id::text from workflow_definition where org_id = $1 and owner_user_id = '' and name = $2 limit 1`, orgID, name).Scan(&id)
		var raw string
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			err = tx.QueryRow(ctx, `insert into workflow_definition as t (org_id, owner_user_id, name, description, enabled, status, goal, cron, cron_timezone,
				cron_enabled, network_hosts, output_contract, updated_at)
				values ($1, '', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, now()) returning t.id::text, to_jsonb(t)::text`, args...).Scan(&id, &raw)
		case err == nil:
			err = tx.QueryRow(ctx, `update workflow_definition t set description = $3, enabled = $4, status = $5, goal = $6, cron = $7, cron_timezone = $8,
				cron_enabled = $9, network_hosts = $10, output_contract = case when $11::jsonb is null and not $13 then output_contract else $11::jsonb end,
				updated_at = now() where t.id = $12::uuid and t.org_id = $1 and t.name = $2 returning t.id::text, to_jsonb(t)::text`,
				append(args, id, hasContract)...).Scan(&id, &raw)
		}
		if err == nil {
			err = record(art, raw)
		}
		if err != nil {
			return err
		}
		workflows[art.Key] = id
	}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindMetric {
			continue
		}
		value := art.Record()
		definition := maps.Clone(value)
		execution := maps.Clone(jsObjMap(value["execution"]))
		document, err := findPackSavedQuery(bundle, packString(execution["query"]))
		if err != nil {
			return err
		}
		variables, err := packVariables(execution["variables"], inputs)
		if err != nil {
			return err
		}
		execution["document"], execution["variables"], execution["runtime"] = document, variables, map[string]any{}
		definition["execution"] = execution
		definitionHash, err := packs.CanonicalHash(definition)
		if err != nil {
			return err
		}
		var raw string
		if err := tx.QueryRow(ctx, `insert into metric as t (org_id, role, slug, source, title, description, why, chart_hint, unit, direction_good,
			cadence, active, execution_mode, definition_json, definition_version, definition_hash, updated_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, true, 'saved_query', $12::jsonb, 1, $13, now())
			on conflict (org_id, role, slug) do update set source = excluded.source, title = excluded.title, description = excluded.description,
			why = excluded.why, chart_hint = excluded.chart_hint, unit = excluded.unit, direction_good = excluded.direction_good,
			cadence = excluded.cadence, active = true, execution_mode = 'saved_query', definition_json = excluded.definition_json,
			definition_version = 1, definition_hash = excluded.definition_hash, updated_at = now() returning to_jsonb(t)::text`,
			orgID, packString(value["role"]), packString(value["targetRef"]), "pack:"+p.packID, packString(value["title"]),
			packString(value["description"]), packString(value["calculationNote"]), packString(value["chartHint"]), packString(value["unit"]),
			packString(value["directionGood"]), packString(value["cadence"]), packJSON(definition), definitionHash).Scan(&raw); err != nil {
			return err
		}
		if err := record(art, raw); err != nil {
			return err
		}
	}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindWatcher {
			continue
		}
		value := art.Record()
		workflowID := workflows[packString(value["workflow"])]
		if workflowID == "" {
			return fmt.Errorf("watcher %s references missing workflow %s", art.Key, packString(value["workflow"]))
		}
		query, err := findPackSavedQuery(bundle, packString(value["query"]))
		if err != nil {
			return err
		}
		variables, err := packVariables(value["variables"], inputs)
		if err != nil {
			return err
		}
		threshold, hasThreshold := value["threshold"]
		var raw string
		if err := tx.QueryRow(ctx, `insert into watcher as t (org_id, name, workflow_id, description, enabled, query, value_path, op, threshold,
			cadence_seconds, debounce_seconds, cooldown_seconds, dedupe_key, activation, variables_json, severity, updated_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, $13, $14, $15::jsonb, $16, now())
			on conflict (org_id, name) do update set workflow_id = excluded.workflow_id, description = excluded.description, enabled = excluded.enabled,
			query = excluded.query, value_path = excluded.value_path, op = excluded.op, threshold = excluded.threshold,
			cadence_seconds = excluded.cadence_seconds, debounce_seconds = excluded.debounce_seconds, cooldown_seconds = excluded.cooldown_seconds,
			dedupe_key = excluded.dedupe_key, activation = excluded.activation, variables_json = excluded.variables_json,
			severity = excluded.severity, updated_at = now() returning to_jsonb(t)::text`,
			orgID, packString(value["name"]), workflowID, packString(value["description"]), packTruthy(value["enabled"]), query,
			packString(value["valuePath"]), packString(value["operator"]), optionalJSON(threshold, hasThreshold),
			int64(jsValueNumber(value["cadenceSeconds"])), int64(jsValueNumber(value["debounceSeconds"])), int64(jsValueNumber(value["cooldownSeconds"])),
			packString(value["dedupeKey"]), packString(value["activation"]), packJSON(variables), packString(value["severity"])).Scan(&raw); err != nil {
			return err
		}
		if err := record(art, raw); err != nil {
			return err
		}
	}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindPolicy {
			continue
		}
		value := art.Record()
		name := packString(value["name"])
		var id string
		var current bool
		err := tx.QueryRow(ctx, `select id::text, enabled from action_policy where org_id = $1 and name = $2 limit 1`, orgID, name).Scan(&id, &current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		enabled := packTruthy(value["enabled"]) && !packPolicyControlsWrite(bundle, value)
		if id != "" {
			enabled = current
		}
		targets, hasTargets := value["allowedTargets"]
		limits, hasLimits := value["limits"]
		args := []any{orgID, name, packString(value["description"]), stringList(value["appliesToKinds"]), stringList(value["appliesToScopes"]),
			packPolicyMode(value["mode"]), optionalJSON(targets, hasTargets), optionalJSON(limits, hasLimits), int64(jsValueNumber(value["priority"])), enabled}
		var raw string
		if id == "" {
			err = tx.QueryRow(ctx, `insert into action_policy as t (org_id, name, description, applies_to_kinds, applies_to_scopes, mode, allowed_targets,
				limits, priority, enabled, updated_at)
				values ($1, $2, $3, $4, $5, $6, $7::jsonb, coalesce($8::jsonb, '{}'::jsonb), $9, $10, now()) returning to_jsonb(t)::text`, args...).Scan(&raw)
		} else {
			err = tx.QueryRow(ctx, `update action_policy t set description = $3, applies_to_kinds = $4, applies_to_scopes = $5, mode = $6,
				allowed_targets = case when $12 then $7::jsonb else allowed_targets end, limits = coalesce($8::jsonb, limits), priority = $9,
				enabled = $10, updated_at = now() where t.id = $11::uuid and t.org_id = $1 and t.name = $2 returning to_jsonb(t)::text`,
				append(args, id, hasTargets)...).Scan(&raw)
		}
		if err == nil {
			err = record(art, raw)
		}
		if err != nil {
			return err
		}
	}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindAction {
			continue
		}
		value := art.Record()
		adapter, _ := value["adapter"].(map[string]any)
		readiness, reason := "blocked", any("unsupported_adapter")
		if k := adapter["kind"]; k == "graphjin_api_operation" || k == "magento_financial_handoff" {
			readiness, reason = "ready", nil
		}
		var raw string
		if err := tx.QueryRow(ctx, `insert into pack_action_definition as t (org_id, kind, description, definition, definition_hash, readiness, readiness_reason, enabled)
			values ($1, $2, $3, $4::jsonb, $5, $6, $7, true)
			on conflict (org_id, kind) do update set description = excluded.description, definition = excluded.definition,
			definition_hash = excluded.definition_hash, readiness = excluded.readiness, readiness_reason = excluded.readiness_reason,
			enabled = true, updated_at = now() returning to_jsonb(t)::text`,
			orgID, packString(value["kind"]), packString(value["description"]), packJSON(value), art.Hash, readiness, reason).Scan(&raw); err != nil {
			return err
		}
		if err := record(art, raw); err != nil {
			return err
		}
	}
	return nil
}

func jsObjMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// persistLegacyStateBaselines gives receipts from before state hash version 1
// the hash the plan reads now, when the plan finds them unchanged.
func (a *API) persistLegacyStateBaselines(ctx context.Context, installID string, plan *packs.Plan, bundle *packs.Bundle) error {
	rows, err := a.packArtifactRows(ctx, installID)
	if err != nil {
		return err
	}
	entries := map[string]packs.PlanEntry{}
	for _, e := range plan.Entries {
		entries[string(e.Kind)+":"+e.Key] = e
	}
	desired := map[string]packs.Artifact{}
	for _, art := range bundle.Artifacts {
		desired[string(art.Kind)+":"+art.Key] = art
	}
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		for _, row := range rows {
			if jsValueNumber(row.Metadata.vals["stateHashVersion"]) == 1 {
				continue
			}
			entry, ok := entries[row.identity()]
			if !ok || entry.Action != "noop" || entry.CurrentHash == nil || *entry.CurrentHash == "" {
				continue
			}
			metadata := jsSpread(row.Metadata)
			if art, ok := desired[row.identity()]; ok {
				metadata.set("locator", jsValueOf(packs.Locator(art)))
			}
			metadata.set("stateHashVersion", float64(1))
			if _, err := tx.Exec(ctx, `update pack_artifact set last_applied_hash = $1, metadata = $2::jsonb, updated_at = now() where id = $3`,
				*entry.CurrentHash, jsStringify(metadata, false), row.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// queuePackMetricRefreshes is enqueuePackMetricRefreshes(): the first
// refresh of each pack metric.
func (a *API) queuePackMetricRefreshes(ctx context.Context, orgID string, bundle *packs.Bundle) {
	if !a.MetricsRefresh {
		return
	}
	if a.Jobs == nil {
		log.Printf("[packs] initial metric refresh setup failed: the job queue is not ready")
		return
	}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindMetric {
			continue
		}
		err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
			var metricID, jobID string
			err := tx.QueryRow(ctx, `select id::text from metric where org_id = $1 and role = $2 and slug = $3 limit 1`,
				orgID, packString(art.Record()["role"]), art.TargetRef).Scan(&metricID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `insert into processing_job (org_id, kind, status, trigger, trigger_payload)
				values ($1, 'metric_refresh', 'queued', 'pack_install', jsonb_build_object('metricId', $2::text)) returning id::text`,
				orgID, metricID).Scan(&jobID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update metric set last_refresh_status = 'pending', last_refresh_error = null, last_refresh_job_id = $2,
				updated_at = now() where id = $1`, metricID, jobID); err != nil {
				return err
			}
			return a.Jobs.Build(ctx, tx, jobs.KindMetricRefresh, orgID, jobID, jobs.BuildAttempts)
		})
		if err != nil {
			log.Printf("[packs] could not schedule initial refresh for %s: %v", art.TargetRef, err)
		}
	}
}

// fail undoes what the run changed and records the failure, as apply() did.
func (p *packApplyRun) fail(ctx context.Context, cause error) {
	ctx = context.WithoutCancel(ctx)
	compensationFailed := false
	for _, restore := range []func() error{
		func() error {
			if p.skills == nil {
				return nil
			}
			return p.skills.restore()
		},
		func() error {
			if p.graphjin == nil {
				return nil
			}
			return p.graphjin(ctx)
		},
		func() error {
			if p.files == nil {
				return nil
			}
			return p.files.restore()
		},
		func() error {
			if p.retirement == nil {
				return nil
			}
			return p.retirement.restore()
		},
		func() error {
			if p.secrets == nil {
				return nil
			}
			return p.secrets()
		},
	} {
		if restore() != nil {
			compensationFailed = true
		}
	}
	message := cutUTF16(cause.Error(), 1000)
	compensation := "succeeded"
	if compensationFailed {
		compensation = "failed"
	}
	a := p.api
	if p.operationID != "" {
		_, _ = a.DB.Exec(ctx, `update pack_operation set status = 'failed', failure_phase = 'apply', error = $1, compensation_status = $2,
			completed_at = now(), updated_at = now() where id = $3`, message, compensation, p.operationID)
	}
	if p.installID == "" {
		return
	}
	prior := p.prior
	if prior != nil && (prior.Status == "installed" || prior.Status == "removed") && !compensationFailed {
		_, _ = a.DB.Exec(ctx, `update pack_install set status = $1, version = $2, source = $3, manifest_hash = $4, config = $5::jsonb,
			operation_id = $6::uuid, installed_at = $7, removed_at = $8, last_error = $9, updated_at = now() where id = $10`,
			prior.Status, prior.Version, prior.Source, prior.ManifestHash, string(prior.RawConfig), prior.OperationID,
			prior.InstalledAt, prior.RemovedAt, message, p.installID)
		return
	}
	_, _ = a.DB.Exec(ctx, `update pack_install set status = 'failed', last_error = $1, updated_at = now() where id = $2`, message, p.installID)
}
