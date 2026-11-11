package webfront

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// API answers the routes that moved from Node to Go. A route it does not
// hold goes to Node.
type API struct {
	Gate    *Gate
	DB      *pgxpool.Pool
	Version string
	// PublicURL overrides OPENNEKO_PUBLIC_URL; tests set it.
	PublicURL string
	// AuditWebhook overrides OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL.
	AuditWebhook string
	// SecureCookies marks the session and sign-in state cookies Secure.
	SecureCookies bool
	// SecretsDir overrides the config folder that holds secrets.json.
	SecretsDir string

	once   sync.Once
	routes map[string]map[string]http.HandlerFunc
	orgMu  sync.Mutex
	orgID  string
}

// GoRoutes lists the /api/v1 routes that Go answers, by method.
// TestGoRoutesInContract checks each one against api/openapi.yaml, and
// apps/web/test/api/openapi-contract.test.ts reads this map as text.
var GoRoutes = map[string][]string{
	"/api/v1/version":                                   {"GET"},
	"/api/v1/auth/status":                               {"GET"},
	"/api/v1/auth/session":                              {"GET"},
	"/api/v1/auth/begin":                                {"GET"},
	"/api/v1/auth/callback":                             {"GET"},
	"/api/v1/auth/logout":                               {"POST"},
	"/api/v1/auth/device/code":                          {"POST"},
	"/api/v1/auth/device/token":                         {"POST"},
	"/api/v1/auth/device/approve":                       {"POST"},
	"/api/v1/sso/setup/status":                          {"GET"},
	"/api/v1/sso/setup/environments":                    {"GET"},
	"/api/v1/sso/setup/organizations":                   {"GET"},
	"/api/v1/sso/setup/credentials-info":                {"GET"},
	"/api/v1/sso/setup/ids":                             {"POST"},
	"/api/v1/sso/setup/credentials":                     {"POST"},
	"/api/v1/sso/setup/generate-portal-link":            {"POST"},
	"/api/v1/sso/setup/check":                           {"POST"},
	"/api/v1/profile":                                   {"GET", "PATCH"},
	"/api/v1/my/pack-accounts/{packId}/{connectionKey}": {"POST", "DELETE"},
	"/api/v1/observations":                              {"GET"},
	"/api/v1/approvals":                                 {"GET"},
	"/api/v1/action-requests":                           {"GET"},
	"/api/v1/action-requests/{actionRequestId}":         {"GET"},
}

func (a *API) table() map[string]map[string]http.HandlerFunc {
	a.once.Do(func() {
		a.routes = map[string]map[string]http.HandlerFunc{
			"/api/v1/version":                                   {"GET": a.version},
			"/api/v1/auth/status":                               {"GET": a.authStatus},
			"/api/v1/auth/session":                              {"GET": a.authSession},
			"/api/v1/auth/begin":                                {"GET": a.authBegin},
			"/api/v1/auth/callback":                             {"GET": a.authCallback},
			"/api/v1/auth/logout":                               {"POST": a.logout},
			"/api/v1/auth/device/code":                          {"POST": a.deviceCode},
			"/api/v1/auth/device/token":                         {"POST": a.deviceToken},
			"/api/v1/auth/device/approve":                       {"POST": a.deviceApprove},
			"/api/v1/sso/setup/status":                          {"GET": a.ssoSetupStatus},
			"/api/v1/sso/setup/environments":                    {"GET": a.ssoSetupEnvironments},
			"/api/v1/sso/setup/organizations":                   {"GET": a.ssoSetupOrganizations},
			"/api/v1/sso/setup/credentials-info":                {"GET": a.ssoSetupCredentialsInfo},
			"/api/v1/sso/setup/ids":                             {"POST": a.ssoSetupIDs},
			"/api/v1/sso/setup/credentials":                     {"POST": a.ssoSetupCredentials},
			"/api/v1/sso/setup/generate-portal-link":            {"POST": a.ssoSetupPortalLink},
			"/api/v1/sso/setup/check":                           {"POST": a.ssoSetupCheck},
			"/api/v1/profile":                                   {"GET": a.profileGet, "PATCH": a.profilePatch},
			"/api/v1/my/pack-accounts/{packId}/{connectionKey}": {"POST": a.personalPackAccount, "DELETE": a.personalPackAccount},
			"/api/v1/observations":                              {"GET": a.observations},
			"/api/v1/approvals":                                 {"GET": a.approvals},
			"/api/v1/action-requests":                           {"GET": a.actionRequests},
			"/api/v1/action-requests/{actionRequestId}":         {"GET": a.actionRequest, "PATCH": nil},
		}
	})
	return a.routes
}

// Handler returns the Go handler for the request, or nil to pass it to Node.
// A method with a nil handler in the table stays with Node.
func (a *API) Handler(r *http.Request) http.HandlerFunc {
	if a == nil {
		return nil
	}
	methods, ok := a.table()[APIV1Path(r.URL.Path)]
	if !ok {
		methods = a.templateMatch(r)
		if methods == nil {
			return nil
		}
	}
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	if h, ok := methods[method]; ok {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		allow := make([]string, 0, len(methods))
		for m := range methods {
			allow = append(allow, m)
		}
		sort.Strings(allow)
		w.Header().Set("Allow", strings.Join(allow, ", "))
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// templateMatch finds a route with {name} segments and sets the path values,
// decoded per segment as Next does.
func (a *API) templateMatch(r *http.Request) map[string]http.HandlerFunc {
	got := strings.Split(APIV1Path(r.URL.EscapedPath()), "/")
	for route, methods := range a.table() {
		if !strings.Contains(route, "{") {
			continue
		}
		want := strings.Split(route, "/")
		if len(want) != len(got) {
			continue
		}
		values := map[string]string{}
		matched := true
		for i, seg := range want {
			if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
				v, err := url.PathUnescape(got[i])
				if err != nil || v == "" {
					matched = false
					break
				}
				values[seg[1:len(seg)-1]] = v
			} else if seg != got[i] {
				matched = false
				break
			}
		}
		if matched {
			for k, v := range values {
				r.SetPathValue(k, v)
			}
			return methods
		}
	}
	return nil
}

func sendJSON(w http.ResponseWriter, status int, body any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
}

func sendError(w http.ResponseWriter, status int, message string) {
	sendJSON(w, status, map[string]string{"error": message})
}

func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	sendError(w, http.StatusInternalServerError, "internal server error")
}

const maxBody = 1 << 20

// readBody parses a JSON body as Node's `request.json().catch(...)` does:
// a body that is not JSON gives ok false.
func readBody(r *http.Request) (any, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return nil, false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	return v, true
}

func field(body any, name string) (any, bool) {
	m, ok := body.(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := m[name]
	return v, ok
}

func (a *API) publicBaseURL(r *http.Request) string {
	configured := a.PublicURL
	if configured == "" {
		configured = os.Getenv("OPENNEKO_PUBLIC_URL")
	}
	if c := strings.TrimRight(strings.TrimSpace(configured), "/"); c != "" {
		return c
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

var errNoOrg = errors.New("organization not found")
