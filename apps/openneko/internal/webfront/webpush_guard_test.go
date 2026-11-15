package webfront

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func newPushStub(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	t.Setenv("OPENNEKO_WEB_PUSH_ALLOW_LOOPBACK", "1")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func testPushSubscription(t *testing.T, endpoint string) storedPushSubscription {
	t.Helper()
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return storedPushSubscription{
		ID:       "sub",
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}
}

func testVAPIDKeys(t *testing.T) vapidKeys {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := parseVAPIDKeyPair(base64.RawURLEncoding.EncodeToString(k.Bytes()), base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestWebPushRefusesPrivateAddressesAndRedirects(t *testing.T) {
	if _, _, _, err := validatePushSubscriptionInput(map[string]any{
		"endpoint": "http://push.example.com/x",
		"keys":     map[string]any{"p256dh": testPushSubscription(t, "").P256dh, "auth": testPushSubscription(t, "").Auth},
	}); err == nil {
		t.Fatal("an http push endpoint was accepted")
	}

	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	loopback := "https" + strings.TrimPrefix(srv.URL, "http")
	keys := testVAPIDKeys(t)
	ctx := context.Background()

	for _, endpoint := range []string{
		loopback + "/loopback",
		"https://169.254.169.254/latest/meta-data",
		"https://10.0.0.1/push",
		"https://192.168.1.1/push",
		"https://100.64.0.1/push",
		"https://[fd00::1]/push",
		"https://0.0.0.0/push",
	} {
		status, err := sendWebPush(ctx, pushClient(), keys, testPushSubscription(t, endpoint), []byte(`{}`))
		var blocked *pushBlockedError
		if !errors.As(err, &blocked) || status != 0 {
			t.Fatalf("%s: status %d, err %v; want a refused dial", endpoint, status, err)
		}
	}
	if status, _ := sendWebPush(ctx, pushClient(), keys, testPushSubscription(t, srv.URL+"/plain"), []byte(`{}`)); status != http.StatusGone {
		t.Fatalf("a stored http endpoint gave status %d, want it removed as gone", status)
	}
	mu.Lock()
	if len(hits) != 0 {
		t.Fatalf("the push stub on loopback got %v", hits)
	}
	mu.Unlock()

	t.Setenv("OPENNEKO_WEB_PUSH_ALLOW_LOOPBACK", "1")
	for _, endpoint := range []string{"https://169.254.169.254/latest/meta-data", "https://10.0.0.1/push"} {
		var blocked *pushBlockedError
		if _, err := sendWebPush(ctx, pushClient(), keys, testPushSubscription(t, endpoint), []byte(`{}`)); !errors.As(err, &blocked) {
			t.Fatalf("with the loopback seam on, %s gave %v; want a refused dial", endpoint, err)
		}
	}
	if _, _, _, err := validatePushSubscriptionInput(map[string]any{
		"endpoint": "http://push.example.com/x",
		"keys":     map[string]any{"p256dh": testPushSubscription(t, "").P256dh, "auth": testPushSubscription(t, "").Auth},
	}); err == nil {
		t.Fatal("with the loopback seam on, an http endpoint off this host was accepted")
	}
	status, err := sendWebPush(ctx, pushClient(), keys, testPushSubscription(t, srv.URL+"/redirect"), []byte(`{}`))
	if err == nil || status != http.StatusTemporaryRedirect {
		t.Fatalf("redirect: status %d, err %v; want the 307 as a failure", status, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(hits, ",") != "/redirect" {
		t.Fatalf("the client followed the redirect: %v", hits)
	}
}

func TestWebPushRetriesOnlyFailedEndpoints(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)

	var mu sync.Mutex
	hits := map[string]int{}
	stub := newPushStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/push/admin" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	ids := map[string]string{}
	for _, user := range []string{"member", "admin"} {
		sub := testPushSubscription(t, stub.URL+"/push/"+user)
		raw, _ := json.Marshal(map[string]any{"endpoint": sub.Endpoint, "keys": map[string]any{"p256dh": sub.P256dh, "auth": sub.Auth}})
		res := call(f, "POST", "/api/v1/my/push-subscriptions", string(raw), sessionCookie(user))
		if res.Code != 200 {
			t.Fatalf("subscribe %s: %d %s", user, res.Code, res.Body.String())
		}
		var out struct {
			Subscription pushSubscriptionSummary `json:"subscription"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		ids[user] = out.Subscription.ID
	}

	err := f.API.WebPushDeliver(ctx, jobs.WebPushArgs{OrgID: "org", Event: WebPushEventApprovalWaiting, Users: []string{"member", "admin"}})
	var retry *jobs.WebPushRetryError
	if !errors.As(err, &retry) || len(retry.Subscriptions) != 1 || retry.Subscriptions[0] != ids["admin"] {
		t.Fatalf("deliver error = %v (%+v), want a retry of the admin subscription only", err, retry)
	}

	mu.Lock()
	hits = map[string]int{}
	mu.Unlock()
	err = f.API.WebPushDeliver(ctx, jobs.WebPushArgs{OrgID: "org", Event: WebPushEventApprovalWaiting, Subscriptions: retry.Subscriptions})
	if !errors.As(err, &retry) {
		t.Fatalf("retry error = %v, want a retry error", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/push/member"] != 0 || hits["/push/admin"] != 1 {
		t.Fatalf("the retry sent to %v; want only the admin endpoint", hits)
	}
}

func TestWebPushEndpointKeepsItsOwner(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)

	first := testPushSubscription(t, "https://push.example.com/shared")
	raw, _ := json.Marshal(map[string]any{"endpoint": first.Endpoint, "keys": map[string]any{"p256dh": first.P256dh, "auth": first.Auth}})
	if res := call(f, "POST", "/api/v1/my/push-subscriptions", string(raw), sessionCookie("member")); res.Code != 200 {
		t.Fatalf("member subscribe: %d %s", res.Code, res.Body.String())
	}

	second := testPushSubscription(t, first.Endpoint)
	raw2, _ := json.Marshal(map[string]any{"endpoint": second.Endpoint, "keys": map[string]any{"p256dh": second.P256dh, "auth": second.Auth}})
	res := call(f, "POST", "/api/v1/my/push-subscriptions", string(raw2), sessionCookie("admin"))
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), `"code":"push_endpoint_taken"`) {
		t.Fatalf("admin posted the member's endpoint: %d %s; want 409 push_endpoint_taken", res.Code, res.Body.String())
	}
	var owner, p256dh string
	if err := db.QueryRow(ctx, `select user_id, p256dh from app_push_subscription where org_id = 'org' and endpoint = $1`, first.Endpoint).Scan(&owner, &p256dh); err != nil {
		t.Fatal(err)
	}
	if owner != "member" || p256dh != first.P256dh {
		t.Fatalf("subscription owner %q, key changed %v; want the member's row unchanged", owner, p256dh != first.P256dh)
	}

	if res := call(f, "POST", "/api/v1/my/push-subscriptions", string(raw2), sessionCookie("member")); res.Code != 200 {
		t.Fatalf("member re-subscribe with new keys: %d %s", res.Code, res.Body.String())
	}
	if err := db.QueryRow(ctx, `select p256dh from app_push_subscription where org_id = 'org' and endpoint = $1`, first.Endpoint).Scan(&p256dh); err != nil || p256dh != second.P256dh {
		t.Fatalf("the owner's new keys were not stored: %v", err)
	}
}

func TestWebPushRunWithNoOwnerGoesToAdmins(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	seedRiver(t, db, f)

	for _, user := range []string{"member", "admin"} {
		sub := testPushSubscription(t, "https://push.example.com/"+user)
		raw, _ := json.Marshal(map[string]any{"endpoint": sub.Endpoint, "keys": map[string]any{"p256dh": sub.P256dh, "auth": sub.Auth}})
		if res := call(f, "POST", "/api/v1/my/push-subscriptions", string(raw), sessionCookie(user)); res.Code != 200 {
			t.Fatalf("subscribe %s: %d %s", user, res.Code, res.Body.String())
		}
	}
	thread := "c6000000-0000-4000-8000-000000000001"
	run := "c6000000-0000-4000-8000-000000000002"
	seed(t, db, `insert into work_thread (id, org_id, title) values ($1, 'org', 'service')`, thread)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ($1, 'org', $2, 'test')`, run, thread)

	f.API.queueWorkRunFinishedPush(ctx, "org", run)
	queued := riverJobs(t, db, jobs.KindWebPush)
	if len(queued) != 1 {
		t.Fatalf("queued %d web_push jobs, want 1", len(queued))
	}
	var args jobs.WebPushArgs
	if err := json.Unmarshal([]byte(queued[0].Args), &args); err != nil {
		t.Fatal(err)
	}
	if strings.Join(args.Users, ",") != "admin" {
		t.Fatalf("a run with no owner notifies %v; want only the admin", args.Users)
	}

	owned := "c6000000-0000-4000-8000-000000000003"
	ownedRun := "c6000000-0000-4000-8000-000000000004"
	seed(t, db, `insert into work_thread (id, org_id, title, created_by_user_id) values ($1, 'org', 'chat', 'member')`, owned)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values ($1, 'org', $2, 'test', 'member', 'member')`, ownedRun, owned)
	f.API.queueWorkRunFinishedPush(ctx, "org", ownedRun)
	queued = riverJobs(t, db, jobs.KindWebPush)
	if len(queued) != 2 {
		t.Fatalf("queued %d web_push jobs after the member's run, want 2", len(queued))
	}
	if err := json.Unmarshal([]byte(queued[1].Args), &args); err != nil {
		t.Fatal(err)
	}
	if strings.Join(args.Users, ",") != "member" {
		t.Fatalf("the member's run notifies %v; want only the member", args.Users)
	}
}
