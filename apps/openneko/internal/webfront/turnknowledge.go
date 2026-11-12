package webfront

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed knowledgeindex.txt
var legacyKnowledgeIndex string

//go:embed knowledgeindex_agentic.txt
var agenticKnowledgeIndex string

const knowledgeSnapshotName = ".knowledge-snapshot.json"

var (
	knowledgeFetchTimeout = 10 * time.Second
	discoveryRetryDelay   = time.Second
	knowledgeSections     = [][2]string{
		{"tables?limit=500", "tables.json"}, {"namespaces", "namespaces.json"},
		{"insights", "insights.json"}, {"syntax", "syntax.json"},
	}
	knowledgeRefreshMu      sync.Mutex
	knowledgeRefreshPending = map[string]*knowledgeRefreshCall{}
	mcpPathSuffix           = regexp.MustCompile(`/mcp/?$`)
	jsLineChars             = `[^\n\r\x{2028}\x{2029}]`
	relationshipID          = regexp.MustCompile(`^relationship:(` + jsLineChars + `+?)->(` + jsLineChars + `+)$`)
	relationshipPrefix      = regexp.MustCompile(`^[^:]+:`)
)

type knowledgeFile struct {
	File  string `json:"file"`
	Bytes int    `json:"bytes"`
}

// knowledgeResult is PrefetchKnowledgeResult in packages/llm/src/knowledge-pack.ts.
type knowledgeResult struct {
	OK    bool            `json:"ok"`
	Files []knowledgeFile `json:"files"`
	Error string          `json:"error,omitempty"`
	Mode  string          `json:"-"`
}

func knowledgeFailed(err error) knowledgeResult {
	return knowledgeResult{Files: []knowledgeFile{}, Error: err.Error()}
}

type knowledgeSnapshot struct {
	Source, Revision string
	Files            map[string]string
}

type knowledgeRefreshCall struct {
	done   chan struct{}
	result knowledgeResult
}

// readKnowledgeSnapshot is readKnowledgeSnapshot() in packages/llm/src/knowledge-cache.ts.
func readKnowledgeSnapshot(root string) *knowledgeSnapshot {
	raw, err := os.ReadFile(filepath.Join(root, knowledgeSnapshotName))
	if err != nil {
		return nil
	}
	var snap struct {
		Format   int                        `json:"format"`
		Source   *string                    `json:"source"`
		Revision *string                    `json:"revision"`
		Files    map[string]json.RawMessage `json:"files"`
	}
	if json.Unmarshal(raw, &snap) != nil || snap.Format != 1 || snap.Source == nil || snap.Revision == nil {
		return nil
	}
	out := &knowledgeSnapshot{Source: *snap.Source, Revision: *snap.Revision, Files: map[string]string{}}
	for _, name := range knowledgeSnapshotFiles {
		var text string
		if json.Unmarshal(snap.Files[name], &text) != nil || strings.HasSuffix(name, ".json") && !json.Valid([]byte(text)) {
			return nil
		}
		out.Files[name] = text
	}
	return out
}

func publishKnowledgeSnapshot(root string, snap knowledgeSnapshot, builtAt *time.Time) error {
	files := &jsObj{}
	for _, name := range knowledgeSnapshotFiles {
		files.set(name, snap.Files[name])
	}
	body := &jsObj{}
	body.set("format", 1)
	body.set("source", snap.Source)
	body.set("revision", snap.Revision)
	body.set("files", files)
	if builtAt != nil {
		body.set("builtAt", float64(builtAt.UnixMilli()))
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(root, ".publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	path := filepath.Join(temp, knowledgeSnapshotName)
	if err := os.WriteFile(path, []byte(jsStringify(body, false)), 0o644); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(root, knowledgeSnapshotName))
}

// clearKnowledgeSnapshot never lets readers use the old source's files when
// its replacement cannot be loaded.
func clearKnowledgeSnapshot(root, source, mode string) error {
	files := map[string]string{}
	for _, name := range knowledgeSnapshotFiles {
		switch name {
		case "mode.json":
			files[name] = jsStringify(modeObject(mode), false)
		case "INDEX.md":
			files[name] = ""
		default:
			files[name] = "{}"
		}
	}
	return publishKnowledgeSnapshot(root, knowledgeSnapshot{Source: source, Files: files}, nil)
}

func modeObject(mode string) *jsObj {
	o := &jsObj{}
	o.set("mode", mode)
	return o
}

func snapshotResult(s *knowledgeSnapshot) knowledgeResult {
	r := knowledgeResult{OK: true}
	for _, name := range knowledgeSnapshotFiles[:4] {
		r.Files = append(r.Files, knowledgeFile{File: name, Bytes: len(s.Files[name])})
	}
	return r
}

// refreshKnowledgeSnapshot is refreshKnowledgeSnapshot() in
// packages/llm/src/knowledge-cache.ts: one whole snapshot, replaced only
// after the same catalog revision is read before and after the build.
func refreshKnowledgeSnapshot(root, source, mode string, refresh bool,
	revision func() (string, error), build func() (map[string]string, error)) knowledgeResult {
	cached := readKnowledgeSnapshot(root)
	if cached != nil && cached.Source == source && cached.Revision != "" && !refresh {
		return snapshotResult(cached)
	}
	key := jsStringify([]any{root, source}, false)
	knowledgeRefreshMu.Lock()
	if call, ok := knowledgeRefreshPending[key]; ok {
		knowledgeRefreshMu.Unlock()
		<-call.done
		return call.result
	}
	call := &knowledgeRefreshCall{done: make(chan struct{})}
	knowledgeRefreshPending[key] = call
	knowledgeRefreshMu.Unlock()
	defer func() {
		knowledgeRefreshMu.Lock()
		delete(knowledgeRefreshPending, key)
		knowledgeRefreshMu.Unlock()
		close(call.done)
	}()
	call.result = refreshKnowledgeOnce(root, source, mode, cached, revision, build)
	return call.result
}

func refreshKnowledgeOnce(root, source, mode string, cached *knowledgeSnapshot,
	revision func() (string, error), build func() (map[string]string, error)) knowledgeResult {
	sameSource := cached != nil && cached.Source == source
	if !sameSource {
		if err := clearKnowledgeSnapshot(root, source, mode); err != nil {
			return knowledgeFailed(err)
		}
	}
	for range 2 {
		rev, err := revision()
		if err != nil {
			return knowledgeFailed(err)
		}
		if rev == "" {
			return knowledgeFailed(errors.New("GraphJin returned no catalog revision"))
		}
		if sameSource && cached.Revision == rev {
			return snapshotResult(cached)
		}
		files, err := build()
		if err != nil {
			return knowledgeFailed(err)
		}
		for _, name := range knowledgeSnapshotFiles {
			text, ok := files[name]
			if !ok {
				return knowledgeFailed(fmt.Errorf("knowledge build wrote no %s", name))
			}
			if strings.HasSuffix(name, ".json") && !json.Valid([]byte(text)) {
				return knowledgeFailed(fmt.Errorf("knowledge build wrote invalid %s", name))
			}
		}
		after, err := revision()
		if err != nil {
			return knowledgeFailed(err)
		}
		if after != rev {
			continue
		}
		snap := knowledgeSnapshot{Source: source, Revision: rev, Files: files}
		now := time.Now()
		if err := publishKnowledgeSnapshot(root, snap, &now); err != nil {
			return knowledgeFailed(err)
		}
		return snapshotResult(&snap)
	}
	return knowledgeFailed(errors.New("GraphJin catalog changed during knowledge refresh; retrying on the next sweep"))
}

// prefetchKnowledge is prefetchKnowledgeForOrg() in
// packages/llm/src/knowledge-pack.ts: auth_mode jwt builds the slim catalog
// pack with a service token; any other mode keeps the discovery dumps.
func (a *API) prefetchKnowledge(ctx context.Context, orgID, destDir string, refresh bool) knowledgeResult {
	var id, authMode, graphqlURL string
	var mcpURL *string
	err := a.DB.QueryRow(ctx, `select id::text, auth_mode, graphql_url, mcp_url from data_source
		where org_id = $1 order by is_default desc, created_at limit 1`, orgID).Scan(&id, &authMode, &graphqlURL, &mcpURL)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return knowledgeFailed(err)
	}
	mcp := ""
	if mcpURL != nil {
		mcp = *mcpURL
	}
	if mcp == "" && graphqlURL == "" {
		if err := clearKnowledgeSnapshot(destDir, "none", "legacy"); err != nil {
			return knowledgeFailed(err)
		}
		r := knowledgeFailed(errors.New("no data source configured"))
		r.Mode = "legacy"
		return r
	}
	if authMode == "jwt" {
		endpoint := graphqlURL
		if endpoint == "" {
			endpoint = mcpPathSuffix.ReplaceAllString(mcp, "/graphql")
		}
		source := jsStringify([]any{orgID, id, endpoint, authMode, "service", 2}, false)
		r := refreshKnowledgeSnapshot(destDir, source, "agentic", refresh,
			func() (string, error) { return a.catalogRevision(ctx, orgID, endpoint) },
			func() (map[string]string, error) {
				token, err := a.graphjinToken(orgID, "service", 300*time.Second)
				if err != nil {
					return nil, err
				}
				files, _, err := buildAgenticKnowledge(ctx, endpoint, token)
				return files, err
			})
		r.Mode = "agentic"
		return r
	}
	if readKnowledgeSnapshot(destDir) != nil {
		if err := clearKnowledgeSnapshot(destDir, "legacy", "legacy"); err != nil {
			return knowledgeFailed(err)
		}
	}
	discovery := graphqlURL
	if mcpURL != nil {
		discovery = mcp
	}
	r := prefetchLegacyKnowledge(ctx, mcpPathSuffix.ReplaceAllString(discovery, "/discovery"), destDir)
	if r.OK {
		if err := os.Remove(filepath.Join(destDir, knowledgeSnapshotName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return knowledgeFailed(err)
		}
	}
	r.Mode = "legacy"
	return r
}

// catalogRevision reads the revision with an admin token; the pack itself
// is built with a service token.
func (a *API) catalogRevision(ctx context.Context, orgID, endpoint string) (string, error) {
	token, err := a.graphjinToken(orgID, "admin", 300*time.Second)
	if err != nil {
		return "", err
	}
	status, raw, err := knowledgePost(ctx, endpoint, token, `query { gj_config(id: "current") { catalog_revision } }`)
	if err != nil {
		return "", err
	}
	var body struct {
		Data struct {
			Config json.RawMessage `json:"gj_config"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	if status < 200 || status > 299 || len(body.Errors) > 0 {
		return "", errors.New("GraphJin catalog revision unavailable")
	}
	var row struct {
		Revision any `json:"catalog_revision"`
	}
	var rows []json.RawMessage
	if json.Unmarshal(body.Data.Config, &rows) == nil {
		if len(rows) > 0 {
			_ = json.Unmarshal(rows[0], &row)
		}
	} else {
		_ = json.Unmarshal(body.Data.Config, &row)
	}
	rev, ok := row.Revision.(string)
	if !ok {
		return "", errors.New("GraphJin returned no catalog revision")
	}
	return rev, nil
}

func knowledgePost(ctx context.Context, endpoint, token, query string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, knowledgeFetchTimeout)
	defer cancel()
	payload, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

// prefetchLegacyKnowledge is prefetchKnowledgePack() in packages/llm/src/knowledge-pack.ts.
func prefetchLegacyKnowledge(ctx context.Context, discoveryURL, destDir string) knowledgeResult {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return knowledgeFailed(err)
	}
	texts := make([]string, len(knowledgeSections))
	errs := make([]error, len(knowledgeSections))
	var wg sync.WaitGroup
	for i, s := range knowledgeSections {
		wg.Go(func() {
			texts[i], errs[i] = fetchDiscoverySection(ctx, discoveryURL, s[0], strings.TrimSuffix(s[1], ".json"))
		})
	}
	wg.Wait()
	if err := firstError(errs); err != nil {
		return knowledgeFailed(err)
	}
	r := knowledgeResult{OK: true}
	for i, s := range knowledgeSections {
		if err := os.WriteFile(filepath.Join(destDir, s[1]), []byte(texts[i]), 0o644); err != nil {
			return knowledgeFailed(err)
		}
		r.Files = append(r.Files, knowledgeFile{File: s[1], Bytes: jsLength(texts[i])})
	}
	if err := os.WriteFile(filepath.Join(destDir, "INDEX.md"), []byte(legacyKnowledgeIndex), 0o644); err != nil {
		return knowledgeFailed(err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "mode.json"), []byte(jsStringify(modeObject("legacy"), false)), 0o644); err != nil {
		return knowledgeFailed(err)
	}
	return r
}

func firstError(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func fetchDiscoverySection(ctx context.Context, discoveryURL, section, label string) (string, error) {
	url := strings.TrimRight(discoveryURL, "/") + "/" + section
	const attempts = 3
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		text, err := fetchDiscoveryOnce(ctx, url)
		if err == nil {
			return text, nil
		}
		last = err
		if attempt < attempts {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(discoveryRetryDelay << (attempt - 1)):
			}
		}
	}
	return "", fmt.Errorf("discovery %s fetch failed after %d attempts: %v", label, attempts, last)
}

func fetchDiscoveryOnce(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, knowledgeFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", errors.New(resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	v, err := jsParse(bytes.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return jsStringifyIndent(v, "  "), nil
}

func catalogRows(ctx context.Context, endpoint, token, query string) ([]any, error) {
	status, raw, err := knowledgePost(ctx, endpoint, token, query)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("gj_catalog HTTP %d", status)
	}
	body, err := jsParse(bytes.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if errs, ok := jsField(body, "errors"); ok {
		if list, ok := errs.([]any); ok && len(list) > 0 {
			messages := make([]string, len(list))
			for i, e := range list {
				m, _ := jsField(e, "message")
				messages[i] = jsValueString(m)
			}
			return nil, fmt.Errorf("gj_catalog query failed: %s", strings.Join(messages, "; "))
		}
	}
	data, _ := jsField(body, "data")
	rows, _ := jsField(data, "gj_catalog")
	switch t := rows.(type) {
	case []any:
		return t, nil
	case *jsObj:
		return []any{t}, nil
	}
	return nil, errors.New("gj_catalog returned no rows")
}

func jsField(v any, key string) (any, bool) {
	o, ok := v.(*jsObj)
	if !ok || o == nil {
		return nil, false
	}
	val, ok := o.vals[key]
	return val, ok
}

func copyFields(dst *jsObj, src any, keys ...string) {
	for _, k := range keys {
		if v, ok := jsField(src, k); ok {
			dst.set(k, v)
		}
	}
}

// parseExamples is JSON.parse(examples_json ?? "[]") as an array; text that
// does not parse gives no examples.
func parseExamples(card any) []any {
	raw, _ := jsField(card, "examples_json")
	text := "[]"
	if raw != nil {
		text = jsValueString(raw)
	}
	v, err := jsParse([]byte(text))
	if err != nil {
		return []any{}
	}
	if list, ok := v.([]any); ok {
		return list
	}
	return []any{v}
}

func cardQuery(id any, fields string) string {
	return fmt.Sprintf(`query { gj_catalog(id: "%s") { %s } }`, jsValueString(id), fields)
}

func listQuery(where string, limit int, fields string) string {
	return fmt.Sprintf(`query { gj_catalog(where: %s, limit: %d) { %s } }`, where, limit, fields)
}

func kindWhere(kind string) string { return fmt.Sprintf(`{ kind: { eq: "%s" } }`, kind) }

// catalogCards fetches one card per id at once; a card that fails gives no rows.
func catalogCards(ctx context.Context, endpoint, token string, ids []any, fields string) []any {
	found := make([][]any, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() { found[i], _ = catalogRows(ctx, endpoint, token, cardQuery(id, fields)) })
	}
	wg.Wait()
	return slices.Concat(found...)
}

type joinPath struct{ fromLabel, fromTable, toLabel, toTable string }

func relationshipSide(s string) (label, table string) {
	label = relationshipPrefix.ReplaceAllString(s, "")
	parts := strings.Split(label, ".")
	if len(parts) >= 2 {
		return label, parts[len(parts)-2]
	}
	return label, label
}

// orderedScores is a JavaScript Map of number by key: keys keep their first place.
type orderedScores struct {
	keys []string
	vals map[string]int
}

func (s *orderedScores) add(k string, n int) {
	if s.vals == nil {
		s.vals = map[string]int{}
	}
	if _, ok := s.vals[k]; !ok {
		s.keys = append(s.keys, k)
	}
	s.vals[k] += n
}

// buildAgenticKnowledge is prefetchAgenticKnowledgePack() in
// packages/llm/src/knowledge-pack.ts. It returns the six pack files and the
// result's file list.
func buildAgenticKnowledge(ctx context.Context, endpoint, token string) (map[string]string, []knowledgeFile, error) {
	queries := []string{
		listQuery(kindWhere("table"), 500, "id name summary"),
		listQuery(kindWhere("database"), 50, "id name summary"),
		listQuery(kindWhere("api_operation"), 500, "id name summary"),
		listQuery(kindWhere("help"), 50, "id name summary"),
		cardQuery("help:query", "id name summary details_json examples_json"),
		cardQuery("help:filters", "id name summary details_json examples_json"),
		listQuery(kindWhere("relationship"), 1000, "id name summary"),
		listQuery(`{ kind: { in: ["query_pattern", "directive", "operator_set"] } }`, 50, "id kind name summary"),
	}
	results := make([][]any, len(queries))
	errs := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Go(func() { results[i], errs[i] = catalogRows(ctx, endpoint, token, q) })
	}
	wg.Wait()
	if errs[7] != nil {
		results[7], errs[7] = []any{}, nil
	}
	if err := firstError(errs); err != nil {
		return nil, nil, err
	}
	tables, databases, apiOperations, helpIndex := results[0], results[1], results[2], results[3]
	helpDetails := slices.Concat(results[4], results[5])
	relationships, languageIndex := results[6], results[7]

	kindRank := map[string]int{"query_pattern": 0, "operator_set": 1, "directive": 2}
	rank := func(c any) int {
		k, _ := jsField(c, "kind")
		if r, ok := kindRank[jsValueString(k)]; ok {
			return r
		}
		return 9
	}
	var language []any
	for _, c := range languageIndex {
		if id, _ := jsField(c, "id"); jsTruthy(id) {
			language = append(language, c)
		}
	}
	slices.SortStableFunc(language, func(x, y any) int { return rank(x) - rank(y) })
	var languageIDs []any
	for _, c := range language[:min(len(language), 24)] {
		id, _ := jsField(c, "id")
		languageIDs = append(languageIDs, id)
	}
	const patternBudget = 4000
	patternBytes := 0
	patterns := []any{}
	for _, c := range catalogCards(ctx, endpoint, token, languageIDs, "id kind name summary examples_json") {
		var examples []any
		for _, e := range jsArrayHead(parseExamples(c), 2) {
			if jsTruthy(e) {
				examples = append(examples, e)
			}
		}
		if kind, _ := jsField(c, "kind"); len(examples) == 0 && kind != "query_pattern" {
			continue
		}
		entry := &jsObj{}
		copyFields(entry, c, "name", "summary")
		entry.set("examples", append([]any{}, examples...))
		size := jsLength(jsStringify(entry, false))
		if patternBytes+size > patternBudget {
			continue
		}
		patternBytes += size
		patterns = append(patterns, entry)
	}

	var paths []joinPath
	for _, rel := range relationships {
		id, _ := jsField(rel, "id")
		m := relationshipID.FindStringSubmatch(jsValueString(id))
		if m == nil {
			continue
		}
		var p joinPath
		p.fromLabel, p.fromTable = relationshipSide(m[1])
		p.toLabel, p.toTable = relationshipSide(m[2])
		paths = append(paths, p)
	}
	idByTable := map[string]any{}
	for _, t := range tables {
		id, _ := jsField(t, "id")
		name, _ := jsField(t, "name")
		if s, ok := name.(string); ok && jsTruthy(id) && s != "" {
			idByTable[s] = id
		}
	}
	idKey := func(table string) (string, bool) {
		id, ok := idByTable[table]
		if !ok {
			return "", false
		}
		return jsStringify(id, false), true
	}
	idOf := map[string]any{}
	for _, id := range idByTable {
		idOf[jsStringify(id, false)] = id
	}
	var hubScores orderedScores
	fkOut := map[string]int{}
	for _, p := range paths {
		if k, ok := idKey(p.fromTable); ok {
			hubScores.add(k, 3)
		}
		if k, ok := idKey(p.toTable); ok {
			hubScores.add(k, 1)
		}
		fkOut[p.fromTable]++
	}
	neighbor := map[string]int{}
	for _, p := range paths {
		from, fromOK := idKey(p.fromTable)
		to, toOK := idKey(p.toTable)
		if fromOK && toOK {
			neighbor[from] += hubScores.vals[to]
			neighbor[to] += hubScores.vals[from]
		}
	}
	hubKeys := slices.Clone(hubScores.keys)
	slices.SortStableFunc(hubKeys, func(x, y string) int {
		if d := hubScores.vals[y] - hubScores.vals[x]; d != 0 {
			return d
		}
		return neighbor[y] - neighbor[x]
	})
	hubKeys = hubKeys[:min(len(hubKeys), 10)]
	hubNames := map[string]bool{}
	for name := range idByTable {
		k, _ := idKey(name)
		if slices.Contains(hubKeys, k) {
			hubNames[name] = true
		}
	}
	touched := map[string]map[string]bool{}
	for _, p := range paths {
		for _, pair := range [][2]string{{p.fromTable, p.toTable}, {p.toTable, p.fromTable}} {
			if hubNames[pair[1]] {
				if touched[pair[0]] == nil {
					touched[pair[0]] = map[string]bool{}
				}
				touched[pair[0]][pair[1]] = true
			}
		}
	}
	linkScore := func(t string) int { return len(touched[t])*4 + fkOut[t] }
	hubIDs := make([]any, len(hubKeys))
	for i, k := range hubKeys {
		hubIDs[i] = idOf[k]
	}
	hubTables := []any{}
	for _, c := range catalogCards(ctx, endpoint, token, hubIDs, "id name summary examples_json") {
		name, _ := jsField(c, "name")
		cardName, named := name.(string)
		var own []joinPath
		for _, p := range paths {
			if named && (p.fromTable == cardName || p.toTable == cardName) {
				own = append(own, p)
			}
		}
		other := func(p joinPath) string {
			if p.fromTable == cardName {
				return p.toTable
			}
			return p.fromTable
		}
		slices.SortStableFunc(own, func(x, y joinPath) int { return linkScore(other(y)) - linkScore(other(x)) })
		joins := []any{}
		for _, p := range own[:min(len(own), 8)] {
			joins = append(joins, p.fromLabel+" -> "+p.toLabel)
		}
		hub := &jsObj{}
		copyFields(hub, c, "id", "name", "summary")
		hub.set("examples", append([]any{}, jsArrayHead(parseExamples(c), 2)...))
		hub.set("join_paths", joins)
		hubTables = append(hubTables, hub)
	}

	object := func(kv ...any) *jsObj {
		o := &jsObj{}
		for i := 0; i < len(kv); i += 2 {
			o.set(kv[i].(string), kv[i+1])
		}
		return o
	}
	files := map[string]string{
		"tables.json":     jsStringifyIndent(object("tables", tables), "  "),
		"namespaces.json": jsStringifyIndent(object("databases", databases), "  "),
		"insights.json": jsStringifyIndent(object(
			"hub_tables", hubTables,
			"api_operations", apiOperations,
			"help_cards", helpIndex,
			"note", `Pull any card's full guidance on demand: gj_catalog(id: "help:<topic>") { details_json examples_json }`,
		), "  "),
		"syntax.json": jsStringifyIndent(object("essentials", helpDetails, "patterns", patterns), " "),
		"INDEX.md":    agenticKnowledgeIndex,
		"mode.json":   jsStringify(modeObject("agentic"), false),
	}
	var written []knowledgeFile
	for _, name := range knowledgeSnapshotFiles[:4] {
		written = append(written, knowledgeFile{File: name, Bytes: jsLength(files[name])})
	}
	return files, written, nil
}

// syncSkillIndex is syncSkillIndex() in packages/llm/src/work/skill-index.ts:
// a new or changed description clears the vector, and the embedding job
// indexes it later.
func (a *API) syncSkillIndex(ctx context.Context, orgID string, skills []installedSkill) error {
	rows, err := a.DB.Query(ctx, `select skill_name, document from skill_embedding where org_id = $1`, orgID)
	if err != nil {
		return err
	}
	known := map[string]string{}
	var existing []string
	for rows.Next() {
		var name, document string
		if err := rows.Scan(&name, &document); err != nil {
			rows.Close()
			return err
		}
		known[name] = document
		existing = append(existing, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	installed := map[string]bool{}
	for _, s := range skills {
		installed[s.Name] = true
		var parts []string
		for _, p := range []string{s.Name, s.Description} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		document := strings.Join(parts, "\n")
		if doc, ok := known[s.Name]; ok && doc == document {
			continue
		}
		if _, err := a.DB.Exec(ctx, `insert into skill_embedding (org_id, skill_name, document, embedding, embedding_model)
			values ($1, $2, $3, null, null)
			on conflict (org_id, skill_name) do update
			set document = excluded.document, embedding = null, embedding_model = null, updated_at = now()`,
			orgID, s.Name, document); err != nil {
			return err
		}
	}
	removed := slices.DeleteFunc(existing, func(name string) bool { return installed[name] })
	if len(removed) == 0 {
		return nil
	}
	_, err = a.DB.Exec(ctx, `delete from skill_embedding where org_id = $1 and skill_name = any($2)`, orgID, removed)
	return err
}
