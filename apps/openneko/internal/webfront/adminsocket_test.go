package webfront

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperatorHandlerServesPackRoutes(t *testing.T) {
	f, wk, _ := packCatalogFront(t)
	h := f.API.OperatorHandler()
	do := func(method, path, contentType, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("GET", "/admin/packs", "", ""); w.Code != 200 || len(decode(t, w)["packs"].([]any)) == 0 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/admin/packs/magento/inspect?version=", "", ""); w.Code != 200 || decode(t, w)["manifest"] == nil {
		t.Fatalf("inspect: %d %s", w.Code, w.Body.String())
	}
	w := do("POST", "/admin/packs/upload", "application/zip", servicePackZip(t, nil))
	if got := decode(t, w); w.Code != 200 || got["packId"] != "service-health" || got["uploadedBy"] != nil {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/admin/packs/magento/status", "", ""); w.Code != http.StatusNotFound {
		t.Fatalf("status of a pack not installed: %d %s", w.Code, w.Body.String())
	}
	if w := do("POST", "/admin/packs/magento/explode", "application/json", "{}"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown action: %d", w.Code)
	}
	if w := do("GET", "/api/v1/admin/settings", "", ""); w.Code != http.StatusNotFound {
		t.Fatalf("the socket serves only the pack routes: %d", w.Code)
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Fatalf("the worker was called: %v", calls)
	}
}
