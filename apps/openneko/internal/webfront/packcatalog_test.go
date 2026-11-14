package webfront

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

func servicePackZip(t *testing.T, mutate func(name string, data []byte) []byte) string {
	t.Helper()
	src := filepath.Join("..", "..", "..", "..", "apps", "worker", "test", "fixtures", "service-health")
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		h := &zip.FileHeader{Name: name, CreatorVersion: 0x0314, ExternalAttrs: 0o100600 << 16, Method: zip.Deflate}
		if strings.HasSuffix(name, "/") {
			h.ExternalAttrs, h.Method = 0o040700<<16, zip.Store
		}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		name := "service-health"
		if rel != "." {
			name += "/" + filepath.ToSlash(rel)
		}
		if d.IsDir() {
			add(name+"/", nil)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if mutate != nil {
			data = mutate(rel, data)
		}
		add(name, data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	add("service-health/actions/", nil)
	add("service-health/policies/", nil)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func packCatalogFront(t *testing.T) (*Front, *packWorker, map[string]string) {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "packs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_PACKS_DIR", abs)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, wk := packFront(t)
	return f, wk, sessionCookie("admin")
}

func TestPackCatalogListAndUpload(t *testing.T) {
	f, wk, admin := packCatalogFront(t)
	list := func() []any {
		t.Helper()
		w := call(f, "GET", "/api/v1/admin/packs", "", admin)
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		return decode(t, w)["packs"].([]any)
	}
	got := list()
	var ids []string
	for _, p := range got {
		ids = append(ids, p.(map[string]any)["id"].(string))
	}
	if !slices.Equal(ids, []string{"google-workspace", "magento", "parallel-search", "resend", "shopify", "slack"}) {
		t.Fatalf("ids %v", ids)
	}
	if first := got[0].(map[string]any); first["source"] != "embedded" || first["status"] != "available" || first["installed"] != false || first["lastError"] != nil || first["name"] == "" {
		t.Errorf("first %v", first)
	}
	seed(t, f.API.DB, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, last_error) values ('org', 'slack', '0.1.0', 'failed', 'h', 'boom')`)

	upload := func(body string) (int, map[string]any) {
		t.Helper()
		header := map[string]string{"Content-Type": "application/zip"}
		for k, v := range admin {
			header[k] = v
		}
		w := call(f, "POST", "/api/v1/admin/packs/upload", body, header)
		return w.Code, decode(t, w)
	}
	code, stored := upload(servicePackZip(t, nil))
	if code != 200 || stored["packId"] != "service-health" || stored["orgId"] != "org" || stored["uploadedBy"] != "admin" || stored["version"] != "0.1.0" {
		t.Fatalf("upload: %d %v", code, stored)
	}
	code, again := upload(servicePackZip(t, nil))
	if code != 200 || again["uploadedAt"] != stored["uploadedAt"] {
		t.Errorf("same upload again: %d %v", code, again)
	}
	code, changed := upload(servicePackZip(t, func(name string, data []byte) []byte {
		if name == "pack.yaml" {
			return append(data, []byte("\n")...)
		}
		return data
	}))
	if code != 400 || !strings.Contains(changed["error"].(string), "immutable") {
		t.Errorf("changed upload: %d %v", code, changed)
	}

	got = list()
	if len(got) != 7 {
		t.Fatalf("list after upload: %v", got)
	}
	last := got[6].(map[string]any)
	if last["id"] != "service-health" || last["source"] != "uploaded" || last["status"] != "available" {
		t.Errorf("uploaded entry %v", last)
	}
	for _, p := range got {
		if m := p.(map[string]any); m["id"] == "slack" && (m["status"] != "failed" || m["installed"] != false || m["lastError"] != "boom") {
			t.Errorf("slack entry %v", m)
		}
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("worker calls: %v", calls)
	}
}

func TestPackUploadRefusesFirstPartyIDs(t *testing.T) {
	f, _, admin := packCatalogFront(t)
	header := map[string]string{"Content-Type": "application/zip"}
	for k, v := range admin {
		header[k] = v
	}
	renamed := servicePackZip(t, func(name string, data []byte) []byte {
		if name == "pack.yaml" {
			return bytes.Replace(data, []byte("id: service-health"), []byte("id: magento"), 1)
		}
		return data
	})
	for _, id := range []string{"magento", "slack"} {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		r, err := zip.NewReader(strings.NewReader(renamed), int64(len(renamed)))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range r.File {
			h := file.FileHeader
			h.Name = strings.Replace(h.Name, "service-health", id, 1)
			fw, _ := zw.CreateHeader(&h)
			rc, _ := file.Open()
			var data bytes.Buffer
			_, _ = data.ReadFrom(rc)
			rc.Close()
			_, _ = fw.Write(bytes.Replace(data.Bytes(), []byte("id: magento"), []byte("id: "+id), 1))
		}
		_ = zw.Close()
		w := call(f, "POST", "/api/v1/admin/packs/upload", buf.String(), header)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "reserved for a first-party pack") {
			t.Errorf("%s: %d %s", id, w.Code, w.Body.String())
		}
	}
}

func TestPackInspect(t *testing.T) {
	f, _, admin := packCatalogFront(t)
	w := call(f, "GET", "/api/v1/admin/packs/slack/inspect", "", admin)
	if w.Code != 200 {
		t.Fatalf("slack: %d %s", w.Code, w.Body.String())
	}
	got := decode(t, w)
	b, err := packs.LoadBundle(filepath.Join(os.Getenv("OPENNEKO_PACKS_DIR"), "slack"))
	if err != nil {
		t.Fatal(err)
	}
	perms := got["permissions"].(map[string]any)
	if got["source"] != "embedded" || got["manifestHash"] != b.ManifestHash || got["bundleHash"] != b.BundleHash || got["upload"] != nil ||
		perms["network"] != "slack.com" || perms["database"] != "none" || got["manifest"].(map[string]any)["metadata"].(map[string]any)["id"] != "slack" {
		t.Errorf("slack inspect %v", got)
	}
	if reqs := got["bindingRequirements"].([]any); len(reqs) != 0 {
		t.Errorf("slack bindings %v", reqs)
	}
	w = call(f, "GET", "/api/v1/admin/packs/magento/inspect", "", admin)
	if w.Code != 200 || decode(t, w)["permissions"].(map[string]any)["customerPii"] != "available to authenticated read-only queries" {
		t.Errorf("magento: %d %s", w.Code, w.Body.String())
	}
	w = call(f, "GET", "/api/v1/admin/packs/no-such-pack/inspect", "", admin)
	if w.Code != 400 {
		t.Fatalf("unknown: %d %s", w.Code, w.Body.String())
	}
	if d := decode(t, w)["diagnostic"].(map[string]any); d["code"] != "pack_operation_failed" || d["phase"] != "inspection" ||
		d["nextStep"] != "Correct the reported manifest or artifact, then inspect the pack again." {
		t.Errorf("diagnostic %v", d)
	}

	header := map[string]string{"Content-Type": "application/zip"}
	for k, v := range admin {
		header[k] = v
	}
	if w := call(f, "POST", "/api/v1/admin/packs/upload", servicePackZip(t, nil), header); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/admin/packs/service-health/inspect", "/api/v1/admin/packs/service-health/inspect?version=0.1.0"} {
		w = call(f, "GET", path, "", admin)
		got = decode(t, w)
		if w.Code != 200 || got["source"] != "uploaded" || got["upload"].(map[string]any)["uploadedBy"] != "admin" {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w = call(f, "GET", "/api/v1/admin/packs/service-health/inspect?version=9.9.9", "", admin); w.Code != 400 {
		t.Errorf("missing version: %d %s", w.Code, w.Body.String())
	}
}

func TestPackStatus(t *testing.T) {
	f, _, admin := packCatalogFront(t)
	if w := call(f, "GET", "/api/v1/admin/packs/slack/status", "", admin); w.Code != 404 || w.Body.String() != `{"error":"pack slack is not installed"}` {
		t.Errorf("not installed: %d %s", w.Code, w.Body.String())
	}
	seed(t, f.API.DB, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, installed_at, created_at, config)
		values ('00000000-0000-0000-0000-000000000001', 'org', 'slack', '0.1.0', 'removed', 'h', null, now() - interval '1 day', '{}'),
		('00000000-0000-0000-0000-000000000002', 'org', 'slack', '0.2.0', 'installed', 'h', '2026-11-14T10:00:00.5Z', now(),
		'{"slack.channel":"#ops","a":1,"_bundle":{"contentHash":"x"},"_runtime":{"source":{"id":"ds-1","graphqlUrl":"http://gj"},"bindings":{"db":"warehouse"},"readiness":["messaging","orders","operator"]}}')`)
	seed(t, f.API.DB, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash, readiness, readiness_reason)
		values ('00000000-0000-0000-0000-000000000002', 'org', 'action', 'action.a', 'a', 'h', 'h', 'ready', 'operator:orders:connected'),
		('00000000-0000-0000-0000-000000000002', 'org', 'action', 'action.b', 'b', 'h', 'h', 'blocked', 'operator:orders:oauth_not_connected'),
		('00000000-0000-0000-0000-000000000002', 'org', 'action', 'action.c', 'c', 'h', 'h', 'blocked', 'operator:plain'),
		('00000000-0000-0000-0000-000000000002', 'org', 'action', 'action.d', 'd', 'h', 'h', 'ready', 'not an operator reason')`)
	w := call(f, "GET", "/api/v1/admin/packs/slack/status", "", admin)
	want := `{"packId":"slack","version":"0.2.0","status":"installed","readiness":{"orders":{"status":"blocked","reason":"oauth_not_connected"},` +
		`"operator":{"status":"blocked","reason":"plain"},"messaging":{"status":"ready","reason":null}},"installedAt":"2026-11-14T10:00:00.500Z",` +
		`"lastError":null,"configuration":{"required":false,"inputs":{"a":1,"slack.channel":"#ops"},"dataSourceId":"ds-1","sourceBindings":{"db":"warehouse"}}}`
	if w.Code != 200 || w.Body.String() != want {
		t.Errorf("status:\n got %d %s\nwant %s", w.Code, w.Body.String(), want)
	}
	seed(t, f.API.DB, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config)
		values ('org', 'resend', '0.1.0', 'planning', 'h', '{"_definitionOnly":true,"_runtime":{"readiness":["email"]}}')`)
	w = call(f, "GET", "/api/v1/admin/packs/resend/status", "", admin)
	want = `{"packId":"resend","version":"0.1.0","status":"planning","readiness":{"setup":{"status":"blocked","reason":"configuration_required"},` +
		`"email":{"status":"blocked","reason":"pack_not_active"}},"installedAt":null,"lastError":null,"configuration":{"required":true,"inputs":{},"sourceBindings":{}}}`
	if w.Code != 200 || w.Body.String() != want {
		t.Errorf("definition only:\n got %d %s\nwant %s", w.Code, w.Body.String(), want)
	}
}
