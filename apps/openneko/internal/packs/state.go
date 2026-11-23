package packs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

var nativeStateKeys = map[ArtifactKind][]string{
	KindMetric: {"role", "slug", "source", "title", "description", "why", "chart_hint", "unit",
		"direction_good", "cadence", "active", "execution_mode", "definition_json",
		"definition_version", "definition_hash"},
	KindWorkflow: {"name", "description", "enabled", "status", "goal", "cron", "cron_timezone",
		"cron_enabled", "output_contract"},
	KindWatcher: {"workflow_id", "name", "description", "enabled", "query", "value_path", "op",
		"threshold", "cadence_seconds", "debounce_seconds", "cooldown_seconds", "dedupe_key",
		"activation", "variables_json", "severity"},
	KindPolicy: {"name", "description", "applies_to_kinds", "applies_to_scopes", "mode",
		"allowed_targets", "limits", "approver_role", "priority"},
	KindAction: {"kind", "description", "definition", "definition_hash", "enabled"},
}

// NativeStateHash is nativeArtifactStateHash(): the hash of the columns a
// pack owns on a native row.
func NativeStateHash(kind ArtifactKind, row map[string]any) (string, error) {
	keys, ok := nativeStateKeys[kind]
	if !ok {
		return "", fmt.Errorf("no native state for %s", kind)
	}
	picked := make(map[string]any, len(keys))
	for _, k := range keys {
		picked[k] = row[k]
	}
	return CanonicalHash(picked)
}

// PolicyStateHash is policyArtifactStateHash(): older receipts hashed an
// approver role that action_policy never stored.
func PolicyStateHash(row map[string]any, lastApplied string) (string, error) {
	current, err := NativeStateHash(KindPolicy, row)
	if err != nil || lastApplied == "" || current == lastApplied {
		return current, err
	}
	if _, has := row["approver_role"]; has {
		return current, nil
	}
	legacyRow := make(map[string]any, len(row)+1)
	for k, v := range row {
		legacyRow[k] = v
	}
	legacyRow["approver_role"] = "admin"
	legacy, err := NativeStateHash(KindPolicy, legacyRow)
	if err != nil {
		return "", err
	}
	if legacy == lastApplied {
		return legacy, nil
	}
	return current, nil
}

// LiveSourceStateHash is liveSourceStateHash(): only the pack-controlled
// part of a source, since GraphJin redacts credentials and fills defaults.
func LiveSourceStateHash(source map[string]any, lastApplied string) (string, error) {
	current, err := CanonicalHash(stableLiveSource(source, false))
	if err != nil || lastApplied == "" || current == lastApplied {
		return current, err
	}
	legacy, err := CanonicalHash(stableLiveSource(source, true))
	if err != nil {
		return "", err
	}
	if legacy == lastApplied {
		return legacy, nil
	}
	return current, nil
}

func stableLiveSource(source map[string]any, legacy bool) map[string]any {
	compact := withoutEmpty
	if legacy {
		compact = func(v any) any { return v }
	}
	specs := map[string]any{}
	for k, spec := range objectEntries(source["specs"]) {
		s, _ := spec.(map[string]any)
		specs[k] = map[string]any{"base_url": s["base_url"], "operations": compact(orEmpty(s["operations"], legacy))}
	}
	return map[string]any{
		"name":         source["name"],
		"kind":         source["kind"],
		"read_only":    source["read_only"],
		"access":       compact(orEmpty(source["access"], legacy)),
		"capabilities": source["capabilities"],
		"specs":        specs,
	}
}

func orEmpty(v any, legacy bool) any {
	if v == nil && !legacy {
		return map[string]any{}
	}
	return v
}

func objectEntries(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case []any:
		out := make(map[string]any, len(t))
		for i, item := range t {
			out[strconv.Itoa(i)] = item
		}
		return out
	}
	return nil
}

// withoutEmpty drops empty values: GraphJin reports an unset list as null
// after an apply and as [] after a restart. False and 0 stay.
func withoutEmpty(v any) any {
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = withoutEmpty(item)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, item := range t {
			c := withoutEmpty(item)
			switch cv := c.(type) {
			case nil:
				continue
			case string:
				if cv == "" {
					continue
				}
			case []any:
				if len(cv) == 0 {
					continue
				}
			case map[string]any:
				if len(cv) == 0 {
					continue
				}
			}
			out[k] = c
		}
		return out
	}
	return v
}

// Locator is packArtifactLocator(): how the state read finds the target.
func Locator(a Artifact) map[string]any {
	c := a.Record()
	switch a.Kind {
	case KindSource:
		return map[string]any{"name": a.TargetRef}
	case KindRelationships:
		return map[string]any{"source": jsText(c["source"], a.TargetRef)}
	case KindMetric:
		return map[string]any{"role": jsText(c["role"], ""), "slug": a.TargetRef}
	case KindWorkflow, KindWatcher, KindPolicy:
		return map[string]any{"name": jsText(c["name"], a.TargetRef)}
	case KindAction:
		return map[string]any{"kind": jsText(c["kind"], a.TargetRef)}
	}
	return map[string]any{}
}

func LocatorFromMetadata(metadata map[string]any, fallback *Artifact) map[string]any {
	if l, ok := metadata["locator"].(map[string]any); ok {
		return l
	}
	if fallback != nil {
		return Locator(*fallback)
	}
	return map[string]any{}
}

// LocatorValue is `locator[key] ?? fallback` as text.
func LocatorValue(locator map[string]any, key, fallback string) string {
	return jsText(locator[key], fallback)
}

func jsText(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	return jsString(v)
}

// GraphjinTarget is where an installed pack's GraphJin state lives: the live
// API ("api") or the config folder that OpenNeko writes ("files").
type GraphjinTarget struct {
	Mode       string
	ConfigFile string
	Live       func(context.Context) (map[string]any, error)
	SavedQuery func(context.Context, string) (*string, error)
}

// NativeLookup returns the native row as JSON values, or nil when it is gone.
type NativeLookup func(ctx context.Context, kind ArtifactKind, locator map[string]any, targetRef string) (map[string]any, error)

type StateInput struct {
	Kind        ArtifactKind
	TargetRef   string
	Metadata    map[string]any
	Graphjin    *GraphjinTarget
	Fallback    *Artifact
	LastApplied string
}

// InspectInstalled is inspectInstalledPackArtifactCurrent(): the hash of the
// target as it is now, or nil when it is missing or cannot be read.
func InspectInstalled(ctx context.Context, native NativeLookup, in StateInput) (*string, error) {
	locator := LocatorFromMetadata(in.Metadata, in.Fallback)
	switch in.Kind {
	case KindSource, KindRelationships:
		if in.Graphjin == nil {
			return nil, nil
		}
		return graphjinConfigState(ctx, in.Graphjin, in.Kind, in.TargetRef, locator, in.LastApplied)
	case KindSpec, KindSavedQuery:
		if in.Graphjin == nil {
			return nil, nil
		}
		materialized, _ := in.Metadata["materializedTarget"].(string)
		if strings.HasPrefix(materialized, "graphjin:") {
			if in.Graphjin.Mode != "api" {
				return nil, nil
			}
			return liveArtifactState(ctx, in.Graphjin, in.Kind, materialized)
		}
		if in.Graphjin.Mode != "files" || materialized == "" {
			return nil, nil
		}
		target, err := safeMaterializedTarget(in.Graphjin.ConfigFile, materialized)
		if err != nil {
			return nil, err
		}
		return HashMaterializedFile(target)
	case KindSkill:
		target, _ := in.Metadata["materializedTarget"].(string)
		if target == "" {
			return nil, nil
		}
		return HashMarkdownTree(target)
	case KindMetric, KindWorkflow, KindWatcher, KindPolicy, KindAction:
		row, err := native(ctx, in.Kind, locator, in.TargetRef)
		if err != nil || row == nil {
			return nil, err
		}
		var h string
		if in.Kind == KindPolicy {
			h, err = PolicyStateHash(row, in.LastApplied)
		} else {
			h, err = NativeStateHash(in.Kind, row)
		}
		if err != nil {
			return nil, err
		}
		return &h, nil
	}
	return nil, nil
}

func hashPtr(v any) (*string, error) {
	h, err := CanonicalHash(v)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func normalizeNewlines(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// HashMaterializedFile is hashMaterializedFile(): JSON and YAML hash as
// values, other files as text.
func HashMaterializedFile(path string) (*string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("pack-owned target became a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("pack-owned target is not a file: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := normalizeNewlines(string(raw))
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		v, err := decodeGeneric([]byte(text))
		if err != nil {
			return nil, err
		}
		return hashPtr(v)
	case ".yaml", ".yml":
		v, err := parseYAML([]byte(text))
		if err != nil {
			return nil, err
		}
		return hashPtr(v)
	}
	h := SHA256([]byte(text))
	return &h, nil
}

// HashMarkdownTree is hashMarkdownTree(): a skill folder may hold only
// folders and Markdown files.
func HashMarkdownTree(root string) (*string, error) {
	return hashMarkdownDir(root, root)
}

func hashMarkdownDir(root, dir string) (*string, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("pack-owned skill became a symlink: %s", dir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("pack-owned skill is not a directory: %s", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(entries, func(a, b fs.DirEntry) int { return localeCompare(a.Name(), b.Name()) })
	values := []any{}
	for _, e := range entries {
		child := filepath.Join(dir, e.Name())
		rel, err := filepath.Rel(root, child)
		if err != nil {
			return nil, err
		}
		if e.Type()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("pack-owned skill contains a symlink: %s", child)
		}
		if e.IsDir() {
			h, err := hashMarkdownDir(root, child)
			if err != nil {
				return nil, err
			}
			if h != nil {
				values = append(values, map[string]any{"path": rel, "hash": *h})
			}
			continue
		}
		if !e.Type().IsRegular() || strings.ToLower(filepath.Ext(e.Name())) != ".md" {
			return nil, fmt.Errorf("pack-owned skill contains a non-Markdown file: %s", child)
		}
		raw, err := os.ReadFile(child)
		if err != nil {
			return nil, err
		}
		values = append(values, map[string]any{"path": rel, "hash": SHA256([]byte(normalizeNewlines(string(raw))))})
	}
	return hashPtr(values)
}

func safeMaterializedTarget(configFile, target string) (string, error) {
	root, err := filepath.Abs(filepath.Dir(configFile))
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, absolute)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("pack materialized target escapes GraphJin config root: %s", target)
	}
	return absolute, nil
}

func configView(ctx context.Context, g *GraphjinTarget) (map[string]any, error) {
	if g.Mode == "api" {
		return g.Live(ctx)
	}
	raw, err := os.ReadFile(g.ConfigFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := parseYAML(raw)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func objects(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func listOrEmpty(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	return []any{}
}

var packAlias = regexp.MustCompile(`^pack_[a-f0-9]{20}$`)

func graphjinConfigState(ctx context.Context, g *GraphjinTarget, kind ArtifactKind, targetRef string, locator map[string]any, lastApplied string) (*string, error) {
	cfg, err := configView(ctx, g)
	if err != nil || cfg == nil {
		return nil, err
	}
	sources := objects(cfg["sources"])
	if kind == KindSource {
		name := locator["name"]
		if name == nil {
			name = targetRef
		}
		for _, s := range sources {
			if equalScalar(s["name"], name) {
				if g.Mode == "api" {
					h, err := LiveSourceStateHash(s, lastApplied)
					if err != nil {
						return nil, err
					}
					return &h, nil
				}
				return hashPtr(s)
			}
		}
		return nil, nil
	}
	source := locator["source"]
	if source == nil {
		source = targetRef
	}
	ignoreAliases, _ := locator["ignorePackAliases"].(bool)
	tables := []any{}
	for _, t := range listOrEmpty(cfg["tables"]) {
		m, _ := t.(map[string]any)
		if (equalScalar(m["source"], source) || equalScalar(m["database"], source)) &&
			!(ignoreAliases && packAlias.MatchString(jsText(m["name"], "undefined"))) {
			tables = append(tables, t)
		}
	}
	prefix := jsText(source, "") + ":"
	relationships := []any{}
	for _, r := range listOrEmpty(cfg["relationships"]) {
		m, _ := r.(map[string]any)
		if strings.HasPrefix(jsText(m["from"], ""), prefix) || strings.HasPrefix(jsText(m["to"], ""), prefix) {
			relationships = append(relationships, r)
		}
	}
	named := slices.ContainsFunc(sources, func(s map[string]any) bool { return equalScalar(s["name"], source) })
	if len(tables) == 0 && len(relationships) == 0 && !named {
		return nil, nil
	}
	return hashPtr(map[string]any{"tables": tables, "relationships": relationships})
}

func equalScalar(a, b any) bool {
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false
}

func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
}

var specExtension = regexp.MustCompile(`(?i)\.(ya?ml|json)$`)

func liveArtifactState(ctx context.Context, g *GraphjinTarget, kind ArtifactKind, target string) (*string, error) {
	if kind == KindSavedQuery {
		query, err := g.SavedQuery(ctx, strings.TrimPrefix(target, "graphjin:saved_query:"))
		if err != nil || query == nil {
			return nil, err
		}
		h := SHA256([]byte(jsTrim(normalizeNewlines(*query))))
		return &h, nil
	}
	path := strings.TrimPrefix(target, "graphjin:spec:")
	key := specExtension.ReplaceAllString(path[strings.LastIndex(path, "/")+1:], "")
	cfg, err := g.Live(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range objects(cfg["sources"]) {
		spec, _ := objectEntries(s["specs"])[key].(map[string]any)
		if doc, ok := spec["document"].(string); ok && jsTrim(doc) != "" {
			v, err := parseYAML([]byte(doc))
			if err != nil {
				return nil, err
			}
			return hashPtr(v)
		}
	}
	return nil, nil
}
