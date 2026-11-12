package webfront

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/textproto"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const petSpec = `openapi: 3.0.3
info:
  title: " Pet Service "
  version: 1.2.0
servers:
  - url: https://API.example.com/v1/
  - url: https://staging.example.com/v1
paths:
  /pets:
    get:
      operationId: listPets
      description: |
        Lists pets.
        Two lines.
    post:
      operationId: addPet
  /pets/{id}:
    parameters: []
    DELETE: {}
components:
  securitySchemes:
    key: {type: apiKey, in: header, name: X-Key}
    other: {}
`

func TestInspectOpenAPISpec(t *testing.T) {
	s, err := inspectOpenAPISpec(petSpec, "pets.yaml", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%q %v %q %d/%d/%d %q %q %q", s.Title, *s.Version, s.BaseURL, s.OperationCount,
		s.ReadOperationCount, s.MutatingOperationCount, s.ServerURLs, s.AuthSchemes, s.Warnings)
	want := `"Pet Service" 1.2.0 "https://api.example.com/v1" 3/1/2 ["https://API.example.com/v1/" "https://staging.example.com/v1"] ["key (apiKey)" "other"] ["2 operations are mutating" "The document declares 2 servers; the selected Base URL will be used"]`
	if got != want {
		t.Fatalf("summary\n got %s\nwant %s", got, want)
	}
	sum := sha256.Sum256([]byte(s.Content))
	if hex.EncodeToString(sum[:]) != s.ChecksumSha256 {
		t.Fatal("checksum is not over the content")
	}
	var back map[string]any
	if err := yaml.Unmarshal([]byte(s.Content), &back); err != nil {
		t.Fatal(err)
	}
	if servers := fmt.Sprint(back["servers"]); servers != "[map[url:https://api.example.com/v1]]" {
		t.Fatalf("servers: %s", servers)
	}
	if !strings.Contains(s.Content, "description: |") || !strings.HasPrefix(s.Content, "openapi: 3.0.3\n") {
		t.Fatalf("content:\n%s", s.Content)
	}

	js, err := inspectOpenAPISpec(`{"openapi":"3.1.0","info":{"title":"J","version":"true"},"paths":{"/a":{"get":{}}},"x-n":"123"}`, "j.json", "", ptr(" https://j.example.com/ "))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(js.Content, `{"`) || !strings.Contains(js.Content, `x-n: "123"`) || !strings.Contains(js.Content, "servers:\n  - url: https://j.example.com\n") {
		t.Fatalf("json content:\n%s", js.Content)
	}
	if *js.Version != "true" || js.Warnings[0] != "No authentication scheme is declared" {
		t.Fatalf("json summary: %+v", js)
	}

	rel, err := inspectOpenAPISpec("openapi: 3.0.0\nservers: [{url: /v2}]\npaths: {/a: {head: {}}}\n", "spec.yml", "https://docs.example.com/api/spec.yml", nil)
	if err != nil || rel.BaseURL != "https://docs.example.com/v2" || rel.Title != "spec" || rel.Version != nil {
		t.Fatalf("relative server: %v %+v", err, rel)
	}

	for _, c := range []struct{ name, content, base, want string }{
		{"empty", "", "", "OpenAPI document is empty"},
		{"big", strings.Repeat("x", maxOpenAPISpecBytes+1), "", "OpenAPI document exceeds the 2 MB limit"},
		{"bad yaml", "a: [", "", "OpenAPI document is not valid YAML or JSON: "},
		{"two docs", "a: 1\n---\nb: 2\n", "", "OpenAPI document is not valid YAML or JSON: Source contains multiple documents"},
		{"duplicate key", "openapi: 3.0.0\nopenapi: 3.0.1\n", "", "OpenAPI document is not valid YAML or JSON: Map keys must be unique"},
		{"aliases", "a: &a x\nb: [" + strings.TrimSuffix(strings.Repeat("*a,", 51), ",") + "]\n", "", "OpenAPI document is not valid YAML or JSON: Excessive alias count"},
		{"blank", "# only a comment\n", "", "OpenAPI document root must be an object"},
		{"list", "- a\n", "", "OpenAPI document root must be an object"},
		{"swagger", "swagger: '2.0'\npaths: {/a: {get: {}}}\n", "", "only OpenAPI 3.x documents are supported"},
		{"number", "openapi: 3.0\npaths: {/a: {get: {}}}\n", "", "only OpenAPI 3.x documents are supported"},
		{"external", "openapi: 3.0.0\npaths:\n  /a:\n    get:\n      responses: {'200': {$ref: 'other.yaml#/x'}}\n", "", "external OpenAPI references are not supported (other.yaml#/x)"},
		{"no paths", "openapi: 3.0.0\npaths: {}\n", "", "OpenAPI document must define at least one path"},
		{"no operations", "openapi: 3.0.0\npaths: {/a: {summary: x}}\n", "", "OpenAPI document does not define any operations"},
		{"no server", "openapi: 3.0.0\npaths: {/a: {get: {}}}\n", "", "OpenAPI document needs an absolute server URL or a Base URL override"},
		{"relative server", "openapi: 3.0.0\nservers: [{url: /v1}]\npaths: {/a: {get: {}}}\n", "", "OpenAPI document needs an absolute server URL or a Base URL override"},
		{"ftp", "openapi: 3.0.0\nservers: [{url: 'ftp://x.example.com'}]\npaths: {/a: {get: {}}}\n", "", "OpenAPI server URL must use HTTP or HTTPS"},
		{"credentials", "openapi: 3.0.0\npaths: {/a: {get: {}}}\n", "https://u:p@x.example.com", "OpenAPI server URL must not include credentials"},
		{"bad base", "openapi: 3.0.0\npaths: {/a: {get: {}}}\n", "not a url", "OpenAPI server URL must be an absolute HTTP or HTTPS URL"},
	} {
		var base *string
		if c.base != "" {
			base = &c.base
		}
		if _, err := inspectOpenAPISpec(c.content, "x.yaml", "", base); err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}

	aliased, err := inspectOpenAPISpec("openapi: 3.0.0\nservers: [{url: 'https://a.example.com'}]\nx-op: &op {get: {}, put: {}}\npaths: {/a: *op, /b: *op}\n", "a.yaml", "", nil)
	if err != nil || aliased.OperationCount != 4 || aliased.Warnings[0] != "2 operations are mutating" {
		t.Fatalf("aliases: %v %+v", err, aliased)
	}
	one, _ := inspectOpenAPISpec("openapi: 3.0.0\nservers: [{url: 'https://a.example.com'}]\npaths: {/a: {post: {}}}\n", "a.yaml", "", nil)
	if one.Warnings[0] != "1 operation is mutating" {
		t.Fatalf("one: %q", one.Warnings)
	}
}

func ptr[T any](v T) *T { return &v }

func TestIsPublicAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "10.1.2.3": false, "100.64.0.1": false, "127.0.0.1": false, "169.254.169.254": false,
		"172.16.0.1": false, "172.32.0.1": true, "192.168.1.1": false, "198.18.0.1": false, "224.0.0.1": false,
		"255.255.255.255": false, "2606:4700::1111": true, "::1": false, "::": false, "::ffff:10.0.0.1": false,
		"::ffff:8.8.8.8": true, "fd00::1": false, "fe80::1%eth0": false, "ff02::1": false, "2001:db8::1": false,
	} {
		if got := isPublicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
}

func TestImportOpenAPIURL(t *testing.T) {
	var hits []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path+" "+r.Header.Get("User-Agent")+" "+r.Header.Get("Accept-Encoding"))
		switch r.URL.Path {
		case "/specs/pets.yaml":
			w.Write([]byte(petSpec))
		case "/relative/openapi.json":
			w.Write([]byte(`{"openapi":"3.0.0","servers":[{"url":"../v3"}],"paths":{"/a":{"get":{}}}}`))
		case "/moved":
			http.Redirect(w, r, "/specs/pets.yaml", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, "http://example.com/a.yaml", http.StatusFound)
		case "/big":
			w.Write(bytes.Repeat([]byte("x"), maxOpenAPISpecBytes+1))
		case "/":
			w.Write([]byte(petSpec))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	oldLookup, oldPublic, oldCAs := openAPILookup, openAPIPublic, openAPIRootCAs
	t.Cleanup(func() { openAPILookup, openAPIPublic, openAPIRootCAs = oldLookup, oldPublic, oldCAs })
	openAPIRootCAs = pool
	openAPILookup = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		switch host {
		case "example.com":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		case "mixed.example.com":
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")}, nil
		}
		return nil, fmt.Errorf("lookup %s: no such host", host)
	}
	openAPIPublic = func(a netip.Addr) bool { return a.IsLoopback() || isPublicAddress(a) }
	base := "https://EXAMPLE.com:" + port

	got, err := importOpenAPISpecFromURL(context.Background(), " "+base+"/moved ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.OriginalName != "pets.yaml" || got.SourceURL != "https://example.com:"+port+"/specs/pets.yaml" || got.OperationCount != 3 {
		t.Fatalf("import: %+v", got)
	}
	if hits[0] != "/moved OpenNeko-OpenAPI-Importer/1.0 identity" {
		t.Fatalf("request: %q", hits)
	}
	rel, err := importOpenAPISpecFromURL(context.Background(), base+"/relative/openapi.json", nil)
	if err != nil || rel.BaseURL != "https://example.com:"+port+"/v3" || rel.OriginalName != "openapi.json" {
		t.Fatalf("relative: %v %+v", err, rel)
	}
	root, err := importOpenAPISpecFromURL(context.Background(), base, ptr("https://api.example.com"))
	if err != nil || root.OriginalName != "openapi.yaml" {
		t.Fatalf("root: %v %+v", err, root)
	}

	for _, c := range []struct{ url, want string }{
		{"not a url", "OpenAPI URL must be a valid absolute URL"},
		{"http://example.com/a.yaml", "OpenAPI URL must use HTTPS"},
		{"https://u:p@example.com/a.yaml", "OpenAPI URL must not include credentials"},
		{"https://example.com/a.yaml?sig=1", "OpenAPI URL must not include a query string or fragment; upload private or signed specifications instead"},
		{"https://example.com/a.yaml#x", "OpenAPI URL must not include a query string or fragment"},
		{"https://mixed.example.com/a.yaml", "OpenAPI URL resolves to a non-public network address"},
		{"https://nowhere.example.com/a.yaml", "lookup nowhere.example.com: no such host"},
		{base + "/missing", "OpenAPI URL returned HTTP 404"},
		{base + "/loop", "OpenAPI URL redirected too many times"},
		{base + "/to-http", "OpenAPI URL must use HTTPS"},
		{base + "/big", "OpenAPI document exceeds the 2 MB limit"},
	} {
		if _, err := importOpenAPISpecFromURL(context.Background(), c.url, nil); err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: %v", c.url, err)
		}
	}
	openAPIPublic = isPublicAddress
	if _, err := importOpenAPISpecFromURL(context.Background(), base+"/specs/pets.yaml", nil); err == nil || err.Error() != "OpenAPI URL resolves to a non-public network address" {
		t.Errorf("loopback: %v", err)
	}
	openAPIPublic = func(netip.Addr) bool { return true }
	openAPIRootCAs = nil
	if _, err := importOpenAPISpecFromURL(context.Background(), base+"/specs/pets.yaml", nil); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("untrusted certificate: %v", err)
	}
}

func specUpload(t *testing.T, fields map[string]string, filename, content string) (string, map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if filename != "" {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
		h.Set("Content-Type", "application/yaml")
		w, _ := mw.CreatePart(h)
		w.Write([]byte(content))
	}
	mw.Close()
	return buf.String(), map[string]string{"Content-Type": mw.FormDataContentType()}
}

func TestOpenAPISpecRoutes(t *testing.T) {
	f, db := settingsFront(t)
	admin := sessionCookie("admin")
	const path = "/api/v1/settings/openapi-specs"
	jsonHeader := withCookie(map[string]string{"Content-Type": "application/json"}, admin)
	expect := func(name string, code int, body string, want int, wantBody string) {
		t.Helper()
		if code != want || body != wantBody {
			t.Fatalf("%s: %d %s", name, code, body)
		}
	}
	upload := func(fields map[string]string, filename, content string) (int, string) {
		body, h := specUpload(t, fields, filename, content)
		w := call(f, "POST", path, body, withCookie(h, admin))
		return w.Code, strings.TrimSpace(w.Body.String())
	}

	w := call(f, "GET", path, "", admin)
	expect("empty list", w.Code, strings.TrimSpace(w.Body.String()), 200, `{"assets":[]}`)
	for _, m := range []string{"GET", "POST"} {
		w = call(f, m, path, `{}`, withCookie(map[string]string{"Content-Type": "application/json"}, sessionCookie("member")))
		expect("member "+m, w.Code, strings.TrimSpace(w.Body.String()), 403, `{"error":"admin only"}`)
	}
	call(f, "PATCH", "/api/v1/settings/graphjin-config", `{"sourceConfigEnabled":false}`, admin)
	for _, m := range []string{"GET", "POST"} {
		w = call(f, m, path, `{"url":"https://example.com/a.yaml"}`, jsonHeader)
		expect("disabled "+m, w.Code, strings.TrimSpace(w.Body.String()), 404, `{"error":"not found"}`)
	}
	call(f, "PATCH", "/api/v1/settings/graphjin-config", `{"sourceConfigEnabled":true}`, admin)

	for _, body := range []string{``, `{}`, `[]`, `"x"`, `{"url":"  "}`, `{"url":5}`, `not json`} {
		w = call(f, "POST", path, body, jsonHeader)
		expect("no url "+body, w.Code, strings.TrimSpace(w.Body.String()), 400, `{"error":"url is required for a hosted OpenAPI import"}`)
	}
	w = call(f, "POST", path, `{"url":"http://example.com/a.yaml"}`, jsonHeader)
	expect("http", w.Code, strings.TrimSpace(w.Body.String()), 400, `{"error":"OpenAPI URL must use HTTPS"}`)
	w = call(f, "POST", path, `{"url":"https://localhost/a.yaml"}`, jsonHeader)
	expect("loopback", w.Code, strings.TrimSpace(w.Body.String()), 400, `{"error":"OpenAPI URL resolves to a non-public network address"}`)
	w = call(f, "POST", path, `{"url":"`+strings.Repeat("x", 4<<20)+`"}`, jsonHeader)
	expect("json too large", w.Code, strings.TrimSpace(w.Body.String()), 413, `{"error":"OpenAPI upload is too large"}`)

	code, body := upload(nil, "", "")
	expect("no file", code, body, 400, `{"error":"an OpenAPI YAML or JSON file is required"}`)
	code, body = upload(map[string]string{"file": "text"}, "", "")
	expect("text file field", code, body, 400, `{"error":"an OpenAPI YAML or JSON file is required"}`)
	code, body = upload(nil, "a.yaml", "")
	expect("empty file", code, body, 400, `{"error":"OpenAPI file must be between 1 byte and 2 MB"}`)
	code, body = upload(nil, "a.yaml", strings.Repeat("x", maxOpenAPISpecBytes+1))
	expect("big file", code, body, 400, `{"error":"OpenAPI file must be between 1 byte and 2 MB"}`)
	code, body = upload(nil, "a.txt", petSpec)
	expect("extension", code, body, 400, `{"error":"OpenAPI file must have a .yaml, .yml, or .json extension"}`)
	code, body = upload(nil, "a.yaml", "openapi: 3.0.0\npaths: {}\n")
	expect("bad spec", code, body, 400, `{"error":"OpenAPI document must define at least one path"}`)
	w = call(f, "POST", path, "--x\r\nbad", withCookie(map[string]string{"Content-Type": "multipart/form-data; boundary=x"}, admin))
	expect("bad multipart", w.Code, strings.TrimSpace(w.Body.String()), 400, `{"error":"Failed to parse body as FormData."}`)

	code, body = upload(map[string]string{"baseUrl": "  "}, "Pets.YML", "\ufeff"+petSpec)
	if code != 201 {
		t.Fatalf("upload: %d %s", code, body)
	}
	var created struct{ Asset openAPISpecAsset }
	json.Unmarshal([]byte(body), &created)
	a := created.Asset
	if a.OriginalName != "Pets.YML" || a.SourceType != "upload" || a.Title != "Pet Service" || *a.Version != "1.2.0" ||
		a.BaseURL != "https://api.example.com/v1" || a.Status != "staged" || a.SourceName != nil || len(a.Warnings) != 2 {
		t.Fatalf("asset: %s", body)
	}
	for _, key := range []string{`"id":`, `"createdAt":`, `"authSchemes":["key (apiKey)","other"]`, `"sourceName":null`, `"operationCount":3`} {
		if !strings.Contains(body, key) {
			t.Fatalf("asset JSON lacks %s: %s", key, body)
		}
	}
	var content, checksum, createdBy, format string
	var sourceURL *string
	var servers []string
	if err := db.QueryRow(context.Background(), `select content, checksum_sha256, created_by_user_id, document_format, source_url, server_urls
		from openapi_spec_asset where id = $1 and org_id = 'org'`, a.ID).Scan(&content, &checksum, &createdBy, &format, &sourceURL, &servers); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	if hex.EncodeToString(sum[:]) != checksum || checksum != a.ChecksumSha256 || createdBy != "admin" || format != "yaml" || sourceURL != nil || len(servers) != 2 {
		t.Fatalf("stored row: %s %s %v %v", createdBy, format, sourceURL, servers)
	}

	code, body = upload(map[string]string{"baseUrl": " https://override.example.com/ "}, "b.json", `{"openapi":"3.0.0","info":{"title":"B"},"paths":{"/b":{"get":{}}}}`)
	if code != 201 || !strings.Contains(body, `"baseUrl":"https://override.example.com"`) || !strings.Contains(body, `"version":null`) {
		t.Fatalf("json upload: %d %s", code, body)
	}
	seed(t, db, `insert into openapi_spec_asset (org_id, source_type, original_name, content, checksum_sha256, title, base_url, created_at)
		values ('org', 'url', 'old.yaml', 'x', 'x', 'Old', 'https://old.example.com', now() - interval '1 day')`)
	w = call(f, "GET", path, "", admin)
	var list struct{ Assets []openAPISpecAsset }
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list.Assets) != 3 || list.Assets[0].Title != "B" || list.Assets[1].ID != a.ID || list.Assets[2].SourceType != "url" {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	for range 20 {
		upload(nil, "c.yaml", "openapi: 3.0.0\nservers: [{url: 'https://c.example.com'}]\npaths: {/c: {get: {}}}\n")
	}
	w = call(f, "GET", path, "", admin)
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Assets) != 20 {
		t.Fatalf("limit: %d", len(list.Assets))
	}
}
