package webfront

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

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

	clientDataRaw, err := decodeB64URL(clientDataB64)
	if err != nil {
		sendError(w, http.StatusBadRequest, "invalid clientDataJSON encoding")
		return
	}
	var clientData struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(clientDataRaw, &clientData); err != nil {
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

	rp := a.passkeyRP(r)
	if err := rp.checkOrigin(clientData.Origin); err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	attRaw, err := decodeB64URL(attestationB64)
	if err != nil {
		sendError(w, http.StatusBadRequest, "invalid attestationObject encoding")
		return
	}
	pubKeyB64, signCount, err := parseAttestationObject(attRaw, credID, rp.id)
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}

	var p passkeySummary
	var created time.Time
	var used *time.Time
	err = a.DB.QueryRow(ctx, `
		insert into app_passkey (org_id, user_id, credential_id, public_key, algorithm, sign_count, name)
		values ($1, $2, $3, $4, -7, $5, $6)
		on conflict (org_id, credential_id) do update
		  set public_key = excluded.public_key,
		      sign_count = excluded.sign_count,
		      name = excluded.name
		where app_passkey.user_id = excluded.user_id
		returning id::text, credential_id, name, created_at, last_used_at`,
		act.OrgID, act.UserID, credID, pubKeyB64, signCount, name).
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
	clientDataRaw []byte
	authData      []byte
	signature     []byte
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

	clientDataRaw, err := decodeB64URL(clientDataB64)
	if err != nil {
		return nil, badPasskey("invalid passkey clientDataJSON")
	}
	var clientData struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(clientDataRaw, &clientData); err != nil {
		return nil, badPasskey("invalid passkey clientDataJSON")
	}
	if clientData.Type != "webauthn.get" || clientData.Challenge == "" {
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

	authData, err := decodeB64URL(authDataB64)
	if err != nil || len(authData) < 37 {
		return nil, badPasskey("invalid passkey authenticatorData")
	}
	sigBytes, err := decodeB64URL(sigB64)
	if err != nil || len(sigBytes) == 0 {
		return nil, badPasskey("invalid passkey signature")
	}
	return &passkeyAssertion{
		credID:        credID,
		origin:        clientData.Origin,
		clientDataRaw: clientDataRaw,
		authData:      authData,
		signature:     sigBytes,
	}, nil
}

func verifyPasskeyApproval(ctx context.Context, tx pgx.Tx, rp passkeyRP, act actor, p *passkeyAssertion) (*verifiedPasskey, error) {
	if err := rp.checkOrigin(p.origin); err != nil {
		return nil, err
	}
	authData := p.authData
	var pkID, pubKeyB64, name string
	var storedSignCount int64
	err := tx.QueryRow(ctx, `
		select id::text, public_key, name, sign_count
		from app_passkey
		where org_id = $1 and user_id = $2 and credential_id = $3
		for update`,
		act.OrgID, act.UserID, p.credID).Scan(&pkID, &pubKeyB64, &name, &storedSignCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, badPasskey("passkey is not registered for this user")
	}
	if err != nil {
		return nil, err
	}

	if !matchesRPIDHash(authData[:32], rp.id) {
		return nil, badPasskey("passkey relying party ID mismatch")
	}
	flags := authData[32]
	if flags&0x01 == 0 {
		return nil, badPasskey("passkey assertion requires user presence")
	}
	if flags&0x04 == 0 {
		return nil, badPasskey("passkey assertion requires user verification")
	}
	signCount := int64(binary.BigEndian.Uint32(authData[33:37]))
	if storedSignCount > 0 && signCount <= storedSignCount {
		return nil, badPasskey("passkey sign count did not advance")
	}

	pubBytes, err := decodeB64URL(pubKeyB64)
	if err != nil || len(pubBytes) != 65 || pubBytes[0] != 0x04 {
		return nil, badPasskey("stored passkey public key is invalid")
	}
	if _, err := ecdh.P256().NewPublicKey(pubBytes); err != nil {
		return nil, badPasskey("stored passkey public key is invalid")
	}
	pubKey := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(pubBytes[1:33]),
		Y:     new(big.Int).SetBytes(pubBytes[33:65]),
	}

	clientHash := sha256.Sum256(p.clientDataRaw)
	signed := make([]byte, 0, len(authData)+len(clientHash))
	signed = append(signed, authData...)
	signed = append(signed, clientHash[:]...)
	digest := sha256.Sum256(signed)
	if !ecdsa.VerifyASN1(pubKey, digest[:], p.signature) {
		return nil, badPasskey("passkey signature verification failed")
	}

	if _, err := tx.Exec(ctx, `
		update app_passkey
		set sign_count = greatest(sign_count, $1), last_used_at = now()
		where id = $2`,
		signCount, pkID); err != nil {
		return nil, err
	}

	return &verifiedPasskey{
		ID:           pkID,
		CredentialID: p.credID,
		Name:         name,
		UserVerified: true,
	}, nil
}

func matchesRPIDHash(rpIDHash []byte, rpID string) bool {
	if rpID == "" {
		return false
	}
	sum := sha256.Sum256([]byte(rpID))
	return bytes.Equal(rpIDHash, sum[:])
}

const (
	maxAttestationBytes = 64 << 10
	maxCBORDepth        = 8
)

func parseAttestationObject(attRaw []byte, expectedCredID, rpID string) (string, int64, error) {
	if len(attRaw) > maxAttestationBytes {
		return "", 0, badPasskey("attestationObject is too large")
	}
	val, _, err := readCBORItem(attRaw, 0)
	if err != nil {
		return "", 0, badPasskey("invalid CBOR attestationObject")
	}
	m, ok := val.(map[any]any)
	if !ok {
		return "", 0, badPasskey("attestationObject must be a CBOR map")
	}
	authData, ok := m["authData"].([]byte)
	if !ok || len(authData) < 55 {
		return "", 0, badPasskey("attestationObject missing valid authData")
	}
	if !matchesRPIDHash(authData[:32], rpID) {
		return "", 0, badPasskey("passkey relying party ID mismatch")
	}
	flags := authData[32]
	if flags&0x01 == 0 || flags&0x40 == 0 {
		return "", 0, badPasskey("attestation authData requires user presence and attested credential data")
	}
	signCount := int64(binary.BigEndian.Uint32(authData[33:37]))
	credLen := int(binary.BigEndian.Uint16(authData[53:55]))
	if credLen <= 0 || len(authData) <= 55+credLen {
		return "", 0, badPasskey("invalid attested credential length")
	}
	attestedCredRaw := authData[55 : 55+credLen]
	wantCredRaw, err := decodeB64URL(expectedCredID)
	if err != nil || !bytes.Equal(attestedCredRaw, wantCredRaw) {
		return "", 0, badPasskey("attested credential ID does not match credentialId")
	}

	coseVal, _, err := readCBORItem(authData[55+credLen:], 0)
	if err != nil {
		return "", 0, badPasskey("invalid COSE public key")
	}
	coseMap, ok := coseVal.(map[any]any)
	if !ok {
		return "", 0, badPasskey("COSE public key must be a map")
	}
	kty, _ := coseMap[int64(1)].(int64)
	alg, _ := coseMap[int64(3)].(int64)
	crv, _ := coseMap[int64(-1)].(int64)
	xBytes, _ := coseMap[int64(-2)].([]byte)
	yBytes, _ := coseMap[int64(-3)].([]byte)
	if kty != 2 || alg != -7 || crv != 1 || len(xBytes) != 32 || len(yBytes) != 32 {
		return "", 0, badPasskey("passkey public key must be ES256 (P-256)")
	}
	uncompressed := make([]byte, 65)
	uncompressed[0] = 0x04
	copy(uncompressed[1:33], xBytes)
	copy(uncompressed[33:65], yBytes)
	if _, err := ecdh.P256().NewPublicKey(uncompressed); err != nil {
		return "", 0, badPasskey("passkey public key is not on P-256 curve")
	}
	return base64.RawURLEncoding.EncodeToString(uncompressed), signCount, nil
}

func readCBORItem(b []byte, depth int) (any, []byte, error) {
	if depth > maxCBORDepth {
		return nil, nil, errors.New("CBOR nesting too deep")
	}
	if len(b) == 0 {
		return nil, nil, errors.New("unexpected end of CBOR")
	}
	major := b[0] >> 5
	info := b[0] & 0x1f
	n, rest, err := readCBORUint(info, b[1:])
	if err != nil {
		return nil, nil, err
	}
	switch major {
	case 0:
		return int64(n), rest, nil
	case 1:
		return -1 - int64(n), rest, nil
	case 2:
		if uint64(len(rest)) < n {
			return nil, nil, errors.New("truncated CBOR byte string")
		}
		out := bytes.Clone(rest[:n])
		return out, rest[n:], nil
	case 3:
		if uint64(len(rest)) < n {
			return nil, nil, errors.New("truncated CBOR text string")
		}
		return string(rest[:n]), rest[n:], nil
	case 4:
		if n > 1024 {
			return nil, nil, errors.New("CBOR array too large")
		}
		arr := make([]any, 0, n)
		cur := rest
		for i := uint64(0); i < n; i++ {
			var item any
			item, cur, err = readCBORItem(cur, depth+1)
			if err != nil {
				return nil, nil, err
			}
			arr = append(arr, item)
		}
		return arr, cur, nil
	case 5:
		if n > 1024 {
			return nil, nil, errors.New("CBOR map too large")
		}
		m := make(map[any]any, n)
		cur := rest
		for i := uint64(0); i < n; i++ {
			var k, v any
			k, cur, err = readCBORItem(cur, depth+1)
			if err != nil {
				return nil, nil, err
			}
			v, cur, err = readCBORItem(cur, depth+1)
			if err != nil {
				return nil, nil, err
			}
			m[k] = v
		}
		return m, cur, nil
	case 7:
		switch info {
		case 20:
			return false, rest, nil
		case 21:
			return true, rest, nil
		case 22, 23:
			return nil, rest, nil
		default:
			return n, rest, nil
		}
	default:
		return nil, nil, errors.New("unsupported CBOR major type")
	}
}

func readCBORUint(info byte, b []byte) (uint64, []byte, error) {
	switch {
	case info < 24:
		return uint64(info), b, nil
	case info == 24:
		if len(b) < 1 {
			return 0, nil, errors.New("truncated CBOR uint8")
		}
		return uint64(b[0]), b[1:], nil
	case info == 25:
		if len(b) < 2 {
			return 0, nil, errors.New("truncated CBOR uint16")
		}
		return uint64(binary.BigEndian.Uint16(b[:2])), b[2:], nil
	case info == 26:
		if len(b) < 4 {
			return 0, nil, errors.New("truncated CBOR uint32")
		}
		return uint64(binary.BigEndian.Uint32(b[:4])), b[4:], nil
	case info == 27:
		if len(b) < 8 {
			return 0, nil, errors.New("truncated CBOR uint64")
		}
		return binary.BigEndian.Uint64(b[:8]), b[8:], nil
	default:
		return 0, nil, errors.New("unsupported CBOR length")
	}
}
