package webfront

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"gopkg.in/yaml.v3"
)

const (
	maxOpenAPISpecBytes = 2 << 20
	openAPIMaxRedirects = 3
	openAPIFetchTimeout = 15 * time.Second
	openAPIMaxAliases   = 50
)

var (
	openAPIVersion   = regexp.MustCompile(`^3\.\d+(?:\.\d+)?(?:[-+].*)?$`)
	openAPIExtension = regexp.MustCompile(`(?i)\.(?:ya?ml|json)$`)
	openAPIMethods   = map[string]bool{"get": true, "put": true, "post": true, "delete": true, "options": true, "head": true, "patch": true, "trace": true}
	openAPIBlocked   = func() []netip.Prefix {
		var out []netip.Prefix
		for _, p := range []string{
			"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
			"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
			"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
			"::/128", "::1/128", "::ffff:0:0/96", "fc00::/7", "fe80::/10", "ff00::/8", "2001:db8::/32",
		} {
			out = append(out, netip.MustParsePrefix(p))
		}
		return out
	}()

	openAPILookup  = net.DefaultResolver.LookupNetIP
	openAPIPublic  = isPublicAddress
	openAPIRootCAs *x509.CertPool
)

type openAPISpecSummary struct {
	Content                string
	ChecksumSha256         string
	Title                  string
	Version                *string
	BaseURL                string
	ServerURLs             []string
	AuthSchemes            []string
	Warnings               []string
	OperationCount         int
	ReadOperationCount     int
	MutatingOperationCount int
}

func isPublicAddress(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, p := range openAPIBlocked {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// whatwgHref serialises an absolute URL the way WHATWG `URL.toString()` does
// for the cases an import meets: lower-case host, no default port, "/" path.
func whatwgHref(u *url.URL) string {
	c := *u
	c.Scheme = strings.ToLower(c.Scheme)
	host, port := strings.ToLower(c.Hostname()), c.Port()
	if (c.Scheme == "https" && port == "443") || (c.Scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	c.Host = host
	if c.Path == "" && c.RawPath == "" {
		c.Path = "/"
	}
	return c.String()
}

func parseAbsoluteURL(value string) (*url.URL, bool) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Opaque != "" || u.Host == "" {
		return nil, false
	}
	return u, true
}

func hasCredentials(u *url.URL) bool {
	if u.User == nil {
		return false
	}
	pw, _ := u.User.Password()
	return u.User.Username() != "" || pw != ""
}

func parseImportURL(value string) (*url.URL, error) {
	u, ok := parseAbsoluteURL(value)
	if !ok {
		return nil, errors.New("OpenAPI URL must be a valid absolute URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, errors.New("OpenAPI URL must use HTTPS")
	}
	if hasCredentials(u) {
		return nil, errors.New("OpenAPI URL must not include credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("OpenAPI URL must not include a query string or fragment; upload private or signed specifications instead")
	}
	host, err := idna.Lookup.ToASCII(u.Hostname())
	if err != nil {
		return nil, errors.New("OpenAPI URL must be a valid absolute URL")
	}
	if p := u.Port(); p != "" {
		u.Host = net.JoinHostPort(host, p)
	} else if u.Host = host; strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	u.User = nil
	parsed, _ := url.Parse(whatwgHref(u))
	return parsed, nil
}

func resolvePublicHost(ctx context.Context, host string) ([]netip.Addr, error) {
	addrs, err := openAPILookup(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, errors.New("OpenAPI URL resolves to a non-public network address")
	}
	for _, a := range addrs {
		if !openAPIPublic(a) {
			return nil, errors.New("OpenAPI URL resolves to a non-public network address")
		}
	}
	return addrs, nil
}

func readOpenAPIURL(ctx context.Context, u *url.URL, redirects int) (string, *url.URL, error) {
	addrs, err := resolvePublicHost(ctx, u.Hostname())
	if err != nil {
		return "", nil, err
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	pinned := net.JoinHostPort(addrs[0].Unmap().String(), port)
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, pinned)
		},
		TLSClientConfig:    &tls.Config{ServerName: u.Hostname(), RootCAs: openAPIRootCAs},
		DisableCompression: true,
		DisableKeepAlives:  true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	reqCtx, cancel := context.WithTimeout(ctx, openAPIFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Accept", "application/yaml, application/json, text/yaml, text/plain;q=0.8")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "OpenNeko-OpenAPI-Importer/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fetchError(reqCtx, err)
	}
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode >= 300 && resp.StatusCode < 400 && loc != "" {
		if redirects >= openAPIMaxRedirects {
			return "", nil, errors.New("OpenAPI URL redirected too many times")
		}
		target, err := u.Parse(loc)
		if err != nil {
			return "", nil, errors.New("OpenAPI URL must be a valid absolute URL")
		}
		next, err := parseImportURL(target.String())
		if err != nil {
			return "", nil, err
		}
		return readOpenAPIURL(ctx, next, redirects+1)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("OpenAPI URL returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxOpenAPISpecBytes {
		return "", nil, errors.New("OpenAPI document exceeds the 2 MB limit")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOpenAPISpecBytes+1))
	if err != nil {
		return "", nil, fetchError(reqCtx, err)
	}
	if len(body) > maxOpenAPISpecBytes {
		return "", nil, errors.New("OpenAPI document exceeds the 2 MB limit")
	}
	return strings.ToValidUTF8(string(body), "�"), u, nil
}

func fetchError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("OpenAPI URL fetch timed out")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

type openAPIImport struct {
	openAPISpecSummary
	SourceURL    string
	OriginalName string
}

func importOpenAPISpecFromURL(ctx context.Context, rawURL string, baseURL *string) (openAPIImport, error) {
	requested, err := parseImportURL(strings.TrimSpace(rawURL))
	if err != nil {
		return openAPIImport{}, err
	}
	body, final, err := readOpenAPIURL(ctx, requested, 0)
	if err != nil {
		return openAPIImport{}, err
	}
	name := path.Base(final.EscapedPath())
	if name == "/" || name == "." {
		name = ""
	}
	if name == "" {
		name = "openapi.yaml"
	}
	source := final.String()
	summary, err := inspectOpenAPISpec(body, name, source, baseURL)
	if err != nil {
		return openAPIImport{}, err
	}
	return openAPIImport{summary, source, name}, nil
}

func yamlTarget(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func yamlString(n *yaml.Node) (string, bool) {
	n = yamlTarget(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return "", false
	}
	switch n.ShortTag() {
	case "!!str", "!!timestamp":
		return n.Value, true
	}
	return "", false
}

func yamlMap(n *yaml.Node) *yaml.Node {
	if n = yamlTarget(n); n != nil && n.Kind == yaml.MappingNode {
		return n
	}
	return nil
}

func yamlGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func parseOpenAPIDocument(content string) (*yaml.Node, error) {
	dec := yaml.NewDecoder(strings.NewReader(content))
	var doc yaml.Node
	err := dec.Decode(&doc)
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err == nil {
		var extra yaml.Node
		if e := dec.Decode(&extra); !errors.Is(e, io.EOF) {
			err = errors.New("Source contains multiple documents; please use YAML.parseAllDocuments()")
		}
	}
	if err == nil {
		err = prepareOpenAPINode(&doc, new(int), map[*yaml.Node]bool{})
	}
	if err != nil {
		return nil, fmt.Errorf("OpenAPI document is not valid YAML or JSON: %s", strings.TrimPrefix(err.Error(), "yaml: "))
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	return doc.Content[0], nil
}

// prepareOpenAPINode drops source styles and comments so the stored copy is
// block YAML, as the Node `stringify` of the parsed object was.
func prepareOpenAPINode(n *yaml.Node, aliases *int, seen map[*yaml.Node]bool) error {
	if n == nil || seen[n] {
		return nil
	}
	seen[n] = true
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	switch n.Kind {
	case yaml.AliasNode:
		if *aliases++; *aliases > openAPIMaxAliases {
			return errors.New("Excessive alias count indicates a resource exhaustion attack")
		}
		return nil
	case yaml.ScalarNode:
		n.Style = 0
		if n.ShortTag() == "!!str" && strings.Contains(n.Value, "\n") {
			n.Style = yaml.LiteralStyle
		}
		return nil
	case yaml.MappingNode:
		n.Style = 0
		keys := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i]; k.Kind == yaml.ScalarNode {
				if keys[k.Value] {
					return errors.New("Map keys must be unique")
				}
				keys[k.Value] = true
			}
		}
	default:
		n.Style = 0
	}
	for _, c := range n.Content {
		if err := prepareOpenAPINode(c, aliases, seen); err != nil {
			return err
		}
	}
	return nil
}

func assertNoExternalReferences(n *yaml.Node, seen map[*yaml.Node]bool) error {
	n = yamlTarget(n)
	if n == nil || seen[n] {
		return nil
	}
	seen[n] = true
	if n.Kind == yaml.MappingNode {
		if ref, ok := yamlString(yamlGet(n, "$ref")); ok && !strings.HasPrefix(ref, "#/") {
			if r := []rune(ref); len(r) > 120 {
				ref = string(r[:120])
			}
			return fmt.Errorf("external OpenAPI references are not supported (%s)", ref)
		}
	}
	for i, c := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 0 {
			continue
		}
		if err := assertNoExternalReferences(c, seen); err != nil {
			return err
		}
	}
	return nil
}

func normalizeBaseURL(value string) (string, error) {
	u, ok := parseAbsoluteURL(value)
	if !ok {
		return "", errors.New("OpenAPI server URL must be an absolute HTTP or HTTPS URL")
	}
	if s := strings.ToLower(u.Scheme); s != "https" && s != "http" {
		return "", errors.New("OpenAPI server URL must use HTTP or HTTPS")
	}
	if hasCredentials(u) {
		return "", errors.New("OpenAPI server URL must not include credentials")
	}
	u.User = nil
	u.Fragment, u.RawFragment = "", ""
	return strings.TrimSuffix(whatwgHref(u), "/"), nil
}

func inspectOpenAPISpec(content, originalName, sourceURL string, baseURLOverride *string) (openAPISpecSummary, error) {
	var s openAPISpecSummary
	if len(content) == 0 {
		return s, errors.New("OpenAPI document is empty")
	}
	if len(content) > maxOpenAPISpecBytes {
		return s, errors.New("OpenAPI document exceeds the 2 MB limit")
	}
	root, err := parseOpenAPIDocument(content)
	if err != nil {
		return s, err
	}
	doc := yamlMap(root)
	if doc == nil {
		return s, errors.New("OpenAPI document root must be an object")
	}
	version, _ := yamlString(yamlGet(doc, "openapi"))
	if !openAPIVersion.MatchString(strings.TrimSpace(version)) {
		return s, errors.New("only OpenAPI 3.x documents are supported")
	}
	if err := assertNoExternalReferences(doc, map[*yaml.Node]bool{}); err != nil {
		return s, err
	}
	paths := yamlMap(yamlGet(doc, "paths"))
	if paths == nil || len(paths.Content) == 0 {
		return s, errors.New("OpenAPI document must define at least one path")
	}
	for i := 1; i < len(paths.Content); i += 2 {
		item := yamlMap(paths.Content[i])
		if item == nil {
			continue
		}
		for j := 0; j < len(item.Content); j += 2 {
			method := strings.ToLower(item.Content[j].Value)
			if !openAPIMethods[method] {
				continue
			}
			s.OperationCount++
			if method == "get" || method == "head" || method == "options" {
				s.ReadOperationCount++
			} else {
				s.MutatingOperationCount++
			}
		}
	}
	if s.OperationCount == 0 {
		return s, errors.New("OpenAPI document does not define any operations")
	}

	info := yamlMap(yamlGet(doc, "info"))
	if t, ok := yamlString(yamlGet(info, "title")); ok && strings.TrimSpace(t) != "" {
		s.Title = strings.TrimSpace(t)
	} else if t := openAPIExtension.ReplaceAllString(originalName, ""); t != "" {
		s.Title = t
	} else {
		s.Title = "Imported API"
	}
	if v, ok := yamlString(yamlGet(info, "version")); ok && strings.TrimSpace(v) != "" {
		v = strings.TrimSpace(v)
		s.Version = &v
	}

	s.ServerURLs = []string{}
	if servers := yamlTarget(yamlGet(doc, "servers")); servers != nil && servers.Kind == yaml.SequenceNode {
		for _, item := range servers.Content {
			if v, ok := yamlString(yamlGet(yamlMap(item), "url")); ok && v != "" {
				s.ServerURLs = append(s.ServerURLs, v)
			}
		}
	}
	baseURL := ""
	if baseURLOverride != nil {
		baseURL = strings.TrimSpace(*baseURLOverride)
	}
	if baseURL == "" && len(s.ServerURLs) > 0 {
		declared := s.ServerURLs[0]
		if u, ok := parseAbsoluteURL(declared); ok {
			baseURL = whatwgHref(u)
		} else if sourceURL != "" {
			if base, err := url.Parse(sourceURL); err == nil {
				if u, err := base.Parse(declared); err == nil {
					baseURL = whatwgHref(u)
				}
			}
		}
	}
	if baseURL == "" {
		return s, errors.New("OpenAPI document needs an absolute server URL or a Base URL override")
	}
	if s.BaseURL, err = normalizeBaseURL(baseURL); err != nil {
		return s, err
	}
	setOpenAPIServers(doc, s.BaseURL)

	s.AuthSchemes = []string{}
	if schemes := yamlMap(yamlGet(yamlMap(yamlGet(doc, "components")), "securitySchemes")); schemes != nil {
		for i := 0; i+1 < len(schemes.Content); i += 2 {
			name := schemes.Content[i].Value
			if t, ok := yamlString(yamlGet(yamlMap(schemes.Content[i+1]), "type")); ok {
				name = fmt.Sprintf("%s (%s)", name, t)
			}
			s.AuthSchemes = append(s.AuthSchemes, name)
		}
	}
	s.Warnings = []string{}
	if n := s.MutatingOperationCount; n == 1 {
		s.Warnings = append(s.Warnings, "1 operation is mutating")
	} else if n > 1 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%d operations are mutating", n))
	}
	if len(s.AuthSchemes) == 0 {
		s.Warnings = append(s.Warnings, "No authentication scheme is declared")
	}
	if len(s.ServerURLs) > 1 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("The document declares %d servers; the selected Base URL will be used", len(s.ServerURLs)))
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return s, err
	}
	_ = enc.Close()
	s.Content = buf.String()
	sum := sha256.Sum256(buf.Bytes())
	s.ChecksumSha256 = hex.EncodeToString(sum[:])
	return s, nil
}

func setOpenAPIServers(doc *yaml.Node, baseURL string) {
	servers := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{{
		Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "url"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: baseURL},
		},
	}}}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "servers" {
			doc.Content[i+1] = servers
			return
		}
	}
	doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "servers"}, servers)
}

type openAPISpecAsset struct {
	ID                     string   `json:"id"`
	OriginalName           string   `json:"originalName"`
	SourceType             string   `json:"sourceType"`
	Title                  string   `json:"title"`
	Version                *string  `json:"version"`
	BaseURL                string   `json:"baseUrl"`
	OperationCount         int      `json:"operationCount"`
	ReadOperationCount     int      `json:"readOperationCount"`
	MutatingOperationCount int      `json:"mutatingOperationCount"`
	AuthSchemes            []string `json:"authSchemes"`
	Warnings               []string `json:"warnings"`
	ChecksumSha256         string   `json:"checksumSha256"`
	Status                 string   `json:"status"`
	SourceName             *string  `json:"sourceName"`
	CreatedAt              string   `json:"createdAt"`
}

const openAPIAssetColumns = `id::text, original_name, source_type, title, version, base_url,
	operation_count, read_operation_count, mutating_operation_count, auth_schemes, warnings,
	checksum_sha256, status, source_name, created_at`

func scanOpenAPIAsset(row interface{ Scan(...any) error }) (openAPISpecAsset, error) {
	var v openAPISpecAsset
	var created time.Time
	err := row.Scan(&v.ID, &v.OriginalName, &v.SourceType, &v.Title, &v.Version, &v.BaseURL,
		&v.OperationCount, &v.ReadOperationCount, &v.MutatingOperationCount, &v.AuthSchemes, &v.Warnings,
		&v.ChecksumSha256, &v.Status, &v.SourceName, &created)
	if v.SourceType != "url" {
		v.SourceType = "upload"
	}
	if v.AuthSchemes == nil {
		v.AuthSchemes = []string{}
	}
	if v.Warnings == nil {
		v.Warnings = []string{}
	}
	v.CreatedAt = jsTime(created)
	return v, err
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func capList(list []string, n int) []string {
	if len(list) > n {
		return list[:n]
	}
	return list
}

func (a *API) storeOpenAPIAsset(ctx context.Context, act actor, sourceType, originalName string, sourceURL *string, s openAPISpecSummary) (openAPISpecAsset, error) {
	var createdBy *string
	if act.UserID != "" {
		createdBy = &act.UserID
	}
	var version *string
	if s.Version != nil {
		v := cutRunes(*s.Version, 200)
		version = &v
	}
	return scanOpenAPIAsset(a.DB.QueryRow(ctx, `insert into openapi_spec_asset (org_id, created_by_user_id,
		source_type, original_name, source_url, content, checksum_sha256, document_format, title, version,
		base_url, server_urls, auth_schemes, warnings, operation_count, read_operation_count, mutating_operation_count)
		values ($1, $2, $3, $4, $5, $6, $7, 'yaml', $8, $9, $10, $11, $12, $13, $14, $15, $16)
		returning `+openAPIAssetColumns,
		act.OrgID, createdBy, sourceType, cutRunes(originalName, 500), sourceURL, s.Content, s.ChecksumSha256,
		cutRunes(s.Title, 500), version, s.BaseURL, capList(s.ServerURLs, 20), capList(s.AuthSchemes, 50),
		capList(s.Warnings, 20), s.OperationCount, s.ReadOperationCount, s.MutatingOperationCount))
}

func (a *API) openAPISpecAdmin(w http.ResponseWriter, r *http.Request) (actor, bool) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return act, false
	}
	if !act.Admin {
		sendError(w, http.StatusNotFound, "not found")
		return act, false
	}
	if on, err := a.sourceConfigEnabled(r, act.OrgID); err != nil {
		a.fail(w, r, err)
		return act, false
	} else if !on {
		sendError(w, http.StatusNotFound, "not found")
		return act, false
	}
	return act, true
}

func (a *API) openAPISpecList(w http.ResponseWriter, r *http.Request) {
	act, ok := a.openAPISpecAdmin(w, r)
	if !ok {
		return
	}
	rows, err := a.DB.Query(r.Context(), `select `+openAPIAssetColumns+` from openapi_spec_asset
		where org_id = $1 order by created_at desc limit 20`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rows.Close()
	assets := []openAPISpecAsset{}
	for rows.Next() {
		v, err := scanOpenAPIAsset(rows)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		assets = append(assets, v)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"assets": assets})
}

func cleanBaseURL(v any) *string {
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		s = strings.TrimSpace(s)
		return &s
	}
	return nil
}

func (a *API) openAPISpecImport(w http.ResponseWriter, r *http.Request) {
	act, ok := a.openAPISpecAdmin(w, r)
	if !ok {
		return
	}
	const bodyLimit = maxOpenAPISpecBytes + 1<<20
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	var tooLarge *http.MaxBytesError

	if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		form, err := readUploadForm(r)
		if form != nil && form.File != nil {
			defer form.RemoveAll()
		}
		if errors.As(err, &tooLarge) {
			sendError(w, http.StatusRequestEntityTooLarge, "OpenAPI upload is too large")
			return
		}
		if err != nil {
			if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType == "multipart/form-data" {
				err = errors.New("Failed to parse body as FormData.")
			}
			sendError(w, http.StatusBadRequest, err.Error())
			return
		}
		files := form.File["file"]
		if len(files) == 0 {
			sendError(w, http.StatusBadRequest, "an OpenAPI YAML or JSON file is required")
			return
		}
		h := files[0]
		if h.Size == 0 || h.Size > maxOpenAPISpecBytes {
			sendError(w, http.StatusBadRequest, "OpenAPI file must be between 1 byte and 2 MB")
			return
		}
		name := partFilename(h)
		if !openAPIExtension.MatchString(name) {
			sendError(w, http.StatusBadRequest, "OpenAPI file must have a .yaml, .yml, or .json extension")
			return
		}
		f, err := h.Open()
		if err != nil {
			a.fail(w, r, err)
			return
		}
		raw, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			a.fail(w, r, err)
			return
		}
		raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
		var baseURL *string
		if v := form.Value["baseUrl"]; len(v) > 0 {
			baseURL = cleanBaseURL(v[0])
		}
		summary, err := inspectOpenAPISpec(strings.ToValidUTF8(string(raw), "�"), name, "", baseURL)
		if err != nil {
			sendError(w, http.StatusBadRequest, err.Error())
			return
		}
		asset, err := a.storeOpenAPIAsset(r.Context(), act, "upload", name, nil, summary)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		sendJSON(w, http.StatusCreated, map[string]any{"asset": asset})
		return
	}

	raw, err := io.ReadAll(r.Body)
	if errors.As(err, &tooLarge) {
		sendError(w, http.StatusRequestEntityTooLarge, "OpenAPI upload is too large")
		return
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	rawURL, _ := body["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		sendError(w, http.StatusBadRequest, "url is required for a hosted OpenAPI import")
		return
	}
	imported, err := importOpenAPISpecFromURL(r.Context(), rawURL, cleanBaseURL(body["baseUrl"]))
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	asset, err := a.storeOpenAPIAsset(r.Context(), act, "url", imported.OriginalName, &imported.SourceURL, imported.openAPISpecSummary)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusCreated, map[string]any{"asset": asset})
}
