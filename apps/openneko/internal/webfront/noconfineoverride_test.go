package webfront

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNoRouteTurnsAgentTurnsOnWithoutConfinement(t *testing.T) {
	for path, methods := range GoRoutes {
		if strings.Contains(path, "unconfined") || strings.Contains(path, "confinement") {
			t.Errorf("the API has %s %v; no route may change agent confinement", path, methods)
		}
	}
	h := (&API{}).OperatorHandler()
	for _, probe := range []struct{ method, path string }{
		{http.MethodPost, "/admin/confinement/unconfined"},
		{http.MethodGet, "/health/agent-unconfined"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(probe.method, probe.path, strings.NewReader(`{"on":true,"reason":"x"}`)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("the web admin socket answers %s %s with %d", probe.method, probe.path, rec.Code)
		}
	}
}
