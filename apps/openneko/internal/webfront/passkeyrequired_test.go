package webfront

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func registerTestPasskey(t *testing.T, f http.Handler, header map[string]string, priv *ecdsa.PrivateKey, credRaw []byte) string {
	t.Helper()
	cw := call(f, "POST", "/api/v1/my/passkeys/challenge", `{"purpose":"register"}`, header)
	var ch struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(cw.Body.Bytes(), &ch); err != nil || cw.Code != 200 {
		t.Fatalf("register challenge: %d %s", cw.Code, cw.Body.String())
	}
	clientJSON, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": ch.Challenge, "origin": "https://neko.example"})
	body, _ := json.Marshal(map[string]any{
		"credentialId":      base64.RawURLEncoding.EncodeToString(credRaw),
		"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientJSON),
		"attestationObject": buildTestAttestation(t, priv, "neko.example", credRaw, 1),
	})
	w := call(f, "POST", "/api/v1/my/passkeys", string(body), header)
	var created struct {
		Passkey struct {
			ID string `json:"id"`
		} `json:"passkey"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != 201 {
		t.Fatalf("register passkey: %d %s", w.Code, w.Body.String())
	}
	return created.Passkey.ID
}

func testChallenge(t *testing.T, f http.Handler, header map[string]string, body string) string {
	t.Helper()
	w := call(f, "POST", "/api/v1/my/passkeys/challenge", body, header)
	var ch struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil || w.Code != 200 {
		t.Fatalf("challenge %s: %d %s", body, w.Code, w.Body.String())
	}
	return ch.Challenge
}

func TestPasskeyRequiredForSessionApproval(t *testing.T) {
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	seedRiver(t, db, f)
	member := sessionCookie("member")
	const required = `{"code":"passkey_required","error":"This approval needs your passkey."}`

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credRaw := []byte("required-passkey-001")
	credID := base64.RawURLEncoding.EncodeToString(credRaw)
	firstID := registerTestPasskey(t, f, member, priv, credRaw)

	const sessionID, rejectID, tokenID = "30000000-0000-4000-8000-0000000000a1", "30000000-0000-4000-8000-0000000000a2", "30000000-0000-4000-8000-0000000000a3"
	for _, id := range []string{sessionID, rejectID, tokenID} {
		seed(t, db, `insert into action_request (id, org_id, workflow_run_id, scope, kind, status)
			values ($1, 'org', $2, 'org', 'note', 'pending_approval')`, id, runHeld)
	}
	if w := call(f, "PATCH", "/api/v1/action-requests/"+sessionID, `{"decision":"approve"}`, member); w.Code != 403 || w.Body.String() != required {
		t.Fatalf("session approval without a passkey: %d %s", w.Code, w.Body.String())
	}
	if st := queryText(t, db, `select status from action_request where id = '`+sessionID+`'`); st != "pending_approval" {
		t.Fatalf("refused approval changed the row: %s", st)
	}
	if w := call(f, "PATCH", "/api/v1/action-requests/"+rejectID, `{"decision":"reject"}`, member); w.Code != 200 {
		t.Fatalf("reject without a passkey: %d %s", w.Code, w.Body.String())
	}
	body, _ := json.Marshal(map[string]any{"decision": "approve", "passkey": buildTestAssertion(t, priv, "neko.example", credID,
		testChallenge(t, f, member, `{"purpose":"approve","actionRequestId":"`+sessionID+`"}`), 2, true)})
	if w := call(f, "PATCH", "/api/v1/action-requests/"+sessionID, string(body), member); w.Code != 200 {
		t.Fatalf("session approval with a passkey: %d %s", w.Code, w.Body.String())
	}
	if got := lastAudit(t, db, sessionID); !strings.Contains(got, `"method": "passkey"`) {
		t.Fatalf("passkey approval audit: %s", got)
	}

	token := "onk_dev_aaaaaaaaaaaa_" + strings.Repeat("A", 43)
	sum := sha256.Sum256([]byte(token))
	seed(t, db, `insert into app_device (id, org_id, user_id, name, token_prefix, token_verifier) values ('60000000-0000-4000-8000-0000000000a1', 'org', 'member', 'Phone', 'onk_dev_aaaaaaaaaaaa', $1)`,
		hex.EncodeToString(sum[:]))
	if w := call(f, "PATCH", "/api/v1/action-requests/"+tokenID, `{"decision":"approve"}`, map[string]string{"Authorization": "Bearer " + token}); w.Code != 200 {
		t.Fatalf("token approval: %d %s", w.Code, w.Body.String())
	}
	if got := lastAudit(t, db, tokenID); got != `approved {"kind": "note", "method": "token", "approvedBy": "member", "soloOperator": false}` {
		t.Fatalf("token approval audit: %s", got)
	}

	const threadID, workRunID = "40000000-0000-4000-8000-0000000000a4", "50000000-0000-4000-8000-0000000000a4"
	const chatID, chatRejectID = "30000000-0000-4000-8000-0000000000a5", "30000000-0000-4000-8000-0000000000a6"
	seed(t, db, `
		insert into work_thread (id, org_id, created_by_user_id, title) values ('`+threadID+`', 'org', 'member', 'Chat');
		insert into work_run (id, org_id, thread_id, backend, status) values ('`+workRunID+`', 'org', '`+threadID+`', 'hermes', 'running');
		insert into action_request (id, org_id, work_run_id, scope, kind, status) values
		  ('`+chatID+`', 'org', '`+workRunID+`', 'org', 'note', 'pending_approval'),
		  ('`+chatRejectID+`', 'org', '`+workRunID+`', 'org', 'note', 'pending_approval');
	`)
	approveAction := "/api/v1/work/threads/" + threadID + "/runs/" + workRunID + "/approve-action"
	if w := call(f, "POST", approveAction, `{"actionRequestId":"`+chatID+`","decision":"approve"}`, member); w.Code != 403 || w.Body.String() != required {
		t.Fatalf("chat approval without a passkey: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", approveAction, `{"actionRequestId":"`+chatRejectID+`","decision":"reject"}`, member); w.Code != 200 {
		t.Fatalf("chat reject without a passkey: %d %s", w.Code, w.Body.String())
	}
	body, _ = json.Marshal(map[string]any{"actionRequestId": chatID, "decision": "approve", "passkey": buildTestAssertion(t, priv, "neko.example", credID,
		testChallenge(t, f, member, `{"purpose":"approve","actionRequestId":"`+chatID+`"}`), 3, true)})
	if w := call(f, "POST", approveAction, string(body), member); w.Code != 200 {
		t.Fatalf("chat approval with a passkey: %d %s", w.Code, w.Body.String())
	}

	const afterID = "30000000-0000-4000-8000-0000000000a7"
	seed(t, db, `insert into action_request (id, org_id, workflow_run_id, scope, kind, status)
		values ($1, 'org', $2, 'org', 'note', 'pending_approval')`, afterID, runHeld)
	secondRaw := []byte("required-passkey-002")
	secondID := registerTestPasskey(t, f, member, priv, secondRaw)
	if w := call(f, "DELETE", "/api/v1/my/passkeys/"+secondID, "", member); w.Code != 200 {
		t.Fatalf("remove a passkey that is not the last: %d %s", w.Code, w.Body.String())
	}
	lastRequired := `{"code":"passkey_required","error":"Confirm with your passkey to remove your last passkey."}`
	if w := call(f, "DELETE", "/api/v1/my/passkeys/"+firstID, "", member); w.Code != 403 || w.Body.String() != lastRequired {
		t.Fatalf("remove the last passkey without a passkey: %d %s", w.Code, w.Body.String())
	}
	wrongPurpose, _ := json.Marshal(map[string]any{"passkey": buildTestAssertion(t, priv, "neko.example", credID,
		testChallenge(t, f, member, `{"purpose":"approve","actionRequestId":"`+afterID+`"}`), 4, true)})
	if w := call(f, "DELETE", "/api/v1/my/passkeys/"+firstID, string(wrongPurpose), member); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid or expired") {
		t.Fatalf("remove with an approval challenge: %d %s", w.Code, w.Body.String())
	}
	removeCh := testChallenge(t, f, member, `{"purpose":"remove"}`)
	badRemove := buildTestAssertion(t, priv, "neko.example", credID, removeCh, 4, true)
	badRemove["signature"] = base64.RawURLEncoding.EncodeToString([]byte("not-a-valid-der-sig"))
	badBody, _ := json.Marshal(map[string]any{"passkey": badRemove})
	if w := call(f, "DELETE", "/api/v1/my/passkeys/"+firstID, string(badBody), member); w.Code != 400 {
		t.Fatalf("remove with a bad signature: %d %s", w.Code, w.Body.String())
	}
	if n := queryText(t, db, `select count(*)::text from app_passkey where id = '`+firstID+`'`); n != "1" {
		t.Fatalf("a refused removal deleted the passkey: %s", n)
	}
	goodBody, _ := json.Marshal(map[string]any{"passkey": buildTestAssertion(t, priv, "neko.example", credID,
		testChallenge(t, f, member, `{"purpose":"remove"}`), 5, true)})
	if w := call(f, "DELETE", "/api/v1/my/passkeys/"+firstID, string(goodBody), member); w.Code != 200 {
		t.Fatalf("remove the last passkey with a passkey: %d %s", w.Code, w.Body.String())
	}

	if w := call(f, "PATCH", "/api/v1/action-requests/"+afterID, `{"decision":"approve"}`, member); w.Code != 200 {
		t.Fatalf("session approval with no passkey left: %d %s", w.Code, w.Body.String())
	}
	if got := lastAudit(t, db, afterID); got != `approved {"kind": "note", "method": "session", "approvedBy": "member", "soloOperator": false}` {
		t.Fatalf("session approval audit: %s", got)
	}
}

func TestSlackApprovalNeedsNoPasskey(t *testing.T) {
	f, db := inboundFront(t)
	seed(t, db, `insert into app_passkey (org_id, user_id, credential_id, public_key, name)
		select org_id, id, 'cred-' || id, 'key', 'Phone' from app_user where org_id = 'org'`)
	seed(t, db, `insert into action_request (id, org_id, scope, kind, status, payload) values ($1, 'org', 'internal', 'note', 'pending_approval', '{}')`, arPending)
	var userID string
	if err := db.QueryRow(t.Context(), `select id from app_user where org_id = 'org' order by id limit 1`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := f.API.channelDecision(t.Context(), "org", "slack", channelActor{UserID: userID, Admin: true}, arPending, "approve", nil); err != nil {
		t.Fatal(err)
	}
	if got := lastAudit(t, db, arPending); !strings.Contains(got, `"method": "slack"`) || !strings.HasPrefix(got, "approved ") {
		t.Fatalf("slack approval audit: %s", got)
	}
}
