package webfront

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

var (
	packInlineTemplate = regexp.MustCompile(`\{\{([^}]+)}}`)
	packQueryExt       = regexp.MustCompile(`(?i)\.(gql|graphql)$`)
)

func (e packExposure) value() map[string]any {
	if e.mutation == "" {
		return map[string]any{"expose_top_level": true}
	}
	return map[string]any{"expose_mutation": true, "allowed_roles": []any{"pack_api_executor"}, "expose_as": e.as}
}

type packAPIArtifacts struct {
	specDocuments map[string]string
	savedQueries  []any
	targets       map[string]string
}

func packAPIAuth(auth map[string]any, requestHeader string) map[string]any {
	if auth["type"] == "bearer" {
		if requestHeader != "" {
			return map[string]any{"scheme": "bearer", "token_from_request": map[string]any{"header": requestHeader}}
		}
		return map[string]any{"scheme": "bearer", "token": auth["token"]}
	}
	in, ok := auth["key_in"]
	if !ok || in == nil {
		in = "header"
	}
	return map[string]any{"scheme": "api_key", "key_name": auth["key_name"], "key_value": auth["key_value"], "key_in": in}
}

// jsURLText is new URL(text).toString() without one trailing slash.
func jsURLText(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return strings.TrimSuffix(raw, "/")
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if u.Path == "" && u.Opaque == "" {
		u.Path = "/"
	}
	return strings.TrimSuffix(u.String(), "/")
}

// declarativeGraphjinUpdate is declarativeGraphjinUpdate() of
// apps/worker/src/packs/declarative.ts.
func declarativeGraphjinUpdate(bundle *packs.Bundle, inputs map[string]any, secretValues map[string]string, retired []string,
	bindings map[string]string, api *packAPIArtifacts, callerAccess string) (map[string]any, error) {
	if err := declarativePackChecks(bundle, inputs, secretValues, bindings); err != nil {
		return nil, err
	}
	exposures, err := packExposures(bundle)
	if err != nil {
		return nil, err
	}
	exposesMutations := false
	for _, own := range exposures {
		for _, e := range own {
			exposesMutations = exposesMutations || e.mutation != ""
		}
	}
	anonymous := callerAccess == "public"
	sources := []any{}
	exposesSourceMutations := false
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindSource || bindings[art.Key] != "" {
			continue
		}
		authored := art.Record()
		auth, _ := authored["auth"].(map[string]any)
		var personal *packs.OAuthConnection
		for i, c := range bundle.Manifest.OAuth {
			if c.Scope == "user" && auth != nil && auth["token"] == "{{secret."+c.AccessToken+"}}" {
				personal = &bundle.Manifest.OAuth[i]
				break
			}
		}
		withAuth := authored
		if personal != nil {
			bound := maps.Clone(auth)
			bound["token"] = "request-bound"
			withAuth = maps.Clone(authored)
			withAuth["auth"] = bound
		}
		resolved, err := packValue(withAuth, inputs, secretValues)
		if err != nil {
			return nil, err
		}
		source := resolved.(map[string]any)
		if source["kind"] == "database" {
			out := maps.Clone(source)
			out["default"], out["read_only"] = false, true
			out["access"] = map[string]any{"read": "authenticated", "write": "blocked", "delete": "blocked"}
			out["capabilities"] = map[string]any{"data.read": true, "data.write": false, "schema.read": true, "schema.write": false}
			sources = append(sources, out)
			continue
		}
		spec := findSpec(bundle, source["openapi"])
		name := packString(source["name"])
		specEntry := map[string]any{"base_url": jsURLText(packString(source["base_url"]))}
		if api != nil {
			specEntry["document"] = api.specDocuments[spec.Path]
		}
		if a, ok := source["auth"].(map[string]any); ok {
			header := ""
			if personal != nil {
				header = packConnectionHeader(bundle.Manifest.Metadata.ID, personal.Key)
			}
			specEntry["auth"] = packAPIAuth(a, header)
		}
		operations := map[string]any{}
		for id, e := range exposures[name] {
			if anonymous && e.mutation != "" {
				continue
			}
			operations[id] = e.value()
			exposesSourceMutations = exposesSourceMutations || e.mutation != ""
		}
		if len(operations) > 0 {
			specEntry["operations"] = operations
		}
		requested, _ := source["capabilities"].(map[string]any)
		writeOn := requested["api.write"] == true && !anonymous
		removeOn := requested["api.delete"] == true && !anonymous
		read, write, remove := callerAccess, "blocked", "blocked"
		if requested["api.read"] == false {
			read = "blocked"
		}
		if writeOn {
			write = callerAccess
		}
		if removeOn {
			remove = callerAccess
		}
		capabilities := map[string]any{}
		for k, allowed := range map[string]bool{"api.read": requested["api.read"] != false, "api.write": writeOn, "api.delete": removeOn} {
			if !anonymous || !allowed {
				capabilities[k] = allowed
			}
		}
		out := map[string]any{
			"name": source["name"], "kind": source["kind"], "default": false, "read_only": !(writeOn || removeOn),
			"access":       map[string]any{"read": read, "write": write, "delete": remove},
			"specs":        map[string]any{strings.TrimSuffix(path.Base(spec.Path), path.Ext(spec.Path)): specEntry},
			"capabilities": capabilities,
		}
		if api == nil {
			out["specs_dir"] = "/config/specs"
		}
		sources = append(sources, out)
	}
	names := map[string]string{}
	for _, art := range bundle.Artifacts {
		if art.Kind == packs.KindSource && bindings[art.Key] != "" {
			names[packString(art.Record()["name"])] = bindings[art.Key]
		}
	}
	relationships := []any{}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindRelationships {
			continue
		}
		value := art.Record()
		source := packString(value["source"])
		if bound, ok := names[source]; ok {
			source = bound
		}
		edges, _ := value["relationships"].([]any)
		for _, edge := range edges {
			e, _ := edge.(map[string]any)
			relationships = append(relationships, map[string]any{"from": source + ":" + packString(e["left"]), "to": source + ":" + packString(e["right"])})
		}
	}
	update := map[string]any{"update_sources": sources}
	if exposesMutations && exposesSourceMutations {
		update["roles"] = []any{map[string]any{"name": "pack_api_executor", "comment": "Short-lived executor for approved pack API actions"}}
	}
	if len(relationships) > 0 {
		update["relationships"] = relationships
	}
	if api != nil && len(api.savedQueries) > 0 {
		update["update_saved_queries"] = api.savedQueries
	}
	if len(retired) > 0 {
		patches := make([]any, len(retired))
		for i, name := range retired {
			patches[i] = map[string]any{"name": name, "read_only": true, "access": map[string]any{"read": "blocked", "write": "blocked", "delete": "blocked"}}
		}
		update["source_patches"] = patches
	}
	return update, nil
}

func renderPackTemplate(value string, inputs map[string]any) (string, error) {
	var missing error
	out := packInlineTemplate.ReplaceAllStringFunc(value, func(match string) string {
		key := strings.TrimSpace(match[2 : len(match)-2])
		resolved, ok := inputs[key]
		if !ok {
			if missing == nil {
				missing = fmt.Errorf("missing template input %s", key)
			}
			return match
		}
		return jsValueString(resolved)
	})
	return out, missing
}

func packSavedQueryName(packID, file string) string {
	return strings.ReplaceAll(packID, "-", "_") + "_" + packQueryExt.ReplaceAllString(path.Base(file), "")
}

// graphjinAPIArtifacts is graphjinApiArtifacts(): specs inline and saved
// queries as update_saved_queries, for a GraphJin that keeps its own config.
func graphjinAPIArtifacts(bundle *packs.Bundle, inputs map[string]any) (*packAPIArtifacts, error) {
	out := &packAPIArtifacts{specDocuments: map[string]string{}, savedQueries: []any{}, targets: map[string]string{}}
	for _, art := range bundle.Artifacts {
		switch art.Kind {
		case packs.KindSpec:
			raw, err := os.ReadFile(filepath.Join(bundle.Root, art.Path))
			if err != nil {
				return nil, err
			}
			text, err := renderPackTemplate(string(raw), inputs)
			if err != nil {
				return nil, err
			}
			out.specDocuments[art.Path] = text
			out.targets[string(art.Kind)+":"+art.Key] = "graphjin:spec:" + art.Path
		case packs.KindSavedQuery:
			name := packSavedQueryName(bundle.Manifest.Metadata.ID, art.Path)
			out.savedQueries = append(out.savedQueries, map[string]any{"name": name, "query": packString(art.Content)})
			out.targets[string(art.Kind)+":"+art.Key] = "graphjin:saved_query:" + name
		}
	}
	return out, nil
}

func lpathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func writePackFile(target, content string, owned bool) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	previous, err := os.ReadFile(target)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if existed && !owned {
		return nil, fmt.Errorf("pack file target %s already exists and is not owned by this pack", target)
	}
	if err := writeFileAtomic(target, []byte(content), 0o644, ".pack-stage"); err != nil {
		return nil, err
	}
	return func() error {
		if existed {
			return writeFileAtomic(target, previous, 0o644, ".pack-rollback")
		}
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}, nil
}

type packMaterialized struct {
	targets         map[string]string
	restore, commit func() error
}

func undoAll(steps []func() error) error {
	var first error
	for i := len(steps) - 1; i >= 0; i-- {
		if err := steps[i](); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// installGraphjinFiles writes the pack's specs and saved queries next to the
// GraphJin config file.
func installGraphjinFiles(bundle *packs.Bundle, configFile string, inputs map[string]any, owned map[string]bool) (*packMaterialized, error) {
	root := filepath.Dir(configFile)
	var restorers []func() error
	out := &packMaterialized{targets: map[string]string{}}
	for _, art := range bundle.Artifacts {
		var target, content string
		switch art.Kind {
		case packs.KindSpec:
			raw, err := os.ReadFile(filepath.Join(bundle.Root, art.Path))
			if err == nil {
				content, err = renderPackTemplate(string(raw), inputs)
			}
			if err != nil {
				_ = undoAll(restorers)
				return nil, err
			}
			target = filepath.Join(root, "specs", path.Base(art.Path))
		case packs.KindSavedQuery:
			target = filepath.Join(root, "queries", strings.ReplaceAll(bundle.Manifest.Metadata.ID, "-", "_")+"_"+path.Base(art.Path))
			content = packString(art.Content)
		default:
			continue
		}
		restore, err := writePackFile(target, content, owned[target])
		if err != nil {
			_ = undoAll(restorers)
			return nil, err
		}
		restorers = append(restorers, restore)
		out.targets[string(art.Kind)+":"+art.Key] = target
	}
	out.restore = func() error { return undoAll(restorers) }
	out.commit = func() error { return nil }
	return out, nil
}

// installPackSkills swaps each pack skill folder into the org's skills; the
// old folder stays as a backup until commit.
func installPackSkills(orgID string, bundle *packs.Bundle, owned map[string]bool) (*packMaterialized, error) {
	workspace, err := orgWorkspace(orgID)
	if err != nil {
		return nil, err
	}
	var restorers, committers []func() error
	out := &packMaterialized{targets: map[string]string{}}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindSkill {
			continue
		}
		source := filepath.Join(bundle.Root, filepath.Dir(art.Path))
		target := filepath.Join(workspace.Skills, art.TargetRef)
		backup := target + "." + uuid.NewString() + ".pack-backup"
		stage := target + "." + uuid.NewString() + ".pack-stage"
		existed := lpathExists(target)
		if existed && !owned[target] {
			_ = undoAll(restorers)
			return nil, fmt.Errorf("skill target %s already exists and is not owned by this pack", filepath.Base(target))
		}
		err := copyTree(source, stage)
		if err == nil && existed {
			err = os.Rename(target, backup)
		}
		if err == nil {
			if err = os.Rename(stage, target); err != nil && existed {
				_ = os.Rename(backup, target)
			}
		}
		if err != nil {
			_ = os.RemoveAll(stage)
			_ = undoAll(restorers)
			return nil, err
		}
		out.targets[string(art.Kind)+":"+art.Key] = target
		restorers = append(restorers, func() error {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			if existed {
				return os.Rename(backup, target)
			}
			return nil
		})
		committers = append(committers, func() error { return os.RemoveAll(backup) })
	}
	out.restore = func() error { return undoAll(restorers) }
	out.commit = func() error {
		var first error
		for _, c := range committers {
			if err := c(); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	return out, nil
}

// snapshotUploadedPack copies an uploaded pack to a private folder and
// checks it against the approved content hash.
func snapshotUploadedPack(root string, bundle *packs.Bundle) (*packs.Bundle, func(), error) {
	dir, err := os.MkdirTemp(root, ".apply-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	target := filepath.Join(dir, "bundle")
	hash := ""
	if err = copyTree(bundle.Root, target); err == nil {
		hash, err = packs.HashFiles(target)
	}
	if err == nil && (bundle.Upload == nil || hash != bundle.Upload.ContentHash) {
		err = errors.New("uploaded pack changed while preparing installation")
	}
	var loaded *packs.Bundle
	if err == nil {
		loaded, err = packs.LoadBundle(target)
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	loaded.Upload = bundle.Upload
	for i, art := range loaded.Artifacts {
		for _, catalog := range bundle.Artifacts {
			if catalog.Kind == art.Kind && catalog.Key == art.Key {
				loaded.Artifacts[i].TargetRef = catalog.TargetRef
			}
		}
	}
	return loaded, cleanup, nil
}

// boundPackLocator is boundPackLocator() of pack-artifacts.ts.
func boundPackLocator(bundle *packs.Bundle, art packs.Artifact, bindings map[string]string) map[string]any {
	if name := bindings[art.Key]; name != "" {
		return map[string]any{"name": name}
	}
	if art.Kind == packs.KindRelationships {
		for _, s := range bundle.Artifacts {
			if s.Kind == packs.KindSource && s.Record()["name"] == art.Record()["source"] && bindings[s.Key] != "" {
				return map[string]any{"source": bindings[s.Key], "ignorePackAliases": true}
			}
		}
	}
	return packs.Locator(art)
}

func findPackSavedQuery(bundle *packs.Bundle, name string) (string, error) {
	for _, art := range bundle.Artifacts {
		if art.Kind == packs.KindSavedQuery && strings.TrimSuffix(path.Base(art.Path), path.Ext(art.Path)) == name {
			if text, ok := art.Content.(string); ok {
				return text, nil
			}
		}
	}
	return "", fmt.Errorf("pack saved query %s is missing", name)
}

func packVariables(value any, inputs map[string]any) (any, error) {
	if value == nil {
		value = defaultPackVariables
	}
	definition, err := packValue(value, inputs, nil)
	if err != nil {
		return nil, err
	}
	if _, err := resolveWatcherVariables(jsValueOf(definition), jsNow()); err != nil {
		return nil, err
	}
	return definition, nil
}

// boundPackBundle is the bundle with each saved query bound to its sources.
func boundPackBundle(bundle *packs.Bundle, queries map[string]string) *packs.Bundle {
	out := *bundle
	out.Artifacts = make([]packs.Artifact, len(bundle.Artifacts))
	for i, art := range bundle.Artifacts {
		if text, ok := queries[art.Path]; ok && art.Kind == packs.KindSavedQuery {
			art.Content = text
		}
		out.Artifacts[i] = art
	}
	return &out
}
