package webfront

import (
	"context"
	"net/http"
	"os"
	"strings"
)

// OperatorHandler serves the pack, source check, GraphJin import and health
// routes on the web's admin socket. The socket's file mode is the only check, so the
// caller acts as an admin with no user.
func (a *API) OperatorHandler() http.Handler {
	operator := func(serve func(http.ResponseWriter, *http.Request, actor)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			orgID, err := a.OrgID(r.Context())
			if err != nil {
				a.fail(w, r, err)
				return
			}
			serve(w, r, actor{OrgID: orgID, Admin: true})
		}
	}
	mux := http.NewServeMux()
	mux.Handle("GET /admin/packs", operator(a.servePackList))
	mux.Handle("POST /admin/packs/upload", operator(a.packUpload))
	mux.Handle("GET /admin/packs/{packId}/{action}", operator(a.packRead))
	mux.Handle("POST /admin/packs/{packId}/{action}", operator(a.packWrite))
	mux.Handle("POST /admin/sources/check", operator(a.serveSourceCheck))
	mux.Handle("POST /admin/graphjin/import", operator(a.serveGraphjinImport))
	mux.HandleFunc("GET /health/agent-unconfined", a.serveUnconfinedHealth)
	mux.HandleFunc("POST /admin/confinement/unconfined", a.operatorUnconfined)
	for _, path := range []string{"/health", "/health/scheduler", "/health/security"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) { a.serveHealth(w, path, true) })
	}
	return mux
}

func (a *API) serveGraphjinImport(w http.ResponseWriter, r *http.Request, act actor) {
	update, ok := packObjectBody(w, r)
	if !ok {
		return
	}
	configFile := strings.TrimSpace(os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"))
	if configFile == "" {
		sendError(w, http.StatusBadRequest, "OPENNEKO_GRAPHJIN_CONFIG is not set")
		return
	}
	ctx := context.WithoutCancel(r.Context())
	endpoint, err := a.defaultGraphjinEndpoint(ctx, act.OrgID)
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	if endpoint == "" {
		endpoint = "http://graphjin:8080/api/v1/graphql"
	}
	if _, err := a.applyPackGraphjinConfig(ctx, endpoint, act.OrgID, configFile, packGraphjinUpdate{update: update}, true); err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"ok": true})
}
