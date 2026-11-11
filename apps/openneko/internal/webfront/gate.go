package webfront

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

const (
	SessionCookie        = "openneko_session"
	providerCacheTTL     = time.Second
	adminCacheTTL        = 5 * time.Second
	workerStatusTimeout  = 1500 * time.Millisecond
	workerStatusMaxBytes = 64 * 1024
	deviceIdleDays       = 30
	lastUsedResolution   = 60
)

var (
	bearerDevice = regexp.MustCompile(`(?i)^Bearer[ \t]+(onk_dev_\S+)$`)
	deviceToken  = regexp.MustCompile(`^(onk_dev_[0-9a-f]{12})_[A-Za-z0-9_-]{43}$`)
)

// Gate applies the access table to a request, as proxy.ts does in the Next
// server: it reads the signed session cookie or a device token and the
// Administrators membership.
type Gate struct {
	WorkerURL string
	DB        *pgxpool.Pool
	// SessionSecret returns the cookie signing secret; nil reads
	// OPENNEKO_SESSION_SECRET or derives it from the secret-key file.
	SessionSecret func() (string, error)
	MarkerPath    string
	Client        *http.Client
	Now           func() time.Time

	mu       sync.Mutex
	provider *providerProbe
	marker   *markerState
	admins   map[string]adminProbe
	secret   string
}

type providerProbe struct {
	live bool
	at   time.Time
}

type adminProbe struct {
	admin bool
	at    time.Time
}

type markerState struct {
	found    bool
	provider string
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Allow answers the request itself and returns false, or returns true when
// the request may go on.
func (g *Gate) Allow(w http.ResponseWriter, r *http.Request) bool {
	pathname := r.URL.Path
	isAPI := strings.HasPrefix(pathname, "/api/")
	policy, ok := PolicyFor(pathname)
	if !ok {
		if isAPI {
			writeJSON(w, http.StatusNotFound, "not found")
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
		return false
	}
	if policy.Rule == Public || policy.Rule == Token {
		return true
	}
	if !g.signInLive(r.Context()) {
		return true
	}
	authorization := r.Header.Get("Authorization")
	userID := ""
	if c, err := r.Cookie(SessionCookie); err == nil {
		userID = g.SessionUserID(c.Value)
	}
	if userID == "" {
		userID = g.deviceUserID(r.Context(), authorization)
	}
	if userID == "" && authorization != "" && isAPI {
		writeJSON(w, http.StatusUnauthorized, "sign in again")
		return false
	}
	if userID == "" {
		returnTo := r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			returnTo += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, "/signin?returnTo="+encodeURIComponent(returnTo), http.StatusFound)
		return false
	}
	if policy.Rule == Admin && !g.isAdministrator(r.Context(), userID) {
		if isAPI {
			writeJSON(w, http.StatusForbidden, "admin only")
		} else {
			http.Redirect(w, r, "/", http.StatusFound)
		}
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (g *Gate) sessionSecret() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.secret != "" {
		return g.secret
	}
	read := g.SessionSecret
	if read == nil {
		read = DefaultSessionSecret
	}
	secret, err := read()
	if err != nil {
		return ""
	}
	g.secret = secret
	return secret
}

// DefaultSessionSecret matches sessionSecret() in apps/web/src/lib/auth.ts.
func DefaultSessionSecret() (string, error) {
	if s := os.Getenv("OPENNEKO_SESSION_SECRET"); s != "" {
		return s, nil
	}
	key, err := config.SigningSecret("", "session-cookie:v1")
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// SessionUserID returns the user of a valid signed session cookie, or "".
func (g *Gate) SessionUserID(value string) string {
	if value == "" {
		return ""
	}
	secret := g.sessionSecret()
	if len(secret) < 32 {
		return ""
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(expected)) != 1 {
		return ""
	}
	expiresAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expiresAt < g.now().Unix() {
		return ""
	}
	return parts[0]
}

func (g *Gate) deviceUserID(ctx context.Context, authorization string) string {
	m := bearerDevice.FindStringSubmatch(authorization)
	if m == nil || g.DB == nil {
		return ""
	}
	token := m[1]
	parsed := deviceToken.FindStringSubmatch(token)
	if parsed == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	digest := hex.EncodeToString(sum[:])
	var id, userID, verifier string
	var stale bool
	err := g.DB.QueryRow(ctx, `
		select d.id, d.user_id, d.token_verifier,
		       d.last_used_at < now() - make_interval(secs => $3) as stale
		from app_device d
		join app_user u on u.id = d.user_id and u.disabled_at is null
		where d.org_id = (select id from organization order by created_at, id limit 1)
		  and d.token_prefix = $1 and d.revoked_at is null
		  and d.last_used_at > now() - make_interval(days => $2)`,
		parsed[1], deviceIdleDays, lastUsedResolution).Scan(&id, &userID, &verifier, &stale)
	if err != nil || subtle.ConstantTimeCompare([]byte(digest), []byte(verifier)) != 1 {
		return ""
	}
	if stale {
		_, _ = g.DB.Exec(ctx, `update app_device set last_used_at = now() where id = $1`, id)
	}
	return userID
}

func (g *Gate) isAdministrator(ctx context.Context, userID string) bool {
	now := g.now()
	g.mu.Lock()
	if p, ok := g.admins[userID]; ok && now.Sub(p.at) < adminCacheTTL {
		g.mu.Unlock()
		return p.admin
	}
	g.mu.Unlock()
	if g.DB == nil {
		return true
	}
	var admin bool
	err := g.DB.QueryRow(ctx, `
		select exists (
		  select 1 from user_group g
		  join app_user u on u.id = $1 and u.org_id = g.org_id and u.disabled_at is null
		  join user_group_membership m on m.group_id = g.id and m.user_id = u.id
		  where g.org_id = (select id from organization order by created_at, id limit 1)
		    and g.slug = 'administrators')`, userID).Scan(&admin)
	if err != nil {
		// The route handler checks again, so a database blip must not lock
		// an administrator out of the pages that report it.
		return true
	}
	g.mu.Lock()
	if g.admins == nil {
		g.admins = map[string]adminProbe{}
	}
	g.admins[userID] = adminProbe{admin: admin, at: now}
	g.mu.Unlock()
	return admin
}

func (g *Gate) signInLive(ctx context.Context) bool {
	now := g.now()
	g.mu.Lock()
	if g.provider != nil && now.Sub(g.provider.at) < providerCacheTTL {
		live := g.provider.live
		g.mu.Unlock()
		return live
	}
	g.mu.Unlock()
	provider, err := g.fetchProvider(ctx)
	var live bool
	if err != nil {
		live = g.readMarker().provider != ""
	} else {
		live = provider != ""
		g.writeMarker(provider)
	}
	g.mu.Lock()
	g.provider = &providerProbe{live: live, at: now}
	g.mu.Unlock()
	return live
}

func (g *Gate) fetchProvider(ctx context.Context) (string, error) {
	base := strings.TrimRight(g.WorkerURL, "/")
	if base == "" {
		base = "http://127.0.0.1:4100"
	}
	ctx, cancel := context.WithTimeout(ctx, workerStatusTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/admin/auth/status", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", errStatus
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, workerStatusMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > workerStatusMaxBytes {
		return "", errStatus
	}
	var body struct {
		Provider *struct {
			PluginName string `json:"pluginName"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	if body.Provider == nil {
		return "", nil
	}
	if body.Provider.PluginName == "" {
		return "provider", nil
	}
	return body.Provider.PluginName, nil
}

type statusError string

func (e statusError) Error() string { return string(e) }

const errStatus = statusError("the worker did not give the sign-in status")

// DefaultMarkerPath matches markerPath() in apps/web/src/lib/auth-gate-marker.ts.
func DefaultMarkerPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if strings.TrimSpace(base) == "" {
		home := os.Getenv("HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(strings.TrimSpace(base), "openneko", "auth-gate.json")
}

func (g *Gate) markerPath() string {
	if g.MarkerPath != "" {
		return g.MarkerPath
	}
	return DefaultMarkerPath()
}

func (g *Gate) readMarker() markerState {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.marker != nil {
		return *g.marker
	}
	state := markerState{}
	if raw, err := os.ReadFile(g.markerPath()); err == nil {
		var parsed map[string]json.RawMessage
		if json.Unmarshal(raw, &parsed) == nil {
			if p, ok := parsed["provider"]; ok {
				state.found = true
				var provider struct {
					PluginName string `json:"pluginName"`
				}
				if string(p) != "null" && json.Unmarshal(p, &provider) == nil {
					state.provider = provider.PluginName
					if state.provider == "" {
						state.provider = "provider"
					}
				}
			}
		}
	}
	g.marker = &state
	return state
}

// writeMarker keeps the file format of auth-gate-marker.ts. A read-only config
// folder leaves the marker stale and must not fail the request.
func (g *Gate) writeMarker(provider string) {
	previous := g.readMarker()
	g.mu.Lock()
	g.marker = &markerState{found: true, provider: provider}
	g.mu.Unlock()
	if previous.found && previous.provider == provider {
		return
	}
	var body any = map[string]any{"provider": nil}
	if provider != "" {
		body = map[string]any{"provider": map[string]string{"pluginName": provider}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return
	}
	path := g.markerPath()
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	if os.WriteFile(tmp, raw, 0o600) != nil {
		return
	}
	if os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
	}
}

func encodeURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
	}
	return b.String()
}
