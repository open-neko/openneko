package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	signinWriteTimeout = 10 * time.Second
	publicURLScope     = "public-url"
	magicLinkPlugin    = "magic-link"
)

var emailProviderKeys = map[string]string{
	"resend":   "RESEND_API_KEY",
	"postmark": "POSTMARK_SERVER_TOKEN",
	"sendgrid": "SENDGRID_API_KEY",
}

var deliveryTestEmail = regexp.MustCompile(`^[^@` + jsSpaceClass + `]+@[^@` + jsSpaceClass + `]+\.[^@` + jsSpaceClass + `]+$`)

const jsSpaceClass = `\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`

// objectFields reads a JSON body as an object; a body that is not an object
// reads as an empty one.
func objectFields(body any) map[string]any {
	if m, ok := body.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func (a *API) adminSigninStatus(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	a.Gate.Reset()
	gate := a.Gate.Status(ctx)
	var total, admins int
	err := a.DB.QueryRow(ctx, `
		select (select count(*)::int from app_user where org_id = $1),
			(select count(*)::int from user_group_membership m
				join user_group g on g.id = m.group_id
				join app_user u on u.id = m.user_id and u.disabled_at is null
				where m.org_id = $1 and g.slug = 'administrators')`, act.OrgID).Scan(&total, &admins)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var self *string
	if act.UserID != "" {
		var email string
		err := a.DB.QueryRow(ctx, `select email from app_user where id = $1 and disabled_at is null limit 1`, act.UserID).Scan(&email)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			a.fail(w, r, err)
			return
		}
		if err == nil {
			self = &email
		}
	}
	sendJSON(w, http.StatusOK, struct {
		Provider       json.RawMessage `json:"provider"`
		Pending        json.RawMessage `json:"pending"`
		Users          map[string]int  `json:"users"`
		SelfEmail      *string         `json:"selfEmail"`
		SelfNeedsEmail bool            `json:"selfNeedsEmail"`
	}{
		Provider:       rawOrNull(gate.Provider),
		Pending:        rawOrNull(gate.Pending),
		Users:          map[string]int{"total": total, "activeAdmins": admins},
		SelfEmail:      self,
		SelfNeedsEmail: self != nil && strings.HasSuffix(*self, soloEmailDomain),
	})
}

func rawOrNull(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return json.RawMessage("null")
	}
	return raw
}

// signinWrite posts to a worker sign-in route as lib/auth.ts does: the
// worker's error text, or the fallback with the status.
func (a *API) signinWrite(ctx context.Context, path string, body any, fallback string) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	status, reply, err := a.workerDo(ctx, http.MethodPost, path, signinWriteTimeout, raw)
	if err != nil {
		log.Printf("%s: %v", path, err)
		return errors.New("the worker did not answer")
	}
	if status < 200 || status > 299 {
		var parsed struct {
			Error *string `json:"error"`
		}
		if json.Unmarshal(reply, &parsed) == nil && parsed.Error != nil {
			return errors.New(*parsed.Error)
		}
		return fmt.Errorf("%s (%d)", fallback, status)
	}
	a.Gate.Reset()
	return nil
}

func (a *API) adminSigninProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.settingsAdmin(w, r); !ok {
		return
	}
	body, parsed := readBody(r)
	if !parsed {
		sendError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	provider, present := objectFields(body)["provider"]
	if !(present && provider == nil) && provider != "scalekit" && provider != magicLinkPlugin {
		sendError(w, http.StatusBadRequest, "provider must be scalekit, magic-link, or null")
		return
	}
	if err := a.signinWrite(r.Context(), "/admin/auth/provider", map[string]any{"provider": provider}, "sign-in provider change failed"); err != nil {
		sendError(w, http.StatusBadGateway, err.Error())
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) adminSigninEmailProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.settingsAdmin(w, r); !ok {
		return
	}
	gate := a.Gate.Status(r.Context())
	chosen := gate.Provider
	if chosen == nil {
		chosen = gate.Pending
	}
	if pluginName(chosen) != magicLinkPlugin {
		sendError(w, http.StatusConflict, "email-link sign-in is not the chosen sign-in provider")
		return
	}
	raw, parsed := readBody(r)
	if !parsed {
		sendError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body := objectFields(raw)
	emailProvider, _ := body["provider"].(string)
	chosenKey, known := emailProviderKeys[emailProvider]
	if !known {
		sendError(w, http.StatusBadRequest, "provider must be resend, postmark, or sendgrid")
		return
	}
	from, _ := body["from"].(string)
	from = jsTrim(from)
	if from == "" || jsLength(from) > 320 {
		sendError(w, http.StatusBadRequest, "from address is required")
		return
	}
	var apiKey *string
	switch v := body["apiKey"].(type) {
	case nil:
		if _, present := body["apiKey"]; present {
			sendError(w, http.StatusBadRequest, "apiKey must be a string")
			return
		}
	case string:
		if v != "" {
			trimmed := jsTrim(v)
			apiKey = &trimmed
		}
	default:
		sendError(w, http.StatusBadRequest, "apiKey must be a string")
		return
	}
	if apiKey == nil && gate.Provider == nil && !pendingHasEnv(gate.Pending, chosenKey) {
		sendError(w, http.StatusBadRequest, fmt.Sprintf("paste the %s API key — none is stored yet", emailProvider))
		return
	}
	values := map[string]any{"MAGIC_LINK_FROM": from}
	for _, key := range emailProviderKeys {
		if key != chosenKey {
			values[key] = nil
		}
	}
	if apiKey != nil {
		values[chosenKey] = *apiKey
	}
	if err := a.signinWrite(r.Context(), "/admin/auth/secrets", map[string]any{"values": values}, "auth secrets write failed"); err != nil {
		sendError(w, http.StatusBadGateway, err.Error())
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func pendingHasEnv(pending json.RawMessage, key string) bool {
	var p struct {
		ConfiguredEnv []string `json:"configuredEnv"`
	}
	_ = json.Unmarshal(pending, &p)
	for _, k := range p.ConfiguredEnv {
		if k == key {
			return true
		}
	}
	return false
}

func (a *API) adminSigninTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.settingsAdmin(w, r); !ok {
		return
	}
	raw, parsed := readBody(r)
	if !parsed {
		sendError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	email, _ := objectFields(raw)["email"].(string)
	email = strings.ToLower(jsTrim(email))
	if !deliveryTestEmail.MatchString(email) {
		sendError(w, http.StatusBadRequest, "a valid email address is required")
		return
	}
	nonce, err := randomURL(12)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var out struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	err = a.workerCall(r.Context(), "/admin/auth/begin", beginTimeout, "sign-in begin", map[string]any{
		"redirectUri": a.redirectURI(r), "state": "delivery-test-" + nonce, "loginHint": email,
	}, &out)
	if err != nil {
		sendError(w, http.StatusBadGateway, err.Error())
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type publicURLSetting struct {
	Saved       *string `json:"saved"`
	Environment *string `json:"environment"`
}

func (a *API) publicURLSetting(ctx context.Context, orgID string) (*publicURLSetting, error) {
	var saved *string
	err := a.DB.QueryRow(ctx, `select config->>'url' from llm_provider_config where org_id = $1 and scope = $2`, orgID, publicURLScope).Scan(&saved)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if saved != nil && *saved == "" {
		saved = nil
	}
	configured := a.PublicURL
	if configured == "" {
		configured = os.Getenv("OPENNEKO_PUBLIC_URL")
	}
	var environment *string
	if env := strings.TrimRight(strings.TrimSpace(configured), "/"); env != "" {
		environment = &env
	}
	return &publicURLSetting{Saved: saved, Environment: environment}, nil
}

func (a *API) adminPublicURL(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	setting, err := a.publicURLSetting(r.Context(), act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, setting)
}

func (a *API) adminPublicURLSave(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	raw, _ := readBody(r)
	input, _ := objectFields(raw)["url"].(string)
	input = jsTrim(input)
	normalized := ""
	if input != "" {
		var valid bool
		if normalized, valid = normalizePublicURL(input); !valid {
			sendError(w, http.StatusBadRequest, "Enter an http or https address without a query, such as https://openneko.example.com.")
			return
		}
	}
	ctx := r.Context()
	var err error
	if normalized == "" {
		_, err = a.DB.Exec(ctx, `delete from llm_provider_config where org_id = $1 and scope = $2`, act.OrgID, publicURLScope)
	} else {
		_, err = a.DB.Exec(ctx, `
			insert into llm_provider_config (org_id, scope, provider, config)
			values ($1, $2, $2, jsonb_build_object('url', $3::text))
			on conflict (org_id, scope) do update set config = excluded.config, updated_at = now()`,
			act.OrgID, publicURLScope, normalized)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setting, err := a.publicURLSetting(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, setting)
}

// normalizePublicURL keeps an http or https origin and its path, with no
// trailing slash, as normalizePublicUrl() in public-url-settings.ts did.
func normalizePublicURL(input string) (string, bool) {
	u, err := url.Parse(jsTrim(input))
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if (scheme != "http" && scheme != "https") || host == "" {
		return "", false
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !(scheme == "http" && port == "80") && !(scheme == "https" && port == "443") {
		host += ":" + port
	}
	p := ""
	if escaped := u.EscapedPath(); escaped != "" {
		p = path.Clean(escaped)
	}
	return strings.TrimRight(scheme+"://"+host+p, "/"), true
}
