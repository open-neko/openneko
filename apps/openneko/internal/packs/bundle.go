package packs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

type ArtifactKind string

const (
	KindSource        ArtifactKind = "source"
	KindRelationships ArtifactKind = "relationships"
	KindSpec          ArtifactKind = "spec"
	KindSavedQuery    ArtifactKind = "saved_query"
	KindMetric        ArtifactKind = "metric"
	KindWorkflow      ArtifactKind = "workflow"
	KindWatcher       ArtifactKind = "watcher"
	KindAction        ArtifactKind = "action"
	KindPolicy        ArtifactKind = "policy"
	KindSkill         ArtifactKind = "skill"
)

type Artifact struct {
	Kind      ArtifactKind `json:"kind"`
	Key       string       `json:"key"`
	TargetRef string       `json:"targetRef"`
	Path      string       `json:"path"`
	Hash      string       `json:"hash"`
	Content   any          `json:"content"`
}

// Record is the artifact content as an object, or an empty one.
func (a Artifact) Record() map[string]any {
	m, _ := a.Content.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

type Bundle struct {
	Root         string
	Manifest     *Manifest
	ManifestHash string
	BundleHash   string
	Artifacts    []Artifact
	Upload       *Upload
}

var (
	collatorMu sync.Mutex
	collator   = collate.New(language.Und)
)

// localeCompare orders strings as String.prototype.localeCompare() does.
func localeCompare(a, b string) int {
	collatorMu.Lock()
	defer collatorMu.Unlock()
	return collator.CompareString(a, b)
}

func stableFileKey(kind ArtifactKind, file string) string {
	base := filepath.Base(file)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return string(kind) + "." + strings.ReplaceAll(stem, "_", "-")
}

func outside(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel)
}

// safePath is assertSafePath(): no symlink on the way and no escape.
func safePath(root, candidate string) (string, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	absolute := filepath.Join(root, candidate)
	if filepath.IsAbs(candidate) {
		absolute = filepath.Clean(candidate)
	}
	lexical, err := filepath.Rel(rootReal, absolute)
	if err != nil || outside(lexical) {
		return "", fmt.Errorf("pack path escapes root: %s", candidate)
	}
	cursor := rootReal
	if lexical != "." {
		for _, segment := range strings.Split(lexical, string(filepath.Separator)) {
			if segment == "" {
				continue
			}
			cursor = filepath.Join(cursor, segment)
			info, err := os.Lstat(cursor)
			if err != nil {
				return "", err
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				return "", fmt.Errorf("pack path cannot contain symlinks: %s", candidate)
			}
		}
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	physical, err := filepath.Rel(rootReal, resolved)
	if err != nil || outside(physical) {
		return "", fmt.Errorf("pack path resolves outside root: %s", candidate)
	}
	return resolved, nil
}

func readArtifactContent(path string) (any, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	text := strings.ToValidUTF8(string(raw), "�")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		content, err := decodeGeneric([]byte(text))
		if err != nil {
			return nil, "", err
		}
		h, err := CanonicalHash(content)
		return content, h, err
	case ".yaml", ".yml":
		content, err := parseYAML([]byte(text))
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		h, err := CanonicalHash(content)
		return content, h, err
	}
	return text, SHA256([]byte(text)), nil
}

func artifactIdentity(kind ArtifactKind, path string, content any) (string, string) {
	if m, ok := content.(map[string]any); ok {
		key, isKey := m["key"].(string)
		if !isKey {
			key = stableFileKey(kind, path)
		}
		ref, isRef := m["targetRef"].(string)
		if !isRef {
			ref = key
		}
		return key, ref
	}
	key := stableFileKey(kind, path)
	return key, key
}

func sortedEntries(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(entries, func(a, b os.DirEntry) int { return localeCompare(a.Name(), b.Name()) })
	return entries, nil
}

func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func filesInDirectory(root, path string) ([]string, error) {
	directory, err := safePath(root, path)
	if err != nil {
		return nil, err
	}
	entries, err := sortedEntries(directory)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("pack directory cannot contain symlinks: %s/%s", path, e.Name())
		}
		if !e.Type().IsRegular() {
			continue
		}
		files = append(files, relTo(root, filepath.Join(directory, e.Name())))
	}
	return files, nil
}

func artifactFromFile(root string, kind ArtifactKind, path string) (Artifact, error) {
	safe, err := safePath(root, path)
	if err != nil {
		return Artifact{}, err
	}
	content, hash, err := readArtifactContent(safe)
	if err != nil {
		return Artifact{}, err
	}
	if err := ValidateArtifactContent(kind, content, path); err != nil {
		return Artifact{}, err
	}
	key, ref := artifactIdentity(kind, path, content)
	return Artifact{Kind: kind, Key: key, TargetRef: ref, Path: relTo(root, safe), Hash: hash, Content: content}, nil
}

func sourceArtifacts(root, path string) ([]Artifact, error) {
	file, err := artifactFromFile(root, KindSource, path)
	if err != nil {
		return nil, err
	}
	list, ok := file.Record()["sources"].([]any)
	if !ok {
		return nil, fmt.Errorf("%s must contain a sources array", path)
	}
	out := make([]Artifact, 0, len(list))
	for i, s := range list {
		value, ok := s.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s source %d must be an object", path, i)
		}
		name, _ := value["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("%s source %d requires name", path, i)
		}
		key, isKey := value["key"].(string)
		if !isKey {
			key = "source." + name
		}
		content := map[string]any{}
		for k, v := range value {
			if k != "key" {
				content[k] = v
			}
		}
		h, err := CanonicalHash(content)
		if err != nil {
			return nil, err
		}
		out = append(out, Artifact{Kind: KindSource, Key: key, TargetRef: name, Path: file.Path, Hash: h, Content: content})
	}
	return out, nil
}

func directoryArtifacts(root string, kind ArtifactKind, path *string) ([]Artifact, error) {
	if path == nil {
		return nil, nil
	}
	files, err := filesInDirectory(root, *path)
	if err != nil {
		return nil, err
	}
	allowed := []string{".yaml", ".yml"}
	if kind == KindSavedQuery {
		allowed = []string{".gql", ".graphql"}
	}
	for _, f := range files {
		if !slices.Contains(allowed, strings.ToLower(filepath.Ext(f))) {
			return nil, fmt.Errorf("unsupported %s artifact file: %s", kind, f)
		}
	}
	out := make([]Artifact, 0, len(files))
	for _, f := range files {
		a, err := artifactFromFile(root, kind, f)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func skillArtifact(root, path string) (Artifact, error) {
	a, err := artifactFromFile(root, KindSkill, filepath.ToSlash(filepath.Join(path, "SKILL.md")))
	if err != nil {
		return Artifact{}, err
	}
	name := filepath.Base(path)
	a.Key, a.TargetRef = "skill."+name, name
	a.Hash, err = hashTree(root, path)
	return a, err
}

func hashTree(root, path string) (string, error) {
	directory, err := safePath(root, path)
	if err != nil {
		return "", err
	}
	entries, err := sortedEntries(directory)
	if err != nil {
		return "", err
	}
	values := []any{}
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("pack skill cannot contain symlinks: %s/%s", path, e.Name())
		}
		child := filepath.Join(directory, e.Name())
		childPath := relTo(root, child)
		switch {
		case e.IsDir():
			h, err := hashTree(root, childPath)
			if err != nil {
				return "", err
			}
			values = append(values, map[string]any{"path": childPath, "hash": h})
		case e.Type().IsRegular():
			if strings.ToLower(filepath.Ext(e.Name())) != ".md" {
				return "", fmt.Errorf("pack skills may contain Markdown only: %s", childPath)
			}
			_, h, err := readArtifactContent(child)
			if err != nil {
				return "", err
			}
			values = append(values, map[string]any{"path": childPath, "hash": h})
		}
	}
	return CanonicalHash(values)
}

func validateReferences(artifacts []Artifact) error {
	sources, queries, workflows := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, a := range artifacts {
		switch a.Kind {
		case KindSource:
			sources[a.TargetRef] = true
		case KindSavedQuery:
			base := filepath.Base(a.Path)
			queries[strings.TrimSuffix(base, filepath.Ext(base))] = true
		case KindWorkflow:
			workflows[a.Key] = true
		}
	}
	requireSource := func(a Artifact, v any) error {
		if !sources[jsString(v)] {
			return fmt.Errorf("%s artifact %s references missing source %s", a.Kind, a.Path, jsString(v))
		}
		return nil
	}
	requireQuery := func(a Artifact, v any) error {
		if !queries[jsString(v)] {
			return fmt.Errorf("%s artifact %s references missing saved query %s", a.Kind, a.Path, jsString(v))
		}
		return nil
	}
	for _, a := range artifacts {
		if a.Kind != KindRelationships && a.Kind != KindMetric && a.Kind != KindWatcher && a.Kind != KindAction {
			continue
		}
		record, ok := a.Content.(map[string]any)
		if !ok {
			return fmt.Errorf("%s artifact %s must be an object", a.Kind, a.Path)
		}
		var err error
		switch a.Kind {
		case KindRelationships:
			err = requireSource(a, record["source"])
		case KindMetric:
			exec, _ := record["execution"].(map[string]any)
			if err = requireSource(a, exec["source"]); err == nil {
				err = requireQuery(a, exec["query"])
			}
		case KindWatcher:
			if !workflows[jsString(record["workflow"])] {
				return fmt.Errorf("watcher artifact %s references missing workflow %s", a.Path, jsString(record["workflow"]))
			}
			err = requireQuery(a, record["query"])
		case KindAction:
			adapter, _ := record["adapter"].(map[string]any)
			if src, has := adapter["source"]; has {
				err = requireSource(a, src)
			}
			for _, f := range []string{"preconditionQuery", "mutationQuery", "reconciliationQuery"} {
				if q, has := adapter[f]; has && err == nil {
					err = requireQuery(a, q)
				}
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func jsString(v any) string {
	switch t := v.(type) {
	case nil:
		return "undefined"
	case string:
		return t
	case float64:
		return numberText(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			if x != nil {
				parts[i] = jsString(x)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// LoadBundle is loadSolutionPack().
func LoadBundle(rootInput string) (*Bundle, error) {
	root, err := filepath.EvalSymlinks(rootInput)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	manifestPath, err := safePath(root, "pack.yaml")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	parsed, err := parseYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("pack.yaml: %w", err)
	}
	manifest, err := ParseManifest(parsed)
	if err != nil {
		return nil, err
	}
	manifest.Health.ReadinessKeys = readinessKeys(raw)
	gj := manifest.Artifacts.GraphJin
	art := manifest.Artifacts
	var targets []string
	if gj != nil {
		targets = append(targets, gj.Sources, gj.Relationships)
		targets = append(targets, gj.Specs...)
		if gj.SavedQueries != nil {
			targets = append(targets, *gj.SavedQueries)
		}
	}
	for _, p := range []*string{art.Metrics, art.Workflows, art.Watchers, art.Actions, art.Policies} {
		if p != nil {
			targets = append(targets, *p)
		}
	}
	targets = append(targets, art.Skills...)
	for _, p := range targets {
		if _, err := safePath(root, p); err != nil {
			return nil, err
		}
	}

	var artifacts []Artifact
	add := func(list []Artifact, err error) error {
		artifacts = append(artifacts, list...)
		return err
	}
	if gj != nil {
		if err := add(sourceArtifacts(root, gj.Sources)); err != nil {
			return nil, err
		}
		rel, err := artifactFromFile(root, KindRelationships, gj.Relationships)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, rel)
		for _, spec := range gj.Specs {
			a, err := artifactFromFile(root, KindSpec, spec)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, a)
		}
		if err := add(directoryArtifacts(root, KindSavedQuery, gj.SavedQueries)); err != nil {
			return nil, err
		}
	}
	for _, d := range []struct {
		kind ArtifactKind
		path *string
	}{{KindMetric, art.Metrics}, {KindWorkflow, art.Workflows}, {KindWatcher, art.Watchers}, {KindAction, art.Actions}, {KindPolicy, art.Policies}} {
		if err := add(directoryArtifacts(root, d.kind, d.path)); err != nil {
			return nil, err
		}
	}
	for _, s := range art.Skills {
		a, err := skillArtifact(root, s)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, a)
	}

	identities, targetsSeen := map[string]bool{}, map[string]bool{}
	for _, a := range artifacts {
		id, target := string(a.Kind)+":"+a.Key, string(a.Kind)+":"+a.TargetRef
		if identities[id] {
			return nil, fmt.Errorf("duplicate artifact key: %s", id)
		}
		if targetsSeen[target] {
			return nil, fmt.Errorf("duplicate artifact target: %s", target)
		}
		identities[id], targetsSeen[target] = true, true
	}
	if err := validateReferences(artifacts); err != nil {
		return nil, err
	}
	slices.SortStableFunc(artifacts, func(a, b Artifact) int {
		return localeCompare(string(a.Kind)+":"+a.Key, string(b.Kind)+":"+b.Key)
	})
	manifestHash, err := CanonicalHash(manifest.Normalized())
	if err != nil {
		return nil, err
	}
	summary := make([]any, len(artifacts))
	for i, a := range artifacts {
		summary[i] = map[string]any{"kind": string(a.Kind), "key": a.Key, "targetRef": a.TargetRef, "hash": a.Hash}
	}
	bundleHash, err := CanonicalHash(map[string]any{"manifest": manifestHash, "artifacts": summary})
	if err != nil {
		return nil, err
	}
	return &Bundle{Root: root, Manifest: manifest, ManifestHash: manifestHash, BundleHash: bundleHash, Artifacts: artifacts}, nil
}
