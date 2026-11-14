package webfront

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func buildTestAttestation(t *testing.T, priv *ecdsa.PrivateKey, rpID string, credID []byte, signCount uint32) string {
	t.Helper()
	rpHash := sha256.Sum256([]byte(rpID))
	var authData bytes.Buffer
	authData.Write(rpHash[:])
	authData.WriteByte(0x01 | 0x04 | 0x40) // UP | UV | AT
	var sc [4]byte
	binary.BigEndian.PutUint32(sc[:], signCount)
	authData.Write(sc[:])
	authData.Write(make([]byte, 16)) // AAGUID
	var clen [2]byte
	binary.BigEndian.PutUint16(clen[:], uint16(len(credID)))
	authData.Write(clen[:])
	authData.Write(credID)

	var xBuf, yBuf [32]byte
	priv.PublicKey.X.FillBytes(xBuf[:])
	priv.PublicKey.Y.FillBytes(yBuf[:])

	// CBOR COSE_Key map with 5 entries: {1: 2, 3: -7, -1: 1, -2: x, -3: y}
	var cose bytes.Buffer
	cose.WriteByte(0xa5)
	cose.Write([]byte{0x01, 0x02})       // 1: 2 (EC2)
	cose.Write([]byte{0x03, 0x26})       // 3: -7 (ES256)
	cose.Write([]byte{0x20, 0x01})       // -1: 1 (P-256)
	cose.Write([]byte{0x21, 0x58, 0x20}) // -2: bstr(32)
	cose.Write(xBuf[:])
	cose.Write([]byte{0x22, 0x58, 0x20}) // -3: bstr(32)
	cose.Write(yBuf[:])
	authData.Write(cose.Bytes())

	// CBOR attestationObject map with 1 entry: {"authData": bstr}
	ad := authData.Bytes()
	var att bytes.Buffer
	att.WriteByte(0xa1)
	att.WriteByte(0x68)
	att.WriteString("authData")
	if len(ad) < 256 {
		att.Write([]byte{0x58, byte(len(ad))})
	} else {
		att.WriteByte(0x59)
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(ad)))
		att.Write(l[:])
	}
	att.Write(ad)
	return base64.RawURLEncoding.EncodeToString(att.Bytes())
}

func buildTestAssertion(t *testing.T, priv *ecdsa.PrivateKey, rpID, credID, challenge string, signCount uint32, uv bool) map[string]any {
	t.Helper()
	clientJSON, err := json.Marshal(map[string]any{
		"type":        "webauthn.get",
		"challenge":   challenge,
		"origin":      "http://localhost:3000",
		"crossOrigin": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	rpHash := sha256.Sum256([]byte(rpID))
	var authData [37]byte
	copy(authData[:32], rpHash[:])
	flags := byte(0x01)
	if uv {
		flags |= 0x04
	}
	authData[32] = flags
	binary.BigEndian.PutUint32(authData[33:37], signCount)

	clientHash := sha256.Sum256(clientJSON)
	signed := append(authData[:], clientHash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"credentialId":      credID,
		"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientJSON),
		"authenticatorData": base64.RawURLEncoding.EncodeToString(authData[:]),
		"signature":         base64.RawURLEncoding.EncodeToString(sig),
	}
}

func TestPasskeyRegisterAndApproveAction(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	seedRiver(t, db, f)
	member := sessionCookie("member")
	admin := sessionCookie("admin")

	if w := call(f, "GET", "/api/v1/my/passkeys", "", member); w.Code != 200 || w.Body.String() != `{"passkeys":[]}` {
		t.Fatalf("empty list: %d %s", w.Code, w.Body.String())
	}

	// Start registration challenge
	cw := call(f, "POST", "/api/v1/my/passkeys/challenge", `{"purpose":"register"}`, member)
	if cw.Code != 200 {
		t.Fatalf("register challenge: %d %s", cw.Code, cw.Body.String())
	}
	var chResp struct {
		Challenge string            `json:"challenge"`
		RP        map[string]string `json:"rp"`
	}
	if err := json.Unmarshal(cw.Body.Bytes(), &chResp); err != nil {
		t.Fatal(err)
	}
	if chResp.Challenge == "" || chResp.RP["id"] == "" {
		t.Fatalf("bad challenge response: %s", cw.Body.String())
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credRaw := []byte("phone-passkey-credential-001")
	credID := base64.RawURLEncoding.EncodeToString(credRaw)
	attB64 := buildTestAttestation(t, priv, chResp.RP["id"], credRaw, 1)
	regClientJSON, _ := json.Marshal(map[string]any{
		"type":      "webauthn.create",
		"challenge": chResp.Challenge,
		"origin":    "http://" + chResp.RP["id"],
	})
	regBody, _ := json.Marshal(map[string]any{
		"credentialId":      credID,
		"clientDataJSON":    base64.RawURLEncoding.EncodeToString(regClientJSON),
		"attestationObject": attB64,
		"name":              "Pixel 9 Passkey",
	})

	rw := call(f, "POST", "/api/v1/my/passkeys", string(regBody), member)
	if rw.Code != 201 {
		t.Fatalf("create passkey: %d %s", rw.Code, rw.Body.String())
	}
	var created struct {
		Passkey struct {
			ID           string `json:"id"`
			CredentialID string `json:"credentialId"`
			Name         string `json:"name"`
		} `json:"passkey"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Passkey.CredentialID != credID || created.Passkey.Name != "Pixel 9 Passkey" {
		t.Fatalf("created passkey: %+v", created.Passkey)
	}
	if a := queryText(t, db, `select event || ' ' || payload::text from audit_chain where entity_kind = 'passkey' and entity_id = '`+created.Passkey.ID+`'`); a != `registered {"name": "Pixel 9 Passkey", "userId": "member", "credentialId": "`+credID+`"}` {
		t.Fatalf("passkey register audit: %s", a)
	}

	// Replaying the same registration challenge must fail
	if w := call(f, "POST", "/api/v1/my/passkeys", string(regBody), member); w.Code != 400 {
		t.Fatalf("replayed register challenge: %d %s", w.Code, w.Body.String())
	}

	// Challenge for an action the member cannot view must return 404
	if w := call(f, "POST", "/api/v1/my/passkeys/challenge", `{"purpose":"approve","actionRequestId":"`+arOrgRun+`"}`, member); w.Code != 404 {
		t.Fatalf("hidden action challenge: %d %s", w.Code, w.Body.String())
	}

	// Request an approval challenge for arHeldRun
	acw := call(f, "POST", "/api/v1/my/passkeys/challenge", `{"purpose":"approve","actionRequestId":"`+arHeldRun+`"}`, member)
	if acw.Code != 200 {
		t.Fatalf("approve challenge: %d %s", acw.Code, acw.Body.String())
	}
	var appCh struct {
		Challenge        string                  `json:"challenge"`
		RP               map[string]string       `json:"rp"`
		AllowCredentials []passkeyCredDescriptor `json:"allowCredentials"`
	}
	if err := json.Unmarshal(acw.Body.Bytes(), &appCh); err != nil {
		t.Fatal(err)
	}
	if len(appCh.AllowCredentials) != 1 || appCh.AllowCredentials[0].ID != credID {
		t.Fatalf("allowCredentials: %+v", appCh.AllowCredentials)
	}

	// Invalid signature must fail and leave arHeldRun pending_approval
	badAssertion := buildTestAssertion(t, priv, appCh.RP["id"], credID, appCh.Challenge, 2, true)
	badAssertion["signature"] = base64.RawURLEncoding.EncodeToString([]byte("not-a-valid-der-sig"))
	badPatch, _ := json.Marshal(map[string]any{"decision": "approve", "passkey": badAssertion})
	if w := call(f, "PATCH", "/api/v1/action-requests/"+arHeldRun, string(badPatch), member); w.Code != 400 {
		t.Fatalf("bad signature should fail: %d %s", w.Code, w.Body.String())
	}
	if st := queryText(t, db, `select status from action_request where id = '`+arHeldRun+`'`); st != "pending_approval" {
		t.Fatalf("status changed on bad sig: %s", st)
	}

	// Issue fresh challenge and approve arHeldRun with valid passkey assertion
	acw = call(f, "POST", "/api/v1/my/passkeys/challenge", `{"purpose":"approve","actionRequestId":"`+arHeldRun+`"}`, member)
	if err := json.Unmarshal(acw.Body.Bytes(), &appCh); err != nil {
		t.Fatal(err)
	}
	validAssertion := buildTestAssertion(t, priv, appCh.RP["id"], credID, appCh.Challenge, 2, true)
	goodPatch, _ := json.Marshal(map[string]any{"decision": "approve", "passkey": validAssertion})
	pw := call(f, "PATCH", "/api/v1/action-requests/"+arHeldRun, string(goodPatch), member)
	if pw.Code != 200 || pw.Body.String() != `{"actionRequest":{"id":"`+arHeldRun+`","status":"approved"}}` {
		t.Fatalf("approve with passkey: %d %s", pw.Code, pw.Body.String())
	}
	wantAudit := `approved {"kind": "note", "method": "passkey", "approvedBy": "member", "passkeyName": "Pixel 9 Passkey", "soloOperator": false, "userVerified": true, "passkeyCredentialId": "` + credID + `"}`
	if got := lastAudit(t, db, arHeldRun); got != wantAudit {
		t.Fatalf("audit mismatch: got=%s want=%s", got, wantAudit)
	}
	if sc := queryText(t, db, `select sign_count::text || ' ' || (last_used_at is not null)::text from app_passkey where id = '`+created.Passkey.ID+`'`); sc != "2 true" {
		t.Fatalf("sign_count/last_used_at: %s", sc)
	}

	// Non-advancing sign_count on next approval must be rejected
	seed(t, db, `insert into action_request (id, org_id, workflow_run_id, scope, kind, status)
		values ('30000000-0000-4000-8000-000000000091', 'org', '`+runHeld+`', 'org', 'note', 'pending_approval')`)
	acw2 := call(f, "POST", "/api/v1/my/passkeys/challenge", `{"purpose":"approve","actionRequestId":"30000000-0000-4000-8000-000000000091"}`, member)
	var appCh2 struct {
		Challenge string            `json:"challenge"`
		RP        map[string]string `json:"rp"`
	}
	_ = json.Unmarshal(acw2.Body.Bytes(), &appCh2)
	staleCountAssertion := buildTestAssertion(t, priv, appCh2.RP["id"], credID, appCh2.Challenge, 2, true)
	stalePatch, _ := json.Marshal(map[string]any{"decision": "approve", "passkey": staleCountAssertion})
	if w := call(f, "PATCH", "/api/v1/action-requests/30000000-0000-4000-8000-000000000091", string(stalePatch), member); w.Code != 400 {
		t.Fatalf("stale sign_count should fail: %d %s", w.Code, w.Body.String())
	}

	// Approve in-chat run action via /work/threads/{threadId}/runs/{runId}/approve-action with passkey
	threadID := "40000000-0000-4000-8000-000000000092"
	workRunID := "50000000-0000-4000-8000-000000000092"
	runActionID := "30000000-0000-4000-8000-000000000092"
	seed(t, db, `
		insert into work_thread (id, org_id, created_by_user_id, title) values ('`+threadID+`', 'org', 'member', 'Chat');
		insert into work_run (id, org_id, thread_id, backend, status) values ('`+workRunID+`', 'org', '`+threadID+`', 'hermes', 'running');
		insert into action_request (id, org_id, work_run_id, scope, kind, status) values ('`+runActionID+`', 'org', '`+workRunID+`', 'org', 'send_slack_message', 'pending_approval');
	`)
	acw3 := call(f, "POST", "/api/v1/my/passkeys/challenge", `{"purpose":"approve","actionRequestId":"`+runActionID+`"}`, member)
	var appCh3 struct {
		Challenge string            `json:"challenge"`
		RP        map[string]string `json:"rp"`
	}
	_ = json.Unmarshal(acw3.Body.Bytes(), &appCh3)
	chatAssertion := buildTestAssertion(t, priv, appCh3.RP["id"], credID, appCh3.Challenge, 3, true)
	chatBody, _ := json.Marshal(map[string]any{
		"actionRequestId": runActionID,
		"decision":        "approve",
		"passkey":         chatAssertion,
	})
	cw3 := call(f, "POST", "/api/v1/work/threads/"+threadID+"/runs/"+workRunID+"/approve-action", string(chatBody), member)
	if cw3.Code != 200 {
		t.Fatalf("approve-action with passkey: %d %s", cw3.Code, cw3.Body.String())
	}
	wantChatAudit := `approved {"kind": "send_slack_message", "method": "passkey", "approvedBy": "member", "passkeyName": "Pixel 9 Passkey", "soloOperator": false, "userVerified": true, "passkeyCredentialId": "` + credID + `"}`
	if got := lastAudit(t, db, runActionID); got != wantChatAudit {
		t.Fatalf("chat audit mismatch: got=%s want=%s", got, wantChatAudit)
	}

	// Admin cannot delete member's passkey
	if w := call(f, "DELETE", "/api/v1/my/passkeys/"+created.Passkey.ID, "", admin); w.Code != 404 {
		t.Fatalf("other user delete: %d %s", w.Code, w.Body.String())
	}
	// Member deletes own passkey
	if w := call(f, "DELETE", "/api/v1/my/passkeys/"+created.Passkey.ID, "", member); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Fatalf("member delete: %d %s", w.Code, w.Body.String())
	}
	var count int
	_ = db.QueryRow(ctx, `select count(*) from app_passkey where id = $1`, created.Passkey.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("passkey still exists: %d", count)
	}
}
