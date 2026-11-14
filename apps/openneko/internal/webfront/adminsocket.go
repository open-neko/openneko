package webfront

import "net/http"

// OperatorHandler serves the pack routes for `openneko pack` on the web's
// admin socket. The socket's file mode is the only check, so the caller acts
// as an admin with no user.
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
	return mux
}
