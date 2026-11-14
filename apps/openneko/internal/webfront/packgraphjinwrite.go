package webfront

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
)

// This file is apps/worker/src/packs/graphjin-config.ts with the config
// lock of persist-source-config.ts and restart.ts.

const (
	packConfigAttempts      = 8
	graphjinConfigLockStale = 10 * time.Minute
)

var (
	conflictRevision        = regexp.MustCompile(`(?i)current catalog_revision[^a-f0-9]*([a-f0-9]{64})`)
	noKeystoreMessage       = regexp.MustCompile(`(?i)secrets\.keystore\.key must be set`)
	noRemoteFilesMessage    = regexp.MustCompile(`(?i)gj_config\.(update_saved_queries|remove_saved_queries)' not found`)
	oldGraphjinConfigFields = regexp.MustCompile(`(?i)'(valid|preview_id|errors_json|catalog_revision|applied)' is not a column or a function|Cannot query field "(valid|preview_id|errors_json|catalog_revision|applied)"`)
)

type appliedPackGraphjin struct {
	catalogRevision any
	restore         func(context.Context) error
}

type packGraphjinUpdate struct {
	update            *jsObj
	ownedSources      map[string]bool
	ownedTables       map[string]bool
	ownedSavedQueries map[string]bool
	replaceSections   bool
}

func unsupportedGraphjinConfigAPI(messages []string) error {
	for _, m := range messages {
		if noKeystoreMessage.MatchString(m) {
			return errors.New(graphjinNoKeystoreMessage)
		}
	}
	for _, m := range messages {
		if noRemoteFilesMessage.MatchString(m) {
			return fmt.Errorf("This GraphJin cannot take pack saved queries and OpenAPI documents through its API. Upgrade GraphJin to 3.21.0 or later, then install the pack again. (GraphJin said: %s)", m)
		}
	}
	for _, m := range messages {
		if oldGraphjinConfigFields.MatchString(m) {
			return fmt.Errorf("GraphJin on this OpenNeko install is too old to apply pack configuration. Pack configuration needs GraphJin 3.18.35 or later. Upgrade GraphJin, then install the pack again. (GraphJin said: %s)", m)
		}
	}
	return nil
}

func isCatalogRevisionConflict(messages []string) bool {
	for _, m := range messages {
		if strings.Contains(m, "expected_catalog_revision") && strings.Contains(m, "does not match") {
			return true
		}
	}
	return false
}

func revisionFromConflict(messages []string) any {
	for _, m := range messages {
		if match := conflictRevision.FindStringSubmatch(m); match != nil {
			return match[1]
		}
	}
	return nil
}

func configObjects(v any) []any {
	if text, ok := v.(string); ok {
		parsed, err := jsParse([]byte(text))
		if err != nil {
			return []any{}
		}
		return configObjects(parsed)
	}
	if _, ok := v.([]any); !ok {
		v = jsValueOf(v)
	}
	out := []any{}
	list, _ := v.([]any)
	for _, item := range list {
		if o, ok := item.(*jsObj); ok {
			out = append(out, o)
		}
	}
	return out
}

// durablePackTables drops `database` from a table bound to a source: GraphJin
// needs it on a live patch that adds the source and rejects it on a reload.
func durablePackTables(v any) []any {
	list := configObjects(v)
	out := make([]any, len(list))
	for i, item := range list {
		table := item.(*jsObj)
		if source, ok := table.vals["source"].(string); !ok || jsTrim(source) == "" {
			out[i] = table
			continue
		}
		durable := &jsObj{}
		for _, k := range table.ordered() {
			if k != "database" {
				durable.set(k, table.vals[k])
			}
		}
		out[i] = durable
	}
	return out
}

func mergeByKey(current, additions []any, key func(*jsObj) string) []any {
	merged := &jsObj{}
	for _, list := range [][]any{current, additions} {
		for _, item := range list {
			o := item.(*jsObj)
			merged.set(key(o), o)
		}
	}
	out := make([]any, 0, len(merged.keys))
	for _, k := range merged.keys {
		out = append(out, merged.vals[k])
	}
	return out
}

func nameKey(o *jsObj) string         { return jsStr(o.vals["name"]) }
func tableKey(o *jsObj) string        { return jsStr(o.vals["source"]) + ":" + jsStr(o.vals["name"]) }
func relationshipKey(o *jsObj) string { return jsStr(o.vals["from"]) + "->" + jsStr(o.vals["to"]) }

func objectNames(list []any) map[string]bool {
	names := map[string]bool{}
	for _, item := range list {
		names[jsStr(item.(*jsObj).vals["name"])] = true
	}
	return names
}

func assertPackOwnsSources(requested []any, current map[string]bool, owned map[string]bool) error {
	for _, item := range requested {
		name := jsStr(item.(*jsObj).vals["name"])
		if name != "" && current[name] && !owned[name] {
			return fmt.Errorf("GraphJin source %s already exists and is not owned by this pack", name)
		}
	}
	return nil
}

func assertPackOwnsTables(requested []any, existing map[string]bool, owned map[string]bool) error {
	if owned == nil {
		return nil
	}
	for _, item := range requested {
		name := jsStr(item.(*jsObj).vals["name"])
		if existing[name] && !owned[name] {
			return fmt.Errorf("GraphJin table %s already exists and is not owned by this pack", name)
		}
	}
	return nil
}

func (a *API) packGraphjinMutation(ctx context.Context, endpoint, orgID, query string, variables *jsObj) (any, []string, error) {
	token, err := a.mintGraphjinToken(graphjinClaims{orgID: orgID, userID: "pack-installer", role: "admin"}, 120*time.Second)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return gjPostVars(ctx, endpoint, token, query, variables)
}

type gjConfigResult struct {
	row      *jsObj
	messages []string
	failed   bool
}

// runGjConfig sends one preview or apply. A transport error whose text names
// a stale revision comes back as a failed result, as Node's catch did.
func (a *API) runGjConfig(ctx context.Context, endpoint, orgID, mode string, previewID, revision any, update *jsObj, fields string) (gjConfigResult, error) {
	input := jsObjOf("mode", mode)
	if previewID != nil {
		input.set("preview_id", previewID)
	}
	input.set("expected_catalog_revision", revision)
	for _, k := range update.ordered() {
		input.set(k, update.vals[k])
	}
	literal, definitions, variables := graphjinInputWithJSONVariables(input)
	data, messages, err := a.packGraphjinMutation(ctx, endpoint, orgID,
		`mutation`+definitions+` { gj_config(id: "current", update: `+literal+`) { `+fields+` } }`, variables)
	if err != nil {
		if isCatalogRevisionConflict([]string{err.Error()}) {
			return gjConfigResult{messages: []string{err.Error()}, failed: true}, nil
		}
		return gjConfigResult{}, err
	}
	row, _ := firstOf(jsGet(data, "gj_config")).(*jsObj)
	if row == nil {
		row = &jsObj{}
	}
	all := append([]string{}, messages...)
	if text, ok := row.vals["errors_json"].(string); ok && text != "" {
		all = append(all, text)
	}
	if err := unsupportedGraphjinConfigAPI(all); err != nil {
		return gjConfigResult{}, err
	}
	return gjConfigResult{row: row, messages: all, failed: len(messages) > 0}, nil
}

func failureText(messages []string, fallback string) string {
	if len(messages) == 0 {
		return fallback
	}
	return strings.Join(messages, "; ")
}

func pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(250 * time.Millisecond):
		return nil
	}
}

type liveConfig struct {
	revision                       any
	sources, tables, relationships []any
}

func (a *API) readPackLiveConfig(ctx context.Context, endpoint, orgID string) (*liveConfig, error) {
	live, err := a.readLiveGraphjinConfig(ctx, endpoint, orgID)
	if err != nil {
		return nil, err
	}
	return &liveConfig{revision: live["catalogRevision"], sources: configObjects(live["sources"]),
		tables: configObjects(live["tables"]), relationships: configObjects(live["relationships"])}, nil
}

// previewThenApplyLive is previewThenApplyLive(): one gj_config update on a
// GraphJin that keeps its own config, retried on a stale revision.
func (a *API) previewThenApplyLive(ctx context.Context, endpoint, orgID string, build func(*liveConfig) (*jsObj, error)) (*liveConfig, *jsObj, any, error) {
	var conflict any
	for attempt := 1; attempt <= packConfigAttempts; attempt++ {
		live, err := a.readPackLiveConfig(ctx, endpoint, orgID)
		if err != nil {
			return nil, nil, nil, err
		}
		if live.revision == nil {
			return nil, nil, nil, errors.New("pack GraphJin config revision unavailable")
		}
		revision := live.revision
		if conflict != nil {
			revision = conflict
		}
		update, err := build(live)
		if err != nil {
			return nil, nil, nil, err
		}
		const fields = "valid applied preview_id catalog_revision errors_json"
		preview, err := a.runGjConfig(ctx, endpoint, orgID, "preview", nil, revision, update, fields)
		if err != nil {
			return nil, nil, nil, err
		}
		if preview.failed || preview.row == nil || preview.row.vals["valid"] != true || !jsTruthy(preview.row.vals["preview_id"]) {
			if attempt < packConfigAttempts && isCatalogRevisionConflict(preview.messages) {
				conflict = revisionFromConflict(preview.messages)
				continue
			}
			return nil, nil, nil, fmt.Errorf("pack GraphJin config preview failed: %s", failureText(preview.messages, "invalid preview"))
		}
		applied, err := a.runGjConfig(ctx, endpoint, orgID, "apply", preview.row.vals["preview_id"], revision, update, fields)
		if err != nil {
			return nil, nil, nil, err
		}
		if applied.failed || applied.row == nil || applied.row.vals["applied"] != true {
			if attempt < packConfigAttempts && isCatalogRevisionConflict(applied.messages) {
				conflict = revisionFromConflict(applied.messages)
				continue
			}
			return nil, nil, nil, fmt.Errorf("pack GraphJin config apply failed: %s", failureText(applied.messages, "apply rejected"))
		}
		return live, update, applied.row.vals["catalog_revision"], nil
	}
	return nil, nil, nil, errors.New("pack GraphJin config revision did not stabilize")
}

// applyPackGraphjinConfigLive is applyPackGraphjinConfigLive(): GraphJin
// writes its own config, and the restore is a compensating update.
func (a *API) applyPackGraphjinConfigLive(ctx context.Context, endpoint, orgID string, in packGraphjinUpdate) (*appliedPackGraphjin, error) {
	requestedSources := configObjects(in.update.vals["update_sources"])
	requestedTables := configObjects(in.update.vals["tables"])
	requestedRelationships := configObjects(in.update.vals["relationships"])
	_, hasRelationships := in.update.vals["relationships"]
	before, update, revision, err := a.previewThenApplyLive(ctx, endpoint, orgID, func(live *liveConfig) (*jsObj, error) {
		if err := assertPackOwnsSources(requestedSources, objectNames(live.sources), in.ownedSources); err != nil {
			return nil, err
		}
		if err := assertPackOwnsTables(requestedTables, objectNames(live.tables), in.ownedTables); err != nil {
			return nil, err
		}
		out := &jsObj{}
		for _, k := range in.update.ordered() {
			if k != "relationships" {
				out.set(k, in.update.vals[k])
			}
		}
		if len(requestedTables) > 0 {
			out.set("tables", durablePackTables(requestedTables))
		}
		if in.replaceSections && hasRelationships {
			out.set("relationships", requestedRelationships)
		} else if len(requestedRelationships) > 0 {
			out.set("relationships", mergeByKey(live.relationships, requestedRelationships, relationshipKey))
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	beforeSources, beforeTables := objectNames(before.sources), objectNames(before.tables)
	var addedSources, addedTables, addedSavedQueries []any
	for _, item := range requestedSources {
		if name := jsStr(item.(*jsObj).vals["name"]); name != "" && !beforeSources[name] {
			addedSources = append(addedSources, name)
		}
	}
	for _, item := range requestedTables {
		if name := jsStr(item.(*jsObj).vals["name"]); name != "" && !beforeTables[name] {
			addedTables = append(addedTables, name)
		}
	}
	for _, item := range configObjects(in.update.vals["update_saved_queries"]) {
		if name := jsStr(item.(*jsObj).vals["name"]); name != "" && !in.ownedSavedQueries[name] {
			addedSavedQueries = append(addedSavedQueries, name)
		}
	}
	_, relationshipsChanged := update.vals["relationships"]
	return &appliedPackGraphjin{catalogRevision: revision, restore: func(ctx context.Context) error {
		compensation := &jsObj{}
		if len(addedSources) > 0 {
			compensation.set("remove_sources", addedSources)
		}
		if len(addedTables) > 0 {
			compensation.set("remove_tables", addedTables)
		}
		if len(addedSavedQueries) > 0 {
			compensation.set("remove_saved_queries", addedSavedQueries)
		}
		if relationshipsChanged {
			compensation.set("relationships", before.relationships)
		}
		if len(compensation.keys) == 0 {
			return nil
		}
		_, _, _, err := a.previewThenApplyLive(ctx, endpoint, orgID, func(*liveConfig) (*jsObj, error) { return compensation, nil })
		return err
	}}, nil
}

// acquireGraphjinConfigLock is acquireGraphjinConfigLock(): a lock file next
// to the config that a stale holder loses after ten minutes.
func acquireGraphjinConfigLock(configFile string) (func(), error) {
	path := filepath.Join(filepath.Dir(configFile), "."+filepath.Base(configFile)+".openneko.lock")
	token := rand.Text()
	busy := errors.New("GraphJin config has another approved change in progress")
	if err := writeNew(path, []byte(token), 0o600); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(path)
		if statErr == nil && time.Since(info.ModTime()) <= graphjinConfigLockStale {
			return nil, busy
		}
		_ = os.Remove(path)
		if err := writeNew(path, []byte(token), 0o600); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return nil, busy
			}
			return nil, err
		}
	}
	return func() {
		if current, _ := os.ReadFile(path); string(current) == token {
			_ = os.Remove(path)
		}
	}, nil
}

func writeFileAtomic(path string, content []byte, mode os.FileMode, suffix string) error {
	temporary := path + "." + uuid.NewString() + suffix
	if err := os.WriteFile(temporary, content, mode); err != nil {
		return err
	}
	if err := os.Chmod(temporary, mode); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func waitForToken(ctx context.Context, file, token string, limit time.Duration, ready func() bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if raw, _ := os.ReadFile(file); strings.TrimSpace(string(raw)) == token && (ready == nil || ready()) {
			return true
		}
		if pause(ctx) != nil {
			return false
		}
	}
	return false
}

func assertGraphjinSupervisor(ctx context.Context, configFile string) error {
	dir := filepath.Dir(configFile)
	token := uuid.NewString()
	if err := writeFileAtomic(filepath.Join(dir, gjhost.PingFile), []byte(token), 0o666, ".tmp"); err != nil {
		return err
	}
	if !waitForToken(ctx, filepath.Join(dir, gjhost.PingAckFile), token, 10*time.Second, nil) {
		return errors.New("graphjin_restart_required: this deployment has not adopted the GraphJin supervisor")
	}
	return nil
}

// requestGraphjinRestart is requestGraphjinRestart() in restart.ts.
func requestGraphjinRestart(ctx context.Context, configFile, endpoint string) error {
	dir := filepath.Dir(configFile)
	token := uuid.NewString()
	if err := writeFileAtomic(filepath.Join(dir, gjhost.RestartFile), []byte(token), 0o666, ".tmp"); err != nil {
		return err
	}
	answers := func() bool {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(probe, http.MethodPost, endpoint, strings.NewReader(`{"query":"query OpenNekoRestartReady { __typename }"}`))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode != http.StatusServiceUnavailable
	}
	if !waitForToken(ctx, filepath.Join(dir, gjhost.RestartAckFile), token, 45*time.Second, answers) {
		return errors.New("graphjin_restart_required: GraphJin did not answer after the restart; see `openneko graphjin status`")
	}
	return nil
}

func readOptional(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return raw, err
}

func packKeystorePath(configFile string, durable *jsObj) (string, error) {
	dir := filepath.Dir(configFile)
	configured := jsTrim(packText(jsGet(jsGet(durable, "secrets"), "keystore"), "path"))
	if configured == "" {
		configured = "secrets.enc.yml"
	}
	configured = strings.TrimPrefix(configured, "/config/")
	keystore := configured
	if !filepath.IsAbs(keystore) {
		keystore = filepath.Join(dir, keystore)
	}
	rel, err := filepath.Rel(dir, keystore)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", errors.New("pack credential keystore must be inside the shared GraphJin config directory")
	}
	return keystore, nil
}

func packText(v any, key string) string {
	s, _ := jsGet(v, key).(string)
	return s
}

// applyPackGraphjinConfig is applyPackGraphjinConfig(): GraphJin seals the
// credentials through its live preview and apply, then OpenNeko writes the
// shared config file, which holds only gjsecret:// references.
func (a *API) applyPackGraphjinConfig(ctx context.Context, endpoint, orgID, configFile string, in packGraphjinUpdate, restart bool) (*appliedPackGraphjin, error) {
	release, err := acquireGraphjinConfigLock(configFile)
	if err != nil {
		return nil, err
	}
	defer release()
	previous, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(configFile)
	if err != nil {
		return nil, err
	}
	previousMode := info.Mode().Perm()
	if restart {
		if err := assertGraphjinSupervisor(ctx, configFile); err != nil {
			return nil, err
		}
	}
	var plain any
	if err := yaml.Unmarshal(previous, &plain); err != nil {
		return nil, fmt.Errorf("GraphJin config is not valid YAML: %w", err)
	}
	durable, _ := jsValueOf(plain).(*jsObj)
	if durable == nil {
		durable = &jsObj{}
	}
	keystore, err := packKeystorePath(configFile, durable)
	if err != nil {
		return nil, err
	}
	previousKeystore, err := readOptional(keystore)
	if err != nil {
		return nil, err
	}
	requestedTables := configObjects(in.update.vals["tables"])
	requestedRelationships := configObjects(in.update.vals["relationships"])
	requestedRoles := configObjects(in.update.vals["roles"])
	requestedSources := configObjects(in.update.vals["update_sources"])
	_, hasTables := in.update.vals["tables"]
	_, hasRelationships := in.update.vals["relationships"]

	var appliedUpdate *jsObj
	var revisionOut, conflict any
	for attempt := 1; attempt <= packConfigAttempts && appliedUpdate == nil; attempt++ {
		operation := fmt.Sprintf("PackConfigRevision_%d_%s", attempt, strings.ReplaceAll(uuid.NewString(), "-", ""))
		data, messages, err := a.packGraphjinMutation(ctx, endpoint, orgID,
			`query `+operation+` { gj_config(id: "current") { catalog_revision sources tables relationships } }`, &jsObj{})
		if err != nil {
			return nil, err
		}
		if err := unsupportedGraphjinConfigAPI(messages); err != nil {
			return nil, err
		}
		current, _ := firstOf(jsGet(data, "gj_config")).(*jsObj)
		reported, _ := jsGet(current, "catalog_revision").(string)
		if reported == "" || len(messages) > 0 {
			return nil, fmt.Errorf("pack GraphJin config revision unavailable: %s", failureText(messages, "no revision"))
		}
		var revision any = reported
		if conflict != nil {
			revision = conflict
		}
		if err := assertPackOwnsSources(requestedSources, objectNames(configObjects(jsGet(current, "sources"))), in.ownedSources); err != nil {
			return nil, err
		}
		existingTables := objectNames(append(configObjects(durable.vals["tables"]), configObjects(jsGet(current, "tables"))...))
		if err := assertPackOwnsTables(requestedTables, existingTables, in.ownedTables); err != nil {
			return nil, err
		}
		update := &jsObj{}
		for _, k := range in.update.ordered() {
			if k != "relationships" {
				update.set(k, in.update.vals[k])
			}
		}
		if len(requestedRoles) > 0 {
			update.set("roles", mergeByKey(configObjects(durable.vals["roles"]), requestedRoles, nameKey))
		}
		if in.replaceSections && hasTables {
			update.set("tables", requestedTables)
		} else if len(requestedTables) > 0 {
			update.set("tables", mergeByKey(configObjects(durable.vals["tables"]), requestedTables, tableKey))
		}
		if in.replaceSections && hasRelationships {
			update.set("relationships", requestedRelationships)
		} else if len(requestedRelationships) > 0 {
			update.set("relationships", mergeByKey(configObjects(durable.vals["relationships"]), requestedRelationships, relationshipKey))
		}
		preview, err := a.runGjConfig(ctx, endpoint, orgID, "preview", nil, revision, update, "valid preview_id errors_json")
		if err != nil {
			return nil, err
		}
		if preview.failed || preview.row.vals["valid"] != true || !jsTruthy(preview.row.vals["preview_id"]) {
			if attempt < packConfigAttempts && isCatalogRevisionConflict(preview.messages) {
				conflict = revisionFromConflict(preview.messages)
				if err := pause(ctx); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("pack GraphJin config preview failed: %s", failureText(preview.messages, "invalid preview"))
		}
		applied, err := a.runGjConfig(ctx, endpoint, orgID, "apply", preview.row.vals["preview_id"], revision, update, "applied catalog_revision errors_json")
		if err != nil {
			return nil, err
		}
		if applied.failed || applied.row.vals["applied"] != true {
			if attempt < packConfigAttempts && isCatalogRevisionConflict(applied.messages) {
				conflict = revisionFromConflict(applied.messages)
				if err := pause(ctx); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("pack GraphJin config apply failed: %s", failureText(applied.messages, "apply rejected"))
		}
		appliedUpdate, revisionOut = update, applied.row.vals["catalog_revision"]
	}
	if appliedUpdate == nil {
		return nil, errors.New("pack GraphJin config revision did not stabilize")
	}

	restoreSnapshot := func(ctx context.Context) error {
		if previousKeystore != nil {
			if err := writeFileAtomic(keystore, previousKeystore, 0o600, ".pack-rollback"); err != nil {
				return err
			}
		} else if err := os.Remove(keystore); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := writeFileAtomic(configFile, previous, previousMode, ".pack-rollback"); err != nil {
			return err
		}
		if restart {
			return requestGraphjinRestart(ctx, configFile, endpoint)
		}
		return nil
	}
	appliedConfig, err := persistPackGraphjin(ctx, configFile, endpoint, appliedUpdate, previousMode, restart)
	var appliedKeystore []byte
	if err == nil {
		appliedKeystore, err = readOptional(keystore)
	}
	if err != nil {
		if rollbackErr := restoreSnapshot(ctx); rollbackErr != nil {
			return nil, fmt.Errorf("pack GraphJin persistence failed and its live update could not be rolled back: %w; %v", err, rollbackErr)
		}
		return nil, err
	}
	return &appliedPackGraphjin{catalogRevision: revisionOut, restore: func(ctx context.Context) error {
		unlock, err := acquireGraphjinConfigLock(configFile)
		if err != nil {
			return err
		}
		defer unlock()
		currentConfig, err := os.ReadFile(configFile)
		if err != nil {
			return err
		}
		currentKeystore, err := readOptional(keystore)
		if err != nil {
			return err
		}
		if !bytes.Equal(currentConfig, appliedConfig) || (currentKeystore == nil) != (appliedKeystore == nil) || !bytes.Equal(currentKeystore, appliedKeystore) {
			return errors.New("GraphJin configuration changed after pack apply; rollback preserved the newer state")
		}
		return restoreSnapshot(ctx)
	}}, nil
}

// persistPackGraphjin is persistGraphjinSourceConfigUpdate() followed by
// persistPackSections(), in one write.
func persistPackGraphjin(ctx context.Context, configFile, endpoint string, update *jsObj, mode os.FileMode, restart bool) ([]byte, error) {
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	doc, err := mergeGraphjinConfigUpdate(raw, update)
	if err != nil {
		return nil, err
	}
	root := doc.Content[0]
	if tables, ok := update.vals["tables"]; ok {
		yamlMapSet(root, "tables", jsToYAMLNode(durablePackTables(tables)))
	}
	if roles, ok := update.vals["roles"]; ok {
		yamlMapSet(root, "roles", jsToYAMLNode(roles))
	}
	if relationships, ok := update.vals["relationships"]; ok {
		yamlMapSet(root, "relationships", jsToYAMLNode(relationships))
	}
	text, err := encodeYAML(doc)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(configFile, []byte(text), mode, ".graphjin-config.tmp"); err != nil {
		return nil, err
	}
	if restart {
		if err := requestGraphjinRestart(ctx, configFile, endpoint); err != nil {
			return nil, err
		}
	}
	return os.ReadFile(configFile)
}
