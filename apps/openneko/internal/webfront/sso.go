package webfront

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	StateCookie       = "openneko_sso_state"
	sessionTTLSeconds = 12 * 60 * 60
	stateTTLSeconds   = 10 * 60
	beginTimeout      = 45 * time.Second
	completeTimeout   = 30 * time.Second
	workerReplyLimit  = 1 << 20
)

type providerInfo struct {
	PluginName        string `json:"pluginName"`
	Provisioning      string `json:"provisioning"`
	LoginHintRequired bool   `json:"loginHintRequired"`
}

func parseProvider(raw json.RawMessage) *providerInfo {
	if raw == nil {
		return nil
	}
	p := &providerInfo{}
	_ = json.Unmarshal(raw, p)
	return p
}

type identity struct {
	Sub    string
	Email  string
	Name   *string
	OrgID  string
	Groups []idpGroup
}

type idpGroup struct {
	ExternalID  string
	DisplayName *string
}

func (a *API) authBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	provider := parseProvider(a.Gate.Status(ctx).Provider)
	if provider == nil {
		sendError(w, http.StatusServiceUnavailable, "sign-in is not set up")
		return
	}
	q := r.URL.Query()
	returnTo := "/"
	if q.Has("returnTo") {
		returnTo = q.Get("returnTo")
	}
	loginHint := strings.TrimFunc(q.Get("loginHint"), jsSpace)
	if provider.LoginHintRequired && loginHint == "" {
		appRedirect(w, "/signin?error=enter+your+email+address+to+sign+in")
		return
	}
	manual := provider.Provisioning == "manual"
	if manual {
		known, err := a.provisioned(ctx, loginHint)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if !known {
			appRedirect(w, "/signin?notice=link-sent")
			return
		}
	}
	state, err := randomURL(24)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	value, err := a.stateCookieValue(state, returnTo)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.setCookie(w, StateCookie, value, stateTTLSeconds)
	var hint any
	if loginHint != "" {
		hint = loginHint
	}
	var out struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	err = a.workerCall(ctx, "/admin/auth/begin", beginTimeout, "sign-in begin", map[string]any{
		"redirectUri": a.redirectURI(r), "state": state, "loginHint": hint,
	}, &out)
	if err != nil {
		if manual {
			log.Printf("[auth] magic-link begin failed: %v", err)
			appRedirect(w, "/signin?notice=link-sent")
			return
		}
		sendError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Location", out.AuthorizationURL)
	w.WriteHeader(http.StatusFound)
}

func (a *API) authCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		description := e
		if q.Has("error_description") {
			description = q.Get("error_description")
		}
		appRedirect(w, "/signin?error="+encodeURIComponent(description))
		return
	}
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		appRedirect(w, "/signin?error=missing+code+or+state+on+callback")
		return
	}
	storedState, returnPath, ok := a.readAndClearState(w, r)
	if !ok {
		appRedirect(w, "/signin?error=state+cookie+missing+or+expired")
		return
	}
	if storedState != state {
		appRedirect(w, "/signin?error=state+mismatch")
		return
	}
	userID, err := a.completeSignIn(ctx, r, code, state)
	if err == nil {
		err = a.writeSession(w, userID)
	}
	if err != nil {
		appRedirect(w, "/signin?error="+encodeURIComponent(err.Error()))
		return
	}
	appRedirect(w, returnPath)
}

func (a *API) completeSignIn(ctx context.Context, r *http.Request, code, state string) (string, error) {
	var out struct {
		Identity json.RawMessage `json:"identity"`
	}
	err := a.workerCall(ctx, "/admin/auth/complete", completeTimeout, "sign-in complete", map[string]any{
		"code": code, "redirectUri": a.redirectURI(r), "state": state,
	}, &out)
	if err != nil {
		return "", err
	}
	id, err := parseIdentity(out.Identity)
	if err != nil {
		return "", err
	}
	orgID, err := a.OrgID(ctx)
	if err != nil {
		return "", err
	}
	userID, err := a.upsertUser(ctx, orgID, id)
	if err != nil {
		return "", err
	}
	return userID, a.syncIdentityGroups(ctx, orgID, userID, id)
}

// appRedirect matches appRedirect() in apps/web/src/lib/public-url.ts: a
// relative Location, so the browser keeps its own origin.
func appRedirect(w http.ResponseWriter, path string) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		path = "/"
	}
	w.Header().Set("Location", path)
	w.WriteHeader(http.StatusFound)
}

func (a *API) redirectURI(r *http.Request) string {
	return a.publicBaseURL(r) + "/api/auth/callback"
}

func (a *API) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		Expires:  time.Now().Add(time.Duration(maxAge) * time.Second),
		HttpOnly: true, Secure: a.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
}

func (a *API) signingSecret() (string, error) {
	secret := a.Gate.sessionSecret()
	if len(secret) < 32 {
		return "", errors.New("OPENNEKO_SESSION_SECRET must be at least 32 characters long for SSO sessions")
	}
	return secret, nil
}

func macOf(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// writeSession matches writeSessionCookie() in apps/web/src/lib/auth.ts.
func (a *API) writeSession(w http.ResponseWriter, userID string) error {
	secret, err := a.signingSecret()
	if err != nil {
		return err
	}
	body := userID + "." + strconv.FormatInt(time.Now().Unix()+sessionTTLSeconds, 10)
	a.setCookie(w, SessionCookie, body+"."+macOf(secret, body), sessionTTLSeconds)
	return nil
}

func internalPath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//")
}

func (a *API) stateCookieValue(state, returnPath string) (string, error) {
	secret, err := a.signingSecret()
	if err != nil {
		return "", err
	}
	encoded := "/"
	if internalPath(returnPath) {
		encoded = base64.RawURLEncoding.EncodeToString([]byte(returnPath))
	}
	payload := state + "|" + encoded
	return payload + "." + macOf(secret, payload), nil
}

func (a *API) readAndClearState(w http.ResponseWriter, r *http.Request) (state, returnPath string, ok bool) {
	c, err := r.Cookie(StateCookie)
	if err != nil || c.Value == "" {
		return "", "", false
	}
	http.SetCookie(w, &http.Cookie{Name: StateCookie, Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1})
	raw := c.Value
	split := strings.LastIndex(raw, ".")
	if split <= 0 {
		return "", "", false
	}
	payload, mac := raw[:split], raw[split+1:]
	secret, err := a.signingSecret()
	if err != nil || subtle.ConstantTimeCompare([]byte(mac), []byte(macOf(secret, payload))) != 1 {
		return "", "", false
	}
	pipe := strings.Index(payload, "|")
	if pipe <= 0 {
		return "", "", false
	}
	returnPath = "/"
	if decoded, err := base64.RawURLEncoding.DecodeString(payload[pipe+1:]); err == nil && internalPath(string(decoded)) {
		returnPath = string(decoded)
	}
	return payload[:pipe], returnPath, true
}

func (a *API) workerCall(ctx context.Context, path string, timeout time.Duration, what string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	base := strings.TrimRight(a.Gate.WorkerURL, "/")
	if base == "" {
		base = "http://127.0.0.1:4100"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := a.Gate.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		log.Printf("%s: %v", what, err)
		return fmt.Errorf("%s failed: the worker did not answer", what)
	}
	defer res.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(res.Body, workerReplyLimit))
	if err != nil {
		return fmt.Errorf("%s failed: %w", what, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		text := string(reply)
		if text == "" {
			text = http.StatusText(res.StatusCode)
		}
		return fmt.Errorf("%s failed (%d): %s", what, res.StatusCode, text)
	}
	if err := json.Unmarshal(reply, out); err != nil {
		return fmt.Errorf("%s failed: %w", what, err)
	}
	return nil
}

func parseIdentity(raw json.RawMessage) (identity, error) {
	var in struct {
		Sub    string            `json:"sub"`
		Email  string            `json:"email"`
		Name   *string           `json:"name"`
		OrgID  *string           `json:"orgId"`
		Groups []json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.Sub == "" || in.Email == "" {
		return identity{}, errors.New("the sign-in provider gave no subject or email")
	}
	id := identity{Sub: in.Sub, Email: in.Email, Name: in.Name}
	if in.OrgID != nil {
		id.OrgID = strings.TrimFunc(*in.OrgID, jsSpace)
	}
	order := []string{}
	names := map[string]*string{}
	for _, g := range in.Groups {
		var externalID string
		var display *string
		var s string
		var obj struct {
			ID   *string `json:"id"`
			Name *string `json:"name"`
		}
		switch {
		case json.Unmarshal(g, &s) == nil:
			externalID, display = strings.TrimFunc(s, jsSpace), &s
		case json.Unmarshal(g, &obj) == nil && obj.ID != nil:
			externalID = strings.TrimFunc(*obj.ID, jsSpace)
			if obj.Name != nil {
				if n := strings.TrimFunc(*obj.Name, jsSpace); n != "" {
					display = &n
				}
			}
		default:
			return identity{}, errors.New("the sign-in provider gave a group that is not valid")
		}
		if externalID == "" {
			continue
		}
		if _, seen := names[externalID]; !seen {
			order = append(order, externalID)
		}
		names[externalID] = display
	}
	for _, externalID := range order {
		id.Groups = append(id.Groups, idpGroup{ExternalID: externalID, DisplayName: names[externalID]})
	}
	return id, nil
}

func (a *API) provisioned(ctx context.Context, email string) (bool, error) {
	orgID, err := a.OrgID(ctx)
	if err != nil {
		return false, err
	}
	var found bool
	err = a.DB.QueryRow(ctx, `
		select exists (select 1 from app_user
		  where org_id = $1 and lower(email) = $2 and disabled_at is null)`, orgID, strings.ToLower(email)).Scan(&found)
	return found, err
}

// upsertUser matches upsertUserFromIdentity() in apps/web/src/lib/auth.ts:
// match by subject, then by an email with no subject yet, else insert.
func (a *API) upsertUser(ctx context.Context, orgID string, id identity) (string, error) {
	status := a.Gate.Status(ctx)
	info := parseProvider(status.Provider)
	if info == nil {
		info = parseProvider(status.Pending)
	}
	source := "oidc"
	if info != nil && info.PluginName != "" {
		source = info.PluginName
	}
	var userID string
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "openneko.app_user:"+orgID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			update app_user set email = $3, name = $4, last_login_at = now(), updated_at = now()
			where id = (select id from app_user where org_id = $1 and sub = $2 limit 1)
			returning id`, orgID, id.Sub, id.Email, id.Name).Scan(&userID)
		if err == nil || !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var match string
		var sub *string
		err = tx.QueryRow(ctx, `select id, sub from app_user where org_id = $1 and lower(email) = $2 limit 1`,
			orgID, strings.ToLower(strings.TrimFunc(id.Email, jsSpace))).Scan(&match, &sub)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if match != "" && sub == nil {
			userID = match
			_, err := tx.Exec(ctx, `
				update app_user set sub = $2, name = coalesce($3, name), last_login_at = now(), updated_at = now()
				where id = $1`, match, id.Sub, id.Name)
			return err
		}
		if match != "" {
			return fmt.Errorf("app_user.email %s is already bound to a different SSO subject; remove the row or update sub manually before re-attempting", id.Email)
		}
		if info != nil && info.Provisioning == "manual" {
			return fmt.Errorf("no user is provisioned for %s; ask an administrator to add you", id.Email)
		}
		suffix, err := randomURL(9)
		if err != nil {
			return err
		}
		userID = "usr_" + suffix
		if _, err := tx.Exec(ctx, `
			insert into app_user (id, sub, email, name, org_id, source, last_login_at)
			values ($1, $2, $3, $4, $5, $6, now())`, userID, id.Sub, id.Email, id.Name, orgID, source); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			insert into user_group_membership (org_id, group_id, user_id, source)
			select $1, g.id, $2, 'local' from user_group g
			where g.org_id = $1 and g.slug = 'administrators'
			  and not exists (
			    select 1 from app_user u
			    join user_group_membership m on m.user_id = u.id
			    join user_group a on a.id = m.group_id
			    where u.org_id = $1 and a.slug = 'administrators' and u.disabled_at is null)
			on conflict do nothing`, orgID, userID)
		return err
	})
	return userID, err
}

var errLockout = errors.New("refusing to remove the last active administrator")

// syncIdentityGroups matches syncIdentityGroups() in apps/web/src/lib/auth.ts
// and reconcileSignInGroups() in packages/db/src/groups.ts. A sync that would
// remove the last administrator rolls back and keeps the previous groups.
func (a *API) syncIdentityGroups(ctx context.Context, orgID, userID string, id identity) error {
	provider := "oidc"
	if p := parseProvider(a.Gate.Status(ctx).Provider); p != nil && p.PluginName != "" {
		provider = p.PluginName
	}
	tenantID := id.OrgID
	if tenantID == "" {
		tenantID = orgID
	}
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "openneko.app_user:"+orgID); err != nil {
			return err
		}
		before, err := activeAdministrators(ctx, tx, orgID)
		if err != nil {
			return err
		}
		groupIDs := []string{}
		externalIDs := []string{}
		for _, g := range id.Groups {
			var groupID string
			if err := tx.QueryRow(ctx, `
				insert into sso_group (org_id, provider, tenant_id, external_id, display_name, active, updated_at)
				values ($1, $2, $3, $4, $5, true, now())
				on conflict (org_id, provider, tenant_id, external_id)
				do update set display_name = excluded.display_name, active = true, updated_at = now()
				returning id::text`, orgID, provider, tenantID, g.ExternalID, g.DisplayName).Scan(&groupID); err != nil {
				return err
			}
			groupIDs = append(groupIDs, groupID)
			externalIDs = append(externalIDs, g.ExternalID)
		}
		if _, err := tx.Exec(ctx, `delete from sso_group_membership where org_id = $1 and user_id = $2`, orgID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			insert into sso_group_membership (org_id, group_id, user_id, synced_at)
			select $1, g::uuid, $2, now() from unnest($3::text[]) g
			on conflict do nothing`, orgID, userID, groupIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			insert into sso_group_sync_audit (org_id, user_id, provider, tenant_id, external_group_ids)
			values ($1, $2, $3, $4, $5::text[])`, orgID, userID, provider, tenantID, externalIDs); err != nil {
			return err
		}
		if err := recomputeRuleMemberships(ctx, tx, orgID, userID); err != nil {
			return err
		}
		after, err := activeAdministrators(ctx, tx, orgID)
		if err != nil {
			return err
		}
		if before > 0 && after == 0 {
			return errLockout
		}
		return nil
	})
	if errors.Is(err, errLockout) {
		log.Printf("[auth] kept previous groups for %s: %v", userID, err)
		return nil
	}
	return err
}

func activeAdministrators(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		select count(distinct m.user_id)::int
		from user_group_membership m
		join user_group g on g.id = m.group_id and g.org_id = m.org_id and g.slug = 'administrators'
		join app_user u on u.id = m.user_id and u.disabled_at is null
		where m.org_id = $1`, orgID).Scan(&n)
	return n, err
}

// recomputeRuleMemberships matches the function of that name in
// packages/db/src/groups.ts, for one user.
func recomputeRuleMemberships(ctx context.Context, tx pgx.Tx, orgID, userID string) error {
	const desired = `
		select distinct r.user_group_id as group_id, m.user_id, 'rule:' || r.id::text as source
		from idp_group_rule r
		join sso_group s on s.id = r.sso_group_id and s.org_id = r.org_id and s.active
		join sso_group_membership m on m.group_id = r.sso_group_id and m.org_id = r.org_id
		where r.org_id = $1 and m.user_id = $2`
	if _, err := tx.Exec(ctx, `
		delete from user_group_membership um
		where um.org_id = $1 and um.source like 'rule:%' and um.user_id = $2
		  and not exists (
		    select 1 from (`+desired+`) d
		    where d.group_id = um.group_id and d.user_id = um.user_id and d.source = um.source)`, orgID, userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		insert into user_group_membership (org_id, group_id, user_id, source)
		select $1, d.group_id, d.user_id, d.source from (`+desired+`) d
		on conflict do nothing`, orgID, userID)
	return err
}
