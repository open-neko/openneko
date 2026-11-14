package webfront

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func decryptTestWebPush(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, authSecret []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("ciphertext too short (%d bytes)", len(body))
	}
	salt := body[0:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	if rs < 18 {
		t.Fatalf("invalid record size %d", rs)
	}
	idLen := int(body[20])
	if len(body) < 21+idLen+17 {
		t.Fatalf("ciphertext shorter than header + tag (%d bytes)", len(body))
	}
	asPubBytes := body[21 : 21+idLen]
	ciphertext := body[21+idLen:]

	asPub, err := ecdh.P256().NewPublicKey(asPubBytes)
	if err != nil {
		t.Fatalf("ephemeral server public key: %v", err)
	}
	shared, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	uaPubBytes := uaPriv.PublicKey().Bytes()
	keyInfo := append([]byte("WebPush: info\x00"), append(uaPubBytes, asPubBytes...)...)
	prkKey := hkdfExtractSHA256(authSecret, shared)
	ikm := hkdfExpandSHA256(prkKey, keyInfo, 32)
	prk := hkdfExtractSHA256(salt, ikm)
	cek := hkdfExpandSHA256(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfExpandSHA256(prk, []byte("Content-Encoding: nonce\x00"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	padded, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("AES-128-GCM open: %v", err)
	}
	idx := bytes.LastIndexByte(padded, 0x02)
	if idx < 0 {
		t.Fatalf("missing 0x02 padding delimiter in %x", padded)
	}
	return padded[:idx]
}

func verifyTestVAPIDHeader(t *testing.T, authHeader string) (pubB64 string, claims map[string]any) {
	t.Helper()
	if !strings.HasPrefix(authHeader, "vapid ") {
		t.Fatalf("Authorization header = %q, want vapid prefix", authHeader)
	}
	var token string
	for _, part := range strings.Split(strings.TrimPrefix(authHeader, "vapid "), ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "t="); ok {
			token = v
		} else if v, ok := strings.CutPrefix(part, "k="); ok {
			pubB64 = v
		}
	}
	if token == "" || pubB64 == "" {
		t.Fatalf("Authorization header missing t or k: %q", authHeader)
	}
	pubRaw, err := decodeB64URL(pubB64)
	if err != nil || len(pubRaw) != 65 || pubRaw[0] != 0x04 {
		t.Fatalf("invalid k public key %q: %v", pubB64, err)
	}
	pubKey := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(pubRaw[1:33]),
		Y:     new(big.Int).SetBytes(pubRaw[33:65]),
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid JWT %q", token)
	}
	sigRaw, err := decodeB64URL(parts[2])
	if err != nil || len(sigRaw) != 64 {
		t.Fatalf("invalid JWT signature: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	rInt := new(big.Int).SetBytes(sigRaw[:32])
	sInt := new(big.Int).SetBytes(sigRaw[32:64])
	if !ecdsa.Verify(pubKey, digest[:], rInt, sInt) {
		t.Fatal("VAPID ES256 signature verification failed")
	}
	claimsRaw, err := decodeB64URL(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	return pubB64, claims
}

func TestWebPushSubscriptionsVAPIDAndThreeEvents(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	seedRiver(t, db, f)
	member := sessionCookie("member")
	admin := sessionCookie("admin")

	if err := f.API.EnsureVAPIDKeys(); err != nil {
		t.Fatalf("EnsureVAPIDKeys: %v", err)
	}
	store1, err := secrets.Read(f.API.SecretsDir)
	if err != nil {
		t.Fatal(err)
	}
	privKey1 := store1[WebPushSecretsSection][vapidPrivateKeyName]
	pubKey1 := store1[WebPushSecretsSection][vapidPublicKeyName]
	if privKey1 == "" || pubKey1 == "" {
		t.Fatalf("missing VAPID keys in secrets store: %+v", store1[WebPushSecretsSection])
	}
	if err := f.API.EnsureVAPIDKeys(); err != nil {
		t.Fatal(err)
	}
	store2, err := secrets.Read(f.API.SecretsDir)
	if err != nil {
		t.Fatal(err)
	}
	if store2[WebPushSecretsSection][vapidPrivateKeyName] != privKey1 ||
		store2[WebPushSecretsSection][vapidPublicKeyName] != pubKey1 {
		t.Fatal("EnsureVAPIDKeys rotated existing keys instead of keeping them")
	}

	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatal(err)
	}
	p256dhB64 := base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes())
	authB64 := base64.RawURLEncoding.EncodeToString(authSecret)

	type receivedPush struct {
		Path     string
		Encoding string
		VAPIDKey string
		Claims   map[string]any
		Notice   webPushNotice
		RawJSON  string
	}
	var mu sync.Mutex
	var received []receivedPush
	gonePaths := map[string]bool{}

	pushStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		isGone := gonePaths[r.URL.Path]
		mu.Unlock()
		if isGone {
			w.WriteHeader(http.StatusGone)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read push body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		vapidPub, claims := verifyTestVAPIDHeader(t, r.Header.Get("Authorization"))
		decrypted := decryptTestWebPush(t, raw, uaPriv, authSecret)
		var notice webPushNotice
		if err := json.Unmarshal(decrypted, &notice); err != nil {
			t.Errorf("unmarshal decrypted notice: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, receivedPush{
			Path:     r.URL.Path,
			Encoding: r.Header.Get("Content-Encoding"),
			VAPIDKey: vapidPub,
			Claims:   claims,
			Notice:   notice,
			RawJSON:  string(decrypted),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer pushStub.Close()

	// Empty list at start; neither VAPID key is ever in a response.
	listResp := call(f, "GET", "/api/v1/my/push-subscriptions", "", member)
	if listResp.Code != 200 || listResp.Body.String() != `{"subscriptions":[]}` {
		t.Fatalf("empty list: %d %s", listResp.Code, listResp.Body.String())
	}

	// Subscribe member's device.
	subPayload := map[string]any{
		"endpoint": pushStub.URL + "/push/member-phone",
		"keys": map[string]any{
			"p256dh": p256dhB64,
			"auth":   authB64,
		},
	}
	rawSub, _ := json.Marshal(subPayload)
	createResp := call(f, "POST", "/api/v1/my/push-subscriptions", string(rawSub), member)
	if createResp.Code != 200 {
		t.Fatalf("create subscription: %d %s", createResp.Code, createResp.Body.String())
	}
	if strings.Contains(createResp.Body.String(), privKey1) || strings.Contains(createResp.Body.String(), pubKey1) {
		t.Fatal("response leaked a VAPID key from the secrets store")
	}
	var created struct {
		Subscription pushSubscriptionSummary `json:"subscription"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Subscription.Endpoint != pushStub.URL+"/push/member-phone" {
		t.Fatalf("created subscription = %+v", created.Subscription)
	}

	// Admin cannot see or delete member's subscription.
	adminList := call(f, "GET", "/api/v1/my/push-subscriptions", "", admin)
	if adminList.Code != 200 || adminList.Body.String() != `{"subscriptions":[]}` {
		t.Fatalf("admin list: %d %s", adminList.Code, adminList.Body.String())
	}
	if del := call(f, "DELETE", "/api/v1/my/push-subscriptions/"+created.Subscription.ID, "", admin); del.Code != 404 {
		t.Fatalf("admin delete of member subscription: %d %s", del.Code, del.Body.String())
	}

	// Subscribe admin's device too.
	adminSubPayload := map[string]any{
		"endpoint": pushStub.URL + "/push/admin-phone",
		"keys": map[string]any{
			"p256dh": p256dhB64,
			"auth":   authB64,
		},
	}
	rawAdminSub, _ := json.Marshal(adminSubPayload)
	adminCreate := call(f, "POST", "/api/v1/my/push-subscriptions", string(rawAdminSub), admin)
	if adminCreate.Code != 200 {
		t.Fatalf("admin create: %d %s", adminCreate.Code, adminCreate.Body.String())
	}

	// 1. Event: an approval waits ("approval_waiting").
	f.API.queueApprovalWaitingPush(ctx, "org", arOrgRun)
	// 2. Event: a run finished ("run_finished").
	sensitiveSummary := "Sensitive revenue numbers: $999,000"
	if err := f.API.finishWorkflowRun(ctx, runOrg, "completed", &sensitiveSummary, nil); err != nil {
		t.Fatalf("finishWorkflowRun completed: %v", err)
	}
	// 3. Event: a workflow needs attention ("workflow_needs_attention" via needs_input and failed).
	sensitiveQuestion := "Confidential question about SKU-42"
	if err := f.API.finishWorkflowRun(ctx, runOrg, "needs_input", &sensitiveQuestion, nil); err != nil {
		t.Fatalf("finishWorkflowRun needs_input: %v", err)
	}
	sensitiveErr := "Database credentials expired for SKU-42"
	if err := f.API.finishWorkflowRun(ctx, runOrg, "failed", nil, &sensitiveErr); err != nil {
		t.Fatalf("finishWorkflowRun failed: %v", err)
	}

	// Verify the four River jobs were queued in the river schema and run them via WebPushDeliver.
	rawJobs := riverJobs(t, db, jobs.KindWebPush)
	var queued []jobs.WebPushArgs
	for _, rj := range rawJobs {
		var a jobs.WebPushArgs
		if err := json.Unmarshal([]byte(rj.Args), &a); err != nil {
			t.Fatal(err)
		}
		queued = append(queued, a)
	}
	if len(queued) != 4 {
		t.Fatalf("queued %d web_push jobs, want 4: %+v", len(queued), queued)
	}
	for _, qj := range queued {
		if err := f.API.WebPushDeliver(ctx, qj); err != nil {
			t.Fatalf("WebPushDeliver(%+v): %v", qj, err)
		}
	}

	mu.Lock()
	gotPushes := append([]receivedPush(nil), received...)
	mu.Unlock()
	if len(gotPushes) == 0 {
		t.Fatal("expected push deliveries at stub")
	}

	seenEvents := map[string]webPushNotice{}
	for _, p := range gotPushes {
		if p.Encoding != "aes128gcm" {
			t.Fatalf("Content-Encoding = %q, want aes128gcm", p.Encoding)
		}
		if p.VAPIDKey != pubKey1 {
			t.Fatalf("VAPID k = %q, want %q", p.VAPIDKey, pubKey1)
		}
		if strings.Contains(p.RawJSON, "999,000") || strings.Contains(p.RawJSON, "SKU-42") || strings.Contains(p.RawJSON, runOrg) || strings.Contains(p.RawJSON, arOrgRun) {
			t.Fatalf("push payload leaked business/run data: %s", p.RawJSON)
		}
		seenEvents[p.Notice.Event] = p.Notice
	}
	if got := seenEvents[WebPushEventApprovalWaiting]; got.Body != "An approval is waiting." || got.URL != "/actions" || got.Title != "OpenNeko" {
		t.Fatalf("approval_waiting notice = %+v", got)
	}
	if got := seenEvents[WebPushEventRunFinished]; got.Body != "A run finished." || got.URL != "/runs" || got.Title != "OpenNeko" {
		t.Fatalf("run_finished notice = %+v", got)
	}
	if got := seenEvents[WebPushEventWorkflowNeedsAttention]; got.Body != "A workflow needs attention." || got.URL != "/workflows" || got.Title != "OpenNeko" {
		t.Fatalf("workflow_needs_attention notice = %+v", got)
	}

	// Mark member's endpoint as 410 Gone at the push service and verify WebPushDeliver removes it.
	mu.Lock()
	gonePaths["/push/member-phone"] = true
	mu.Unlock()
	if err := f.API.WebPushDeliver(ctx, jobs.WebPushArgs{OrgID: "org", Event: WebPushEventRunFinished, Users: []string{"member"}}); err != nil {
		t.Fatalf("WebPushDeliver on 410 Gone: %v", err)
	}
	afterGone := call(f, "GET", "/api/v1/my/push-subscriptions", "", member)
	if afterGone.Code != 200 || afterGone.Body.String() != `{"subscriptions":[]}` {
		t.Fatalf("member subscriptions after 410 Gone: %d %s", afterGone.Code, afterGone.Body.String())
	}

	// Admin turns off their own subscription via DELETE.
	var adminCreated struct {
		Subscription pushSubscriptionSummary `json:"subscription"`
	}
	_ = json.Unmarshal(adminCreate.Body.Bytes(), &adminCreated)
	delAdmin := call(f, "DELETE", "/api/v1/my/push-subscriptions/"+adminCreated.Subscription.ID, "", admin)
	if delAdmin.Code != 200 || delAdmin.Body.String() != `{"ok":true}` {
		t.Fatalf("admin delete: %d %s", delAdmin.Code, delAdmin.Body.String())
	}
	afterAdminDel := call(f, "GET", "/api/v1/my/push-subscriptions", "", admin)
	if afterAdminDel.Code != 200 || afterAdminDel.Body.String() != `{"subscriptions":[]}` {
		t.Fatalf("admin subscriptions after delete: %d %s", afterAdminDel.Code, afterAdminDel.Body.String())
	}
}
