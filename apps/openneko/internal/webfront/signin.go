package webfront

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
)

const (
	deviceRequestTTL      = 10 * 60
	devicePollInterval    = 5
	maxPendingRequests    = 20
	userCodeAlphabet      = "BCDFGHJKLMNPQRSTVWXZ"
	deviceNameMaxUTF16    = 80
	tooManyDevicesMessage = "too many devices are waiting for approval; try again in a few minutes"
)

var deviceUUID = regexp.MustCompile(`^(?i)[0-9a-f-]{36}$`)

func (a *API) version(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sendJSON(w, http.StatusOK, map[string]string{"version": a.Version})
}

func (a *API) authStatus(w http.ResponseWriter, r *http.Request) {
	status := a.Gate.Status(r.Context())
	var pending any
	if status.Pending != nil {
		var p map[string]json.RawMessage
		_ = json.Unmarshal(status.Pending, &p)
		label := map[string]json.RawMessage{}
		if v, ok := p["providerLabel"]; ok {
			label["providerLabel"] = v
		}
		pending = label
	}
	sendJSON(w, http.StatusOK, map[string]any{"provider": status.Provider, "pending": pending})
}

func (a *API) authSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.currentUser(ctx, r, orgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var role any
	if u != nil {
		if role, err = a.role(ctx, orgID, u.ID); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	signedIn := false
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		signedIn = true
	} else if u != nil {
		deviceID, _, _ := a.Gate.Device(ctx, r.Header.Get("Authorization"))
		signedIn = deviceID != ""
	}
	sendJSON(w, http.StatusOK, map[string]any{
		"user":        u,
		"role":        role,
		"authEnabled": a.Gate.Status(ctx).Live(),
		"signedIn":    signedIn,
	})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	deviceID, userID, _ := a.Gate.Device(ctx, r.Header.Get("Authorization"))
	if deviceID != "" {
		orgID, err := a.OrgID(ctx)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if deviceUUID.MatchString(deviceID) {
			var id, owner, name string
			err := a.DB.QueryRow(ctx, `
				update app_device set revoked_at = now(), revoked_by_user_id = $3
				where org_id = $1 and id = $2 and revoked_at is null
				returning id, user_id, name`, orgID, deviceID, userID).Scan(&id, &owner, &name)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				a.fail(w, r, err)
				return
			}
			if err == nil {
				a.recordAudit(ctx, auditEvent{OrgID: orgID, EntityKind: "device", EntityID: id, Event: "signed_out",
					Payload: map[string]any{"userId": owner, "deviceName": name}})
			}
		}
		sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1})
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) deviceCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, _ := readBody(r)
	name, _ := field(body, "name")
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	deviceCode, userCode, expiresAt, err := a.startDeviceRequest(ctx, orgID, normalizeDeviceName(name))
	if errors.Is(err, errTooManyDevices) {
		sendError(w, http.StatusTooManyRequests, tooManyDevicesMessage)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	formatted := formatUserCode(userCode)
	verification := a.publicBaseURL(r) + "/device"
	sendJSON(w, http.StatusOK, map[string]any{
		"deviceCode":              deviceCode,
		"userCode":                formatted,
		"verificationUri":         verification,
		"verificationUriComplete": verification + "?code=" + formatted,
		"expiresIn":               max(0, int(math.Round(time.Until(expiresAt).Seconds()))),
		"interval":                devicePollInterval,
	})
}

var errTooManyDevices = errors.New(tooManyDevicesMessage)

func (a *API) startDeviceRequest(ctx context.Context, orgID, name string) (deviceCode, userCode string, expiresAt time.Time, err error) {
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "openneko.device-request:"+orgID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from app_device_request where org_id = $1 and expires_at < now()`, orgID); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `select count(*)::int from app_device_request where org_id = $1`, orgID).Scan(&pending); err != nil {
			return err
		}
		if pending >= maxPendingRequests {
			return errTooManyDevices
		}
		code, err := randomURL(32)
		if err != nil {
			return err
		}
		for range 5 {
			candidate, err := newUserCode()
			if err != nil {
				return err
			}
			err = tx.QueryRow(ctx, `
				insert into app_device_request (org_id, device_code_verifier, user_code, name, expires_at)
				values ($1, $2, $3, $4, now() + make_interval(secs => $5))
				on conflict (user_code) do nothing
				returning expires_at`, orgID, sha256Hex(code), candidate, name, deviceRequestTTL).Scan(&expiresAt)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			deviceCode, userCode = code, candidate
			return nil
		}
		return errors.New("could not choose a unique device code")
	})
	return deviceCode, userCode, expiresAt, err
}

var oauthErrors = map[string]string{
	"pending":   "authorization_pending",
	"slow_down": "slow_down",
	"denied":    "access_denied",
	"expired":   "expired_token",
}

func (a *API) deviceToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, _ := readBody(r)
	raw, _ := field(body, "deviceCode")
	code, ok := raw.(string)
	if !ok || code == "" || len(utf16.Encode([]rune(code))) > 128 {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "message": "deviceCode is required"})
		return
	}
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var status, token, deviceID string
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var id, name string
		var approvedBy *string
		var approved, denied, expired, tooSoon bool
		err := tx.QueryRow(ctx, `
			select id, name, approved_by_user_id, approved_at is not null, denied_at is not null,
			       expires_at <= now(),
			       coalesce(last_polled_at > now() - make_interval(secs => $3), false)
			from app_device_request
			where org_id = $1 and device_code_verifier = $2
			for update`, orgID, sha256Hex(code), devicePollInterval-1).
			Scan(&id, &name, &approvedBy, &approved, &denied, &expired, &tooSoon)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && expired) {
			status = "expired"
			return nil
		}
		if err != nil {
			return err
		}
		if denied {
			status = "denied"
			_, err := tx.Exec(ctx, `delete from app_device_request where id = $1`, id)
			return err
		}
		if !approved || approvedBy == nil {
			status = "pending"
			if tooSoon {
				status = "slow_down"
			}
			_, err := tx.Exec(ctx, `update app_device_request set last_polled_at = now() where id = $1`, id)
			return err
		}
		fingerprint := make([]byte, 6)
		if _, err := rand.Read(fingerprint); err != nil {
			return err
		}
		secret, err := randomURL(32)
		if err != nil {
			return err
		}
		prefix := "onk_dev_" + hex.EncodeToString(fingerprint)
		token = prefix + "_" + secret
		sum := sha256.Sum256([]byte(token))
		if err := tx.QueryRow(ctx, `
			insert into app_device (org_id, user_id, name, token_prefix, token_verifier)
			values ($1, $2, $3, $4, $5) returning id`, orgID, *approvedBy, name, prefix, hex.EncodeToString(sum[:])).Scan(&deviceID); err != nil {
			return err
		}
		status = "issued"
		_, err = tx.Exec(ctx, `delete from app_device_request where id = $1`, id)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if status == "issued" {
		w.Header().Set("Cache-Control", "no-store")
		sendJSON(w, http.StatusOK, map[string]string{"token": token, "deviceId": deviceID})
		return
	}
	sendError(w, http.StatusBadRequest, oauthErrors[status])
}

func (a *API) deviceApprove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	deviceID, _, err := a.Gate.Device(ctx, r.Header.Get("Authorization"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if deviceID != "" {
		sendError(w, http.StatusForbidden, "approve a device from a browser")
		return
	}
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.currentUser(ctx, r, orgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if u == nil {
		sendError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	body, _ := readBody(r)
	rawApprove, _ := field(body, "approve")
	approve, ok := rawApprove.(bool)
	if !ok {
		sendError(w, http.StatusBadRequest, "approve must be true or false")
		return
	}
	rawCode, _ := field(body, "userCode")
	userCode := normalizeUserCode(rawCode)
	var id, name string
	if userCode != "" {
		var approvedBy any
		if approve {
			approvedBy = u.ID
		}
		err = a.DB.QueryRow(ctx, `
			update app_device_request
			set approved_by_user_id = $3,
			    approved_at = case when $4 then now() end,
			    denied_at = case when $4 then null else now() end
			where org_id = $1 and user_code = $2 and expires_at > now()
			  and approved_at is null and denied_at is null
			returning id, name`, orgID, userCode, approvedBy, approve).Scan(&id, &name)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			a.fail(w, r, err)
			return
		}
	}
	if id == "" {
		sendError(w, http.StatusNotFound, "this code is not valid or has expired; start again on the device")
		return
	}
	event := "denied"
	if approve {
		event = "approved"
	}
	a.recordAudit(ctx, auditEvent{OrgID: orgID, EntityKind: "device_request", EntityID: id, Event: event,
		Payload: map[string]any{"userId": u.ID, "deviceName": name}})
	sendJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": approve, "name": name})
}

func newUserCode() (string, error) {
	var b strings.Builder
	limit := big.NewInt(int64(len(userCodeAlphabet)))
	for range 8 {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		b.WriteByte(userCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

func formatUserCode(code string) string { return code[:4] + "-" + code[4:] }

func normalizeUserCode(raw any) string {
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	code := strings.Map(func(r rune) rune {
		if r == '-' || jsSpace(r) {
			return -1
		}
		return r
	}, strings.ToUpper(s))
	if len(code) != 8 {
		return ""
	}
	for _, c := range code {
		if !strings.ContainsRune(userCodeAlphabet, c) {
			return ""
		}
	}
	return code
}

// normalizeDeviceName matches normalizeDeviceName() in packages/db/src/devices.ts.
func normalizeDeviceName(raw any) string {
	s, _ := raw.(string)
	name := strings.Join(strings.FieldsFunc(s, jsSpace), " ")
	units := 0
	for i, r := range name {
		units += utf16.RuneLen(r)
		if units > deviceNameMaxUTF16 {
			name = name[:i]
			break
		}
	}
	if name == "" {
		return "Unnamed device"
	}
	return name
}

// jsSpace is the \s class of JavaScript regular expressions.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}
