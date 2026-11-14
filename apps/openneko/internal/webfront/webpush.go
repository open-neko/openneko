package webfront

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const (
	WebPushSecretsSection = "web_push"
	vapidPrivateKeyName   = "VAPID_PRIVATE_KEY"
	vapidPublicKeyName    = "VAPID_PUBLIC_KEY"
	vapidSubject          = "mailto:notifications@openneko.local"

	WebPushEventApprovalWaiting        = "approval_waiting"
	WebPushEventRunFinished            = "run_finished"
	WebPushEventWorkflowNeedsAttention = "workflow_needs_attention"
)

type webPushNotice struct {
	Event string `json:"event"`
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

// webPushNotices holds the fixed short text and app link for each of the three
// roadmap events. No business data or run content is ever included.
var webPushNotices = map[string]webPushNotice{
	WebPushEventApprovalWaiting: {
		Event: WebPushEventApprovalWaiting,
		Title: "OpenNeko",
		Body:  "An approval is waiting.",
		URL:   "/actions",
	},
	WebPushEventRunFinished: {
		Event: WebPushEventRunFinished,
		Title: "OpenNeko",
		Body:  "A run finished.",
		URL:   "/runs",
	},
	WebPushEventWorkflowNeedsAttention: {
		Event: WebPushEventWorkflowNeedsAttention,
		Title: "OpenNeko",
		Body:  "A workflow needs attention.",
		URL:   "/workflows",
	},
}

type pushSubscriptionSummary struct {
	ID        string  `json:"id"`
	Endpoint  string  `json:"endpoint"`
	DeviceID  *string `json:"deviceId"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
}

type vapidKeys struct {
	priv      *ecdsa.PrivateKey
	pubBytes  []byte
	pubB64URL string
}

// EnsureVAPIDKeys generates the VAPID P-256 key pair on first start and keeps
// it in the secrets store. It never writes the keys to a response or a log.
func (a *API) EnsureVAPIDKeys() error {
	_, err := a.vapidKeyPair()
	return err
}

func (a *API) vapidKeyPair() (vapidKeys, error) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()

	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return vapidKeys{}, err
	}
	if sec := store[WebPushSecretsSection]; sec != nil {
		if keys, err := parseVAPIDKeyPair(sec[vapidPrivateKeyName], sec[vapidPublicKeyName]); err == nil {
			return keys, nil
		}
	}

	ecdhKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return vapidKeys{}, err
	}
	privB64 := base64.RawURLEncoding.EncodeToString(ecdhKey.Bytes())
	pubB64 := base64.RawURLEncoding.EncodeToString(ecdhKey.PublicKey().Bytes())
	store, err = secrets.Set(store, WebPushSecretsSection, vapidPrivateKeyName, privB64)
	if err != nil {
		return vapidKeys{}, err
	}
	store, err = secrets.Set(store, WebPushSecretsSection, vapidPublicKeyName, pubB64)
	if err != nil {
		return vapidKeys{}, err
	}
	if err := secrets.Write(store, a.SecretsDir); err != nil {
		return vapidKeys{}, err
	}
	return parseVAPIDKeyPair(privB64, pubB64)
}

func parseVAPIDKeyPair(privB64, pubB64 string) (vapidKeys, error) {
	privRaw, err := decodeB64URL(privB64)
	if err != nil || len(privRaw) != 32 {
		return vapidKeys{}, errors.New("invalid VAPID private key")
	}
	ecdhPriv, err := ecdh.P256().NewPrivateKey(privRaw)
	if err != nil {
		return vapidKeys{}, err
	}
	pubBytes := ecdhPriv.PublicKey().Bytes()
	if len(pubBytes) != 65 || pubBytes[0] != 0x04 {
		return vapidKeys{}, errors.New("invalid VAPID public key")
	}
	if pubB64 != "" {
		gotPub, err := decodeB64URL(pubB64)
		if err != nil || !bytes.Equal(gotPub, pubBytes) {
			return vapidKeys{}, errors.New("mismatched VAPID public key")
		}
	}
	priv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(pubBytes[1:33]),
			Y:     new(big.Int).SetBytes(pubBytes[33:65]),
		},
		D: new(big.Int).SetBytes(privRaw),
	}
	return vapidKeys{
		priv:      priv,
		pubBytes:  pubBytes,
		pubB64URL: base64.RawURLEncoding.EncodeToString(pubBytes),
	}, nil
}

func (a *API) pushSubscriptionsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if act.UserID == "" {
		sendError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	rows, err := a.DB.Query(ctx, `
		select id::text, endpoint, device_id::text, created_at, updated_at
		from app_push_subscription
		where org_id = $1 and user_id = $2
		order by created_at desc`, act.OrgID, act.UserID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pushSubscriptionSummary, error) {
		var s pushSubscriptionSummary
		var created, updated time.Time
		err := row.Scan(&s.ID, &s.Endpoint, &s.DeviceID, &created, &updated)
		s.CreatedAt = jsTime(created)
		s.UpdatedAt = jsTime(updated)
		return s, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if list == nil {
		list = []pushSubscriptionSummary{}
	}
	sendJSON(w, http.StatusOK, struct {
		Subscriptions []pushSubscriptionSummary `json:"subscriptions"`
	}{Subscriptions: list})
}

func validatePushSubscriptionInput(body any) (endpoint, p256dhB64, authB64 string, err error) {
	rawEndpoint, _ := fieldString(body, "endpoint")
	rawEndpoint = strings.TrimSpace(rawEndpoint)
	if rawEndpoint == "" || len(rawEndpoint) > 2048 {
		return "", "", "", errors.New("a valid push endpoint URL is required")
	}
	u, err := url.Parse(rawEndpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", "", "", errors.New("a valid push endpoint URL is required")
	}
	keysObj, ok := field(body, "keys")
	if !ok || keysObj == nil {
		return "", "", "", errors.New("keys.p256dh and keys.auth are required")
	}
	rawP256dh, _ := fieldString(keysObj, "p256dh")
	rawAuth, _ := fieldString(keysObj, "auth")
	pubBytes, err := decodeB64URL(rawP256dh)
	if err != nil || len(pubBytes) != 65 || pubBytes[0] != 0x04 {
		return "", "", "", errors.New("keys.p256dh must be a 65-byte P-256 public key")
	}
	if _, err := ecdh.P256().NewPublicKey(pubBytes); err != nil {
		return "", "", "", errors.New("keys.p256dh must be a valid P-256 public key")
	}
	authBytes, err := decodeB64URL(rawAuth)
	if err != nil || len(authBytes) != 16 {
		return "", "", "", errors.New("keys.auth must be a 16-byte authentication secret")
	}
	return rawEndpoint,
		base64.RawURLEncoding.EncodeToString(pubBytes),
		base64.RawURLEncoding.EncodeToString(authBytes),
		nil
}

func (a *API) pushSubscriptionCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if act.UserID == "" {
		sendError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	body, ok := readBody(r)
	if !ok || body == nil {
		sendError(w, http.StatusBadRequest, "a valid push subscription payload is required")
		return
	}
	endpoint, p256dh, auth, err := validatePushSubscriptionInput(body)
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.EnsureVAPIDKeys(); err != nil {
		a.fail(w, r, err)
		return
	}

	var deviceID *string
	if a.Gate != nil {
		if devID, _, err := a.Gate.Device(ctx, r.Header.Get("Authorization")); err == nil && devID != "" {
			deviceID = &devID
		}
	}

	var out pushSubscriptionSummary
	var created, updated time.Time
	err = a.DB.QueryRow(ctx, `
		insert into app_push_subscription (org_id, user_id, device_id, endpoint, p256dh, auth)
		values ($1, $2, $3, $4, $5, $6)
		on conflict (org_id, endpoint) do update
		  set user_id = excluded.user_id,
		      device_id = coalesce(excluded.device_id, app_push_subscription.device_id),
		      p256dh = excluded.p256dh,
		      auth = excluded.auth,
		      updated_at = now()
		returning id::text, endpoint, device_id::text, created_at, updated_at`,
		act.OrgID, act.UserID, deviceID, endpoint, p256dh, auth).Scan(
		&out.ID, &out.Endpoint, &out.DeviceID, &created, &updated)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out.CreatedAt = jsTime(created)
	out.UpdatedAt = jsTime(updated)
	sendJSON(w, http.StatusOK, struct {
		Subscription pushSubscriptionSummary `json:"subscription"`
	}{Subscription: out})
}

func (a *API) pushSubscriptionDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if act.UserID == "" {
		sendError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	id := strings.TrimSpace(r.PathValue("subscriptionId"))
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	tag, err := a.DB.Exec(ctx, `
		delete from app_push_subscription
		where org_id = $1 and user_id = $2 and id = $3`,
		act.OrgID, act.UserID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type storedPushSubscription struct {
	ID       string
	Endpoint string
	P256dh   string
	Auth     string
}

// WebPushDeliver is the web_push River job. It encrypts the fixed notice for
// the event with RFC 8291 (aes128gcm), signs the RFC 8292 VAPID header from
// the secrets store, and removes any subscription that the push service says
// is gone (HTTP 404 or 410).
func (a *API) WebPushDeliver(ctx context.Context, args jobs.WebPushArgs) error {
	notice, ok := webPushNotices[args.Event]
	if !ok {
		return river.JobCancel(fmt.Errorf("unknown web push event %q", args.Event))
	}
	payload, err := json.Marshal(notice)
	if err != nil {
		return err
	}

	var rows pgx.Rows
	if len(args.Users) > 0 {
		rows, err = a.DB.Query(ctx, `
			select s.id::text, s.endpoint, s.p256dh, s.auth
			from app_push_subscription s
			join app_user u on u.id = s.user_id and u.org_id = s.org_id and u.disabled_at is null
			left join app_device d on d.id = s.device_id
			where s.org_id = $1 and s.user_id = any($2::text[])
			  and (s.device_id is null or d.revoked_at is null)
			order by s.created_at asc`, args.OrgID, args.Users)
	} else {
		rows, err = a.DB.Query(ctx, `
			select s.id::text, s.endpoint, s.p256dh, s.auth
			from app_push_subscription s
			join app_user u on u.id = s.user_id and u.org_id = s.org_id and u.disabled_at is null
			left join app_device d on d.id = s.device_id
			where s.org_id = $1
			  and (s.device_id is null or d.revoked_at is null)
			order by s.created_at asc`, args.OrgID)
	}
	if err != nil {
		return err
	}
	subs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (storedPushSubscription, error) {
		var s storedPushSubscription
		err := row.Scan(&s.ID, &s.Endpoint, &s.P256dh, &s.Auth)
		return s, err
	})
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}

	keys, err := a.vapidKeyPair()
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 15 * time.Second}
	var firstErr error
	for _, sub := range subs {
		status, sendErr := sendWebPush(ctx, client, keys, sub, payload)
		if status == http.StatusGone || status == http.StatusNotFound {
			_, _ = a.DB.Exec(ctx, `delete from app_push_subscription where id = $1`, sub.ID)
			continue
		}
		if sendErr != nil && firstErr == nil {
			firstErr = sendErr
		}
	}
	return firstErr
}

func sendWebPush(ctx context.Context, client *http.Client, keys vapidKeys, sub storedPushSubscription, plaintext []byte) (int, error) {
	u, err := url.Parse(sub.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return http.StatusGone, nil
	}
	uaPub, err := decodeB64URL(sub.P256dh)
	if err != nil {
		return http.StatusGone, nil
	}
	authSecret, err := decodeB64URL(sub.Auth)
	if err != nil {
		return http.StatusGone, nil
	}
	body, err := encryptWebPushAES128GCM(plaintext, uaPub, authSecret)
	if err != nil {
		return http.StatusGone, nil
	}
	authHeader, err := signVAPIDAuthorization(u.Scheme+"://"+u.Host, vapidSubject, keys, time.Now().Add(12*time.Hour))
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "normal")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	if resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("web push endpoint returned HTTP %d", resp.StatusCode)
}

func signVAPIDAuthorization(audience, subject string, keys vapidKeys, exp time.Time) (string, error) {
	headerJSON := `{"typ":"JWT","alg":"ES256"}`
	claims, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}{
		Aud: audience,
		Exp: exp.Unix(),
		Sub: subject,
	})
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(headerJSON)) + "." +
		base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, keys.priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)
	token := unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	return "vapid t=" + token + ", k=" + keys.pubB64URL, nil
}

// encryptWebPushAES128GCM encrypts plaintext according to RFC 8291 and RFC 8188.
func encryptWebPushAES128GCM(plaintext, uaPublicBytes, authSecret []byte) ([]byte, error) {
	uaPub, err := ecdh.P256().NewPublicKey(uaPublicBytes)
	if err != nil {
		return nil, err
	}
	if len(authSecret) != 16 {
		return nil, errors.New("invalid auth secret length")
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	asPubBytes := asPriv.PublicKey().Bytes()
	sharedSecret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}

	keyInfo := make([]byte, 0, 14+len(uaPublicBytes)+len(asPubBytes))
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaPublicBytes...)
	keyInfo = append(keyInfo, asPubBytes...)

	prkKey := hkdfExtractSHA256(authSecret, sharedSecret)
	ikm := hkdfExpandSHA256(prkKey, keyInfo, 32)

	prk := hkdfExtractSHA256(salt, ikm)
	cek := hkdfExpandSHA256(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfExpandSHA256(prk, []byte("Content-Encoding: nonce\x00"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	padded := make([]byte, len(plaintext)+1)
	copy(padded, plaintext)
	padded[len(plaintext)] = 0x02
	ciphertext := gcm.Seal(nil, nonce, padded, nil)

	header := make([]byte, 16+4+1+len(asPubBytes))
	copy(header[0:16], salt)
	binary.BigEndian.PutUint32(header[16:20], 4096)
	header[20] = byte(len(asPubBytes))
	copy(header[21:], asPubBytes)
	return append(header, ciphertext...), nil
}

func hkdfExtractSHA256(salt, ikm []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	return mac.Sum(nil)
}

func hkdfExpandSHA256(prk, info []byte, length int) []byte {
	var out []byte
	var prev []byte
	for counter := byte(1); len(out) < length; counter++ {
		mac := hmac.New(sha256.New, prk)
		mac.Write(prev)
		mac.Write(info)
		mac.Write([]byte{counter})
		prev = mac.Sum(nil)
		out = append(out, prev...)
	}
	return out[:length]
}

func (a *API) insertWebPushJob(ctx context.Context, orgID, event string, users []string) {
	if orgID == "" || event == "" || a.DB == nil {
		return
	}
	ins := a.Jobs
	if ins == nil {
		var reg *string
		if err := a.DB.QueryRow(ctx, `select to_regclass('river.river_job')::text`).Scan(&reg); err != nil || reg == nil {
			return
		}
		var err error
		ins, err = jobs.NewInserter(a.DB, "")
		if err != nil {
			return
		}
	}
	if err := ins.WebPush(ctx, orgID, event, users); err != nil {
		log.Printf("[web-push] enqueue %s failed: %v", event, err)
	}
}

func (a *API) subscribedOrgUsers(ctx context.Context, orgID string) ([]string, error) {
	if a.DB == nil {
		return nil, nil
	}
	rows, err := a.DB.Query(ctx, `
		select distinct s.user_id
		from app_push_subscription s
		join app_user u on u.id = s.user_id and u.org_id = s.org_id and u.disabled_at is null
		left join app_device d on d.id = s.device_id
		where s.org_id = $1 and (s.device_id is null or d.revoked_at is null)
		order by s.user_id asc`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var uid string
		err := row.Scan(&uid)
		return uid, err
	})
}

func (a *API) queueApprovalWaitingPush(ctx context.Context, orgID, actionRequestID string) {
	candidates, err := a.subscribedOrgUsers(ctx, orgID)
	if err != nil || len(candidates) == 0 {
		return
	}
	req, err := scanActionRequest(a.DB.QueryRow(ctx, `select `+actionRequestColumns+` from action_request where org_id = $1 and id = $2`, orgID, actionRequestID))
	if err != nil {
		return
	}
	var users []string
	for _, uid := range candidates {
		role, err := a.role(ctx, orgID, uid)
		if err != nil {
			continue
		}
		act := actor{OrgID: orgID, UserID: uid, Admin: role == "admin"}
		vis, err := a.visibleActionRequest(ctx, act, req)
		if err == nil && vis {
			users = append(users, uid)
		}
	}
	if len(users) == 0 {
		return
	}
	a.insertWebPushJob(ctx, orgID, WebPushEventApprovalWaiting, users)
}

func (a *API) queueWorkRunFinishedPush(ctx context.Context, orgID, runID string) {
	candidates, err := a.subscribedOrgUsers(ctx, orgID)
	if err != nil || len(candidates) == 0 {
		return
	}
	var isWorkflowRun bool
	var ownerUserID *string
	err = a.DB.QueryRow(ctx, `
		select exists (select 1 from workflow_run where work_run_id = r.id),
		       coalesce(r.requested_by_user_id, t.created_by_user_id)
		from work_run r
		join work_thread t on t.id = r.thread_id
		where r.org_id = $1 and r.id = $2`, orgID, runID).Scan(&isWorkflowRun, &ownerUserID)
	if err != nil || isWorkflowRun {
		return
	}
	if ownerUserID != nil && *ownerUserID != "" {
		for _, uid := range candidates {
			if uid == *ownerUserID {
				a.insertWebPushJob(ctx, orgID, WebPushEventRunFinished, []string{uid})
				return
			}
		}
		return
	}
	a.insertWebPushJob(ctx, orgID, WebPushEventRunFinished, candidates)
}

func (a *API) queueWorkflowRunPush(ctx context.Context, workflowRunID, status string) {
	if a.DB == nil {
		return
	}
	var orgID, workflowID, wfStatus string
	err := a.DB.QueryRow(ctx, `
		select r.org_id, r.workflow_id::text, w.status
		from workflow_run r
		join workflow_definition w on w.id = r.workflow_id
		where r.id = $1`, workflowRunID).Scan(&orgID, &workflowID, &wfStatus)
	if err != nil {
		return
	}
	event := ""
	switch {
	case status == "needs_input" || status == "failed" || status == "broken" || wfStatus == "broken":
		event = WebPushEventWorkflowNeedsAttention
	case status == "completed":
		event = WebPushEventRunFinished
	default:
		return
	}
	a.queueWorkflowPush(ctx, orgID, workflowID, event)
}

func (a *API) queueWorkflowPush(ctx context.Context, orgID, workflowID, event string) {
	candidates, err := a.subscribedOrgUsers(ctx, orgID)
	if err != nil || len(candidates) == 0 {
		return
	}
	var users []string
	for _, uid := range candidates {
		role, err := a.role(ctx, orgID, uid)
		if err != nil {
			continue
		}
		act := actor{OrgID: orgID, UserID: uid, Admin: role == "admin"}
		hidden, err := a.workflowHidden(ctx, act, workflowID)
		if err == nil && !hidden {
			users = append(users, uid)
		}
	}
	if len(users) == 0 {
		return
	}
	a.insertWebPushJob(ctx, orgID, event, users)
}
