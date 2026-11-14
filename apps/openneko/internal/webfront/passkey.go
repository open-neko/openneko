package webfront

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
)

type passkeySummary struct {
	ID           string  `json:"id"`
	CredentialID string  `json:"credentialId"`
	Name         string  `json:"name"`
	CreatedAt    string  `json:"createdAt"`
	LastUsedAt   *string `json:"lastUsedAt"`
}

type passkeyCredDescriptor struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type passkeyAlgParam struct {
	Type string `json:"type"`
	Alg  int    `json:"alg"`
}

type verifiedPasskey struct {
	ID           string
	CredentialID string
	Name         string
	UserVerified bool
}

const maxAttestationBytes = 64 << 10

func badPasskey(msg string) error {
	return &decisionError{status: http.StatusBadRequest, message: msg}
}

type passkeyRP struct {
	id     string
	origin string
}

func (a *API) passkeyRP(r *http.Request) passkeyRP {
	u, err := url.Parse(a.publicBaseURL(r))
	if err != nil {
		return passkeyRP{}
	}
	rp := passkeyRP{id: strings.ToLower(u.Hostname())}
	if a.configuredPublicURL() != "" {
		rp.origin = originOf(u)
	}
	return rp
}

func (rp passkeyRP) checkOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || rp.id == "" || strings.ToLower(u.Hostname()) != rp.id {
		return badPasskey("passkey origin does not match the relying party")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if rp.id != "localhost" {
			return badPasskey("passkey origin must use https")
		}
	default:
		return badPasskey("passkey origin must use https")
	}
	if rp.origin != "" && originOf(u) != rp.origin {
		return badPasskey("passkey origin does not match the relying party")
	}
	return nil
}

func decodeB64URL(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty base64url")
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func (a *API) passkeysList(w http.ResponseWriter, r *http.Request) {
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
		select id::text, credential_id, name, created_at, last_used_at
		from app_passkey
		where org_id = $1 and user_id = $2
		order by created_at desc`, act.OrgID, act.UserID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (passkeySummary, error) {
		var p passkeySummary
		var created time.Time
		var used *time.Time
		err := row.Scan(&p.ID, &p.CredentialID, &p.Name, &created, &used)
		p.CreatedAt = jsTime(created)
		p.LastUsedAt = jsTimePtr(used)
		return p, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if list == nil {
		list = []passkeySummary{}
	}
	sendJSON(w, http.StatusOK, struct {
		Passkeys []passkeySummary `json:"passkeys"`
	}{Passkeys: list})
}

func (a *API) passkeyChallenge(w http.ResponseWriter, r *http.Request) {
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
	if !ok {
		sendError(w, http.StatusBadRequest, "purpose must be 'register' or 'approve'")
		return
	}
	purpose, _ := fieldString(body, "purpose")
	if purpose != "register" && purpose != "approve" {
		sendError(w, http.StatusBadRequest, "purpose must be 'register' or 'approve'")
		return
	}

	var actionID *string
	if purpose == "approve" {
		rawID, _ := fieldString(body, "actionRequestId")
		rawID = strings.TrimSpace(rawID)
		if !isUUID(rawID) {
			sendError(w, http.StatusBadRequest, "actionRequestId is required")
			return
		}
		req, err := scanActionRequest(a.DB.QueryRow(ctx, `select `+actionRequestColumns+` from action_request where org_id = $1 and id = $2`,
			act.OrgID, rawID))
		if errors.Is(err, pgx.ErrNoRows) {
			sendError(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			a.fail(w, r, err)
			return
		}
		visible, err := a.visibleActionRequest(ctx, act, req)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if !visible {
			sendError(w, http.StatusNotFound, "not found")
			return
		}
		err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
			return mayDecide(ctx, tx, act, rawID, req.PolicyID)
		})
		var refused *decisionError
		if errors.As(err, &refused) {
			sendError(w, refused.status, refused.message)
			return
		}
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if req.Status != "draft" && req.Status != "pending_approval" {
			sendError(w, http.StatusConflict, "invalid action_request transition: "+req.Status+" → approved")
			return
		}
		actionID = &rawID
	}

	_, _ = a.DB.Exec(ctx, `delete from app_passkey_challenge where org_id = $1 and expires_at <= now()`, act.OrgID)

	var challengeBytes [32]byte
	if _, err := io.ReadFull(rand.Reader, challengeBytes[:]); err != nil {
		a.fail(w, r, err)
		return
	}
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	if _, err := a.DB.Exec(ctx, `
		insert into app_passkey_challenge (org_id, user_id, purpose, action_request_id, challenge, expires_at)
		values ($1, $2, $3, $4, $5, now() + interval '5 minutes')`,
		act.OrgID, act.UserID, purpose, actionID, challenge); err != nil {
		a.fail(w, r, err)
		return
	}

	var email, displayName string
	err = a.DB.QueryRow(ctx, `select email, coalesce(nullif(trim(name), ''), email) from app_user where id = $1`, act.UserID).
		Scan(&email, &displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		email = act.UserID
		displayName = act.UserID
	} else if err != nil {
		a.fail(w, r, err)
		return
	}

	credIDs, err := collectStrings(ctx, a, `
		select credential_id from app_passkey
		where org_id = $1 and user_id = $2
		order by created_at desc`, act.OrgID, act.UserID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	descriptors := make([]passkeyCredDescriptor, 0, len(credIDs))
	for _, cid := range credIDs {
		descriptors = append(descriptors, passkeyCredDescriptor{ID: cid, Type: "public-key"})
	}

	rpID := a.passkeyRP(r).id
	sendJSON(w, http.StatusOK, struct {
		Purpose            string                  `json:"purpose"`
		Challenge          string                  `json:"challenge"`
		RP                 map[string]string       `json:"rp"`
		User               map[string]string       `json:"user"`
		PubKeyCredParams   []passkeyAlgParam       `json:"pubKeyCredParams"`
		ExcludeCredentials []passkeyCredDescriptor `json:"excludeCredentials"`
		AllowCredentials   []passkeyCredDescriptor `json:"allowCredentials"`
		UserVerification   string                  `json:"userVerification"`
	}{
		Purpose:   purpose,
		Challenge: challenge,
		RP:        map[string]string{"id": rpID, "name": "OpenNeko"},
		User: map[string]string{
			"id":          base64.RawURLEncoding.EncodeToString([]byte(act.UserID)),
			"name":        email,
			"displayName": displayName,
		},
		PubKeyCredParams:   []passkeyAlgParam{{Type: "public-key", Alg: -7}},
		ExcludeCredentials: descriptors,
		AllowCredentials:   descriptors,
		UserVerification:   "required",
	})
}

func (a *API) passkeyCreate(w http.ResponseWriter, r *http.Request) {
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
	if !ok {
		sendError(w, http.StatusBadRequest, "invalid passkey registration")
		return
	}
	credID, _ := fieldString(body, "credentialId")
	clientDataB64, _ := fieldString(body, "clientDataJSON")
	attestationB64, _ := fieldString(body, "attestationObject")
	name, _ := fieldString(body, "name")
	credID = strings.TrimSpace(credID)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	if credID == "" || clientDataB64 == "" || attestationB64 == "" {
		sendError(w, http.StatusBadRequest, "credentialId, clientDataJSON, and attestationObject are required")
		return
	}

	clientData, err := readClientData(clientDataB64)
	if err != nil {
		sendError(w, http.StatusBadRequest, "invalid clientDataJSON payload")
		return
	}
	if clientData.Type != "webauthn.create" || clientData.Challenge == "" {
		sendError(w, http.StatusBadRequest, "clientDataJSON type must be webauthn.create")
		return
	}

	var challengeID string
	err = a.DB.QueryRow(ctx, `
		delete from app_passkey_challenge
		where org_id = $1 and user_id = $2 and purpose = 'register' and challenge = $3 and expires_at > now()
		returning id::text`,
		act.OrgID, act.UserID, clientData.Challenge).Scan(&challengeID)
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusBadRequest, "passkey challenge is invalid or expired")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}

	if base64.RawURLEncoding.DecodedLen(len(strings.TrimRight(attestationB64, "="))) > maxAttestationBytes {
		sendError(w, http.StatusBadRequest, "attestationObject is too large")
		return
	}

	rp := a.passkeyRP(r)
	wa, err := rp.webAuthn(clientData.Origin)
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	credRaw, err := decodeB64URL(credID)
	if err != nil {
		sendError(w, http.StatusBadRequest, "invalid credentialId encoding")
		return
	}
	payload, err := attestationJSON(credRaw, clientDataB64, attestationB64)
	if err != nil {
		sendError(w, http.StatusBadRequest, "invalid passkey registration encoding")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(payload)
	if err == nil && !bytes.Equal(parsed.Response.AttestationObject.AuthData.AttData.CredentialID, credRaw) {
		err = badPasskey("attested credential ID does not match credentialId")
	}
	var cred *webauthn.Credential
	if err == nil {
		cred, err = wa.CreateCredential(passkeyUser{id: []byte(act.UserID)}, webauthn.SessionData{
			Challenge:        clientData.Challenge,
			RelyingPartyID:   rp.id,
			UserID:           []byte(act.UserID),
			UserVerification: protocol.VerificationRequired,
			CredParams:       []protocol.CredentialParameter{{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256}},
		}, parsed)
	}
	var refused *decisionError
	if errors.As(err, &refused) {
		sendError(w, refused.status, refused.message)
		return
	}
	if err != nil {
		sendError(w, http.StatusBadRequest, passkeyCheckError(err).Error())
		return
	}
	pubKeyB64 := base64.RawURLEncoding.EncodeToString(cred.PublicKey)
	signCount := int64(cred.Authenticator.SignCount)

	var p passkeySummary
	var created time.Time
	var used *time.Time
	err = a.DB.QueryRow(ctx, `
		insert into app_passkey (org_id, user_id, credential_id, public_key, algorithm, sign_count, backup_eligible, name)
		values ($1, $2, $3, $4, -7, $5, $6, $7)
		on conflict (org_id, credential_id) do update
		  set public_key = excluded.public_key,
		      sign_count = excluded.sign_count,
		      backup_eligible = excluded.backup_eligible,
		      name = excluded.name
		where app_passkey.user_id = excluded.user_id
		returning id::text, credential_id, name, created_at, last_used_at`,
		act.OrgID, act.UserID, credID, pubKeyB64, signCount, cred.Flags.BackupEligible, name).
		Scan(&p.ID, &p.CredentialID, &p.Name, &created, &used)
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusConflict, "passkey is already registered")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	p.CreatedAt = jsTime(created)
	p.LastUsedAt = jsTimePtr(used)

	a.recordAudit(ctx, auditEvent{
		OrgID:      act.OrgID,
		EntityKind: "passkey",
		EntityID:   p.ID,
		Event:      "registered",
		Payload: map[string]any{
			"credentialId": p.CredentialID,
			"name":         p.Name,
			"userId":       act.UserID,
		},
	})
	sendJSON(w, http.StatusCreated, struct {
		Passkey passkeySummary `json:"passkey"`
	}{Passkey: p})
}

func (a *API) passkeyDelete(w http.ResponseWriter, r *http.Request) {
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
	id := r.PathValue("passkeyId")
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	var credID, name string
	err = a.DB.QueryRow(ctx, `
		delete from app_passkey
		where org_id = $1 and user_id = $2 and id = $3
		returning credential_id, name`,
		act.OrgID, act.UserID, id).Scan(&credID, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordAudit(ctx, auditEvent{
		OrgID:      act.OrgID,
		EntityKind: "passkey",
		EntityID:   id,
		Event:      "removed",
		Payload: map[string]any{
			"credentialId": credID,
			"name":         name,
			"userId":       act.UserID,
		},
	})
	sendJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{OK: true})
}

type passkeyAssertion struct {
	credID        string
	origin        string
	challenge     string
	clientDataB64 string
	authDataB64   string
	sigB64        string
}

// takePasskeyAssertion deletes the challenge outside the decision
// transaction, so that a failed check still spends it.
func (a *API) takePasskeyAssertion(ctx context.Context, act actor, actionID string, raw any) (*passkeyAssertion, error) {
	obj, ok := raw.(map[string]any)
	if !ok || obj == nil {
		return nil, badPasskey("passkey assertion must be an object")
	}
	credID, _ := fieldString(obj, "credentialId")
	clientDataB64, _ := fieldString(obj, "clientDataJSON")
	authDataB64, _ := fieldString(obj, "authenticatorData")
	sigB64, _ := fieldString(obj, "signature")
	credID = strings.TrimSpace(credID)
	if credID == "" || clientDataB64 == "" || authDataB64 == "" || sigB64 == "" {
		return nil, badPasskey("passkey assertion requires credentialId, clientDataJSON, authenticatorData, and signature")
	}
	clientData, err := readClientData(clientDataB64)
	if err != nil || clientData.Type != "webauthn.get" || clientData.Challenge == "" {
		return nil, badPasskey("passkey clientDataJSON type must be webauthn.get")
	}

	var challengeID string
	err = a.DB.QueryRow(ctx, `
		delete from app_passkey_challenge
		where org_id = $1 and user_id = $2 and purpose = 'approve'
		  and action_request_id = $3 and challenge = $4 and expires_at > now()
		returning id::text`,
		act.OrgID, act.UserID, actionID, clientData.Challenge).Scan(&challengeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, badPasskey("passkey challenge is invalid or expired")
	}
	if err != nil {
		return nil, err
	}
	return &passkeyAssertion{
		credID:        credID,
		origin:        clientData.Origin,
		challenge:     clientData.Challenge,
		clientDataB64: clientDataB64,
		authDataB64:   authDataB64,
		sigB64:        sigB64,
	}, nil
}

func verifyPasskeyApproval(ctx context.Context, tx pgx.Tx, rp passkeyRP, act actor, p *passkeyAssertion) (*verifiedPasskey, error) {
	wa, err := rp.webAuthn(p.origin)
	if err != nil {
		return nil, err
	}
	credRaw, err := decodeB64URL(p.credID)
	if err != nil {
		return nil, badPasskey("invalid passkey credentialId")
	}
	payload, err := assertionJSON(credRaw, p)
	if err != nil {
		return nil, badPasskey("invalid passkey assertion encoding")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(payload)
	if err != nil {
		return nil, passkeyCheckError(err)
	}

	var pkID, pubKeyB64, name string
	var storedSignCount int64
	var backupEligible *bool
	err = tx.QueryRow(ctx, `
		select id::text, public_key, name, sign_count, backup_eligible
		from app_passkey
		where org_id = $1 and user_id = $2 and credential_id = $3
		for update`,
		act.OrgID, act.UserID, p.credID).Scan(&pkID, &pubKeyB64, &name, &storedSignCount, &backupEligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, badPasskey("passkey is not registered for this user")
	}
	if err != nil {
		return nil, err
	}
	pubKey, err := decodeB64URL(pubKeyB64)
	if err != nil {
		return nil, badPasskey("stored passkey public key is invalid")
	}
	be := parsed.Response.AuthenticatorData.Flags.HasBackupEligible()
	if backupEligible != nil {
		be = *backupEligible
	}
	user := passkeyUser{id: []byte(act.UserID), cred: webauthn.Credential{
		ID:            credRaw,
		PublicKey:     pubKey,
		Flags:         webauthn.CredentialFlags{BackupEligible: be},
		Authenticator: webauthn.Authenticator{SignCount: uint32(storedSignCount)},
	}}
	cred, err := wa.ValidateLogin(user, webauthn.SessionData{
		Challenge:            p.challenge,
		RelyingPartyID:       rp.id,
		UserID:               user.id,
		AllowedCredentialIDs: [][]byte{credRaw},
		UserVerification:     protocol.VerificationRequired,
	}, parsed)
	if err != nil {
		return nil, passkeyCheckError(err)
	}
	if cred.Authenticator.CloneWarning {
		return nil, badPasskey("passkey sign count did not advance")
	}

	if _, err := tx.Exec(ctx, `
		update app_passkey
		set sign_count = greatest(sign_count, $1), backup_eligible = $2, last_used_at = now()
		where id = $3`,
		int64(cred.Authenticator.SignCount), cred.Flags.BackupEligible, pkID); err != nil {
		return nil, err
	}

	return &verifiedPasskey{
		ID:           pkID,
		CredentialID: p.credID,
		Name:         name,
		UserVerified: parsed.Response.AuthenticatorData.Flags.HasUserVerified(),
	}, nil
}

type passkeyUser struct {
	id   []byte
	cred webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte          { return u.id }
func (u passkeyUser) WebAuthnName() string        { return string(u.id) }
func (u passkeyUser) WebAuthnDisplayName() string { return string(u.id) }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	if u.cred.ID == nil {
		return nil
	}
	return []webauthn.Credential{u.cred}
}

type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

func readClientData(b64 string) (clientData, error) {
	var cd clientData
	raw, err := decodeB64URL(b64)
	if err != nil {
		return cd, err
	}
	return cd, json.Unmarshal(raw, &cd)
}

func attestationJSON(credRaw []byte, clientDataB64, attestationB64 string) ([]byte, error) {
	clientData, err := decodeB64URL(clientDataB64)
	if err != nil {
		return nil, err
	}
	att, err := decodeB64URL(attestationB64)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(credRaw),
		"rawId": protocol.URLEncodedBase64(credRaw),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    protocol.URLEncodedBase64(clientData),
			"attestationObject": protocol.URLEncodedBase64(att),
		},
	})
}

func assertionJSON(credRaw []byte, p *passkeyAssertion) ([]byte, error) {
	resp := map[string]any{}
	for key, value := range map[string]string{
		"clientDataJSON":    p.clientDataB64,
		"authenticatorData": p.authDataB64,
		"signature":         p.sigB64,
	} {
		b, err := decodeB64URL(value)
		if err != nil {
			return nil, err
		}
		resp[key] = protocol.URLEncodedBase64(b)
	}
	return json.Marshal(map[string]any{
		"id":       base64.RawURLEncoding.EncodeToString(credRaw),
		"rawId":    protocol.URLEncodedBase64(credRaw),
		"type":     "public-key",
		"response": resp,
	})
}

func (rp passkeyRP) webAuthn(origin string) (*webauthn.WebAuthn, error) {
	if err := rp.checkOrigin(origin); err != nil {
		return nil, err
	}
	allowed := rp.origin
	if allowed == "" {
		allowed = origin
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rp.id,
		RPDisplayName: "OpenNeko",
		RPOrigins:     []string{allowed},
	})
	if err != nil {
		return nil, badPasskey("passkey relying party is not valid")
	}
	return wa, nil
}

func passkeyCheckError(err error) error {
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		return badPasskey("passkey check failed")
	}
	msg := strings.TrimSpace(pe.Details)
	if info := strings.TrimSpace(pe.DevInfo); info != "" {
		msg += ": " + info
	}
	return badPasskey("passkey check failed: " + strings.ToLower(msg))
}
