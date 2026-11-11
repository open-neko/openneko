package webfront

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
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
	status AuthStatus
	at     time.Time
}

// AuthStatus is the worker's sign-in answer. Provider holds the provider
// object as the worker sent it, or nil when sign-in is off.
type AuthStatus struct {
	Provider json.RawMessage
	Pending  json.RawMessage
}

func (s AuthStatus) Live() bool { return s.Provider != nil }

type adminProbe struct {
	admin bool
	at    time.Time
}

type markerState struct {
	found    bool
	provider json.RawMessage
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
		log.Printf("session secret: %v", err)
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
	if err := config.EnsureSecretKey(""); err != nil {
		return "", err
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
	_, userID, _ := g.Device(ctx, authorization)
	return userID
}

// Device returns the installed app's device and user behind a bearer device
// token, or "" for both when the header holds no valid token.
func (g *Gate) Device(ctx context.Context, authorization string) (deviceID, userID string, err error) {
	m := bearerDevice.FindStringSubmatch(authorization)
	if m == nil || g.DB == nil {
		return "", "", nil
	}
	token := m[1]
	parsed := deviceToken.FindStringSubmatch(token)
	if parsed == nil {
		return "", "", nil
	}
	sum := sha256.Sum256([]byte(token))
	digest := hex.EncodeToString(sum[:])
	var id, verifier string
	var stale bool
	err = g.DB.QueryRow(ctx, `
		select d.id, d.user_id, d.token_verifier,
		       d.last_used_at < now() - make_interval(secs => $3) as stale
		from app_device d
		join app_user u on u.id = d.user_id and u.disabled_at is null
		where d.org_id = (select id from organization order by created_at, id limit 1)
		  and d.token_prefix = $1 and d.revoked_at is null
		  and d.last_used_at > now() - make_interval(days => $2)`,
		parsed[1], deviceIdleDays, lastUsedResolution).Scan(&id, &userID, &verifier, &stale)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(verifier)) != 1 {
		return "", "", nil
	}
	if stale {
		_, _ = g.DB.Exec(ctx, `update app_device set last_used_at = now() where id = $1`, id)
	}
	return id, userID, nil
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
	return g.Status(ctx).Live()
}

// Status asks the worker which sign-in provider is live, at most once a
// second. When the worker does not answer, the last answer in the marker
// file decides, as in apps/web/src/lib/auth.ts.
func (g *Gate) Status(ctx context.Context) AuthStatus {
	now := g.now()
	g.mu.Lock()
	if g.provider != nil && now.Sub(g.provider.at) < providerCacheTTL {
		status := g.provider.status
		g.mu.Unlock()
		return status
	}
	g.mu.Unlock()
	status, err := g.fetchStatus(ctx)
	if err != nil {
		status = AuthStatus{Provider: g.readMarker().provider}
	} else {
		g.writeMarker(status.Provider)
	}
	g.mu.Lock()
	g.provider = &providerProbe{status: status, at: now}
	g.mu.Unlock()
	return status
}

func (g *Gate) fetchStatus(ctx context.Context) (AuthStatus, error) {
	base := strings.TrimRight(g.WorkerURL, "/")
	if base == "" {
		base = "http://127.0.0.1:4100"
	}
	ctx, cancel := context.WithTimeout(ctx, workerStatusTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/admin/auth/status", nil)
	if err != nil {
		return AuthStatus{}, err
	}
	req.Header.Set("Accept", "application/json")
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return AuthStatus{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return AuthStatus{}, errStatus
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, workerStatusMaxBytes+1))
	if err != nil {
		return AuthStatus{}, err
	}
	if len(raw) > workerStatusMaxBytes {
		return AuthStatus{}, errStatus
	}
	var body struct {
		Provider json.RawMessage `json:"provider"`
		Pending  json.RawMessage `json:"pending"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return AuthStatus{}, err
	}
	return AuthStatus{Provider: object(body.Provider), Pending: object(body.Pending)}, nil
}

func object(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	return trimmed
}

func pluginName(provider json.RawMessage) string {
	if provider == nil {
		return ""
	}
	var p struct {
		PluginName string `json:"pluginName"`
	}
	_ = json.Unmarshal(provider, &p)
	return p.PluginName
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
				state.provider = object(p)
			}
		}
	}
	g.marker = &state
	return state
}

// writeMarker keeps the file format of auth-gate-marker.ts. A read-only config
// folder leaves the marker stale and must not fail the request.
func (g *Gate) writeMarker(provider json.RawMessage) {
	previous := g.readMarker()
	g.mu.Lock()
	g.marker = &markerState{found: true, provider: provider}
	g.mu.Unlock()
	if previous.found && (previous.provider == nil) == (provider == nil) && pluginName(previous.provider) == pluginName(provider) {
		return
	}
	body := []byte(`{"provider":null}`)
	if provider != nil {
		var compact bytes.Buffer
		if json.Compact(&compact, provider) != nil {
			return
		}
		body = append(append([]byte(`{"provider":`), compact.Bytes()...), '}')
	}
	path := g.markerPath()
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	if os.WriteFile(tmp, body, 0o600) != nil {
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
