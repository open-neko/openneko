package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func pushSubscribe(t *testing.T, f *Front, user, endpoint string) string {
	t.Helper()
	sub := testPushSubscription(t, endpoint)
	raw, _ := json.Marshal(map[string]any{"endpoint": sub.Endpoint, "keys": map[string]any{"p256dh": sub.P256dh, "auth": sub.Auth}})
	res := call(f, "POST", "/api/v1/my/push-subscriptions", string(raw), sessionCookie(user))
	if res.Code != 200 {
		t.Fatalf("subscribe %s %s: %d %s", user, endpoint, res.Code, res.Body.String())
	}
	var out struct {
		Subscription pushSubscriptionSummary `json:"subscription"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &out)
	return out.Subscription.ID
}

func pushFront(t *testing.T) (*Front, func(sql string, args ...any) string) {
	t.Helper()
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	return f, func(sql string, args ...any) string { return rowText(t, db, sql, args...) }
}

// startDNSStub answers every A question with ip and every other question
// with no record.
func startDNSStub(t *testing.T, ip [4]byte) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			hdr, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: hdr.ID, Response: true, Authoritative: true})
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			if q.Type == dnsmessage.TypeA {
				_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: ip})
			}
			msg, err := b.Finish()
			if err == nil {
				_, _ = conn.WriteTo(msg, from)
			}
		}
	}()
	return conn.LocalAddr().String()
}

func TestWebPushRefusesNameThatResolvesToPrivateAddress(t *testing.T) {
	addr := startDNSStub(t, [4]byte{10, 0, 0, 7})
	guardedResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", addr)
	}}
	t.Cleanup(func() { guardedResolver = nil })

	status, err := sendWebPush(context.Background(), pushClient(), testVAPIDKeys(t),
		testPushSubscription(t, "https://push.attacker.example/x"), []byte(`{}`))
	var blocked *pushBlockedError
	if !errors.As(err, &blocked) || status != 0 || blocked.ip != "10.0.0.7" {
		t.Fatalf("a name that resolves to 10.0.0.7 gave status %d, err %v; want a refused dial", status, err)
	}
}

func TestWebPushSendsInParallelUnderOneDeadline(t *testing.T) {
	f, _ := pushFront(t)
	release := make(chan struct{})
	var mu sync.Mutex
	inFlight, most := 0, 0
	stub := newPushStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		most = max(most, inFlight)
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
		if strings.HasSuffix(r.URL.Path, "/hang") {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(func() { close(release) })
	var fast string
	for i := range 6 {
		fast = pushSubscribe(t, f, "member", stub.URL+"/push/"+string(rune('a'+i)))
	}
	start := time.Now()
	if err := f.API.WebPushDeliver(context.Background(), jobs.WebPushArgs{OrgID: "org", Event: WebPushEventRunFinished, Users: []string{"member"}}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("six sends of 300 ms took %v; want them in parallel", took)
	}
	if most > pushSendsAtOnce {
		t.Fatalf("%d sends ran at once; the bound is %d", most, pushSendsAtOnce)
	}

	webPushBatchLimit = 400 * time.Millisecond
	t.Cleanup(func() { webPushBatchLimit = 45 * time.Second })
	hang := pushSubscribe(t, f, "admin", stub.URL+"/push/hang")
	start = time.Now()
	err := f.API.WebPushDeliver(context.Background(), jobs.WebPushArgs{OrgID: "org", Event: WebPushEventRunFinished, Subscriptions: []string{fast, hang}})
	var retry *jobs.WebPushRetryError
	if !errors.As(err, &retry) || len(retry.Subscriptions) != 1 || retry.Subscriptions[0] != hang {
		t.Fatalf("deliver error = %v (%+v); want a retry of the hung subscription only", err, retry)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a hung endpoint held the batch for %v; the batch limit is %v", took, webPushBatchLimit)
	}
}

func TestWebPushKeepsTenSubscriptionsPerUser(t *testing.T) {
	f, q := pushFront(t)
	var ids []string
	for i := range pushSubscriptionsPerUser + 2 {
		ids = append(ids, pushSubscribe(t, f, "member", "https://push.example.com/device-"+string(rune('a'+i))))
		time.Sleep(5 * time.Millisecond)
	}
	pushSubscribe(t, f, "admin", "https://push.example.com/admin")
	if g := q(`select count(*)::text from app_push_subscription where user_id = 'member'`); g != "10" {
		t.Fatalf("the member has %s subscriptions; want 10", g)
	}
	if g := q(`select count(*)::text from app_push_subscription where id::text = any($1)`, ids[:2]); g != "0" {
		t.Fatalf("the two oldest subscriptions remain (%s)", g)
	}
	if g := q(`select count(*)::text from app_push_subscription where user_id = 'admin'`); g != "1" {
		t.Fatalf("the admin has %s subscriptions; want 1", g)
	}
}

func TestWebPushRemovesOnlyWhatThePushServiceCallsGone(t *testing.T) {
	f, q := pushFront(t)
	stub := newPushStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	gone := pushSubscribe(t, f, "member", stub.URL+"/push/gone")
	broken := pushSubscribe(t, f, "member", stub.URL+"/push/broken-key")
	q(`update app_push_subscription set p256dh = 'not-a-key' where id = $1 returning 'x'`, broken)

	if err := f.API.WebPushDeliver(context.Background(), jobs.WebPushArgs{OrgID: "org", Event: WebPushEventRunFinished, Users: []string{"member"}}); err != nil {
		t.Fatalf("a local key fault gave %v; want no retry", err)
	}
	if g := q(`select count(*)::text from app_push_subscription where id = $1`, gone); g != "0" {
		t.Fatal("the push service said 410 and the subscription stays")
	}
	if g := q(`select count(*)::text from app_push_subscription where id = $1`, broken); g != "1" {
		t.Fatal("a local encrypt error removed the subscription")
	}
}

func TestWebPushCorruptKeyPairFails(t *testing.T) {
	f, _ := pushFront(t)
	store, err := secrets.Read(f.API.SecretsDir)
	if err != nil {
		t.Fatal(err)
	}
	store, _ = secrets.Set(store, WebPushSecretsSection, vapidPrivateKeyName, "corrupt")
	store, _ = secrets.Set(store, WebPushSecretsSection, vapidPublicKeyName, "corrupt")
	if err := secrets.Write(store, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	if err := f.API.EnsureVAPIDKeys(); err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("a corrupt key pair gave %v; want an error", err)
	}
	after, err := secrets.Read(f.API.SecretsDir)
	if err != nil {
		t.Fatal(err)
	}
	if after[WebPushSecretsSection][vapidPrivateKeyName] != "corrupt" {
		t.Fatal("the corrupt key pair was replaced; every browser subscription would break in silence")
	}
}

func TestLogoutRemovesThisBrowsersPushSubscription(t *testing.T) {
	f, q := pushFront(t)
	mine := "https://push.example.com/member-browser"
	theirs := "https://push.example.com/admin-browser"
	pushSubscribe(t, f, "member", mine)
	pushSubscribe(t, f, "admin", theirs)

	if res := call(f, "POST", "/api/v1/auth/logout", `{"pushEndpoint":"`+theirs+`"}`, sessionCookie("member")); res.Code != 200 {
		t.Fatalf("logout: %d %s", res.Code, res.Body.String())
	}
	if g := q(`select count(*)::text from app_push_subscription where endpoint = $1`, theirs); g != "1" {
		t.Fatal("a logout removed the push subscription of another user")
	}
	if res := call(f, "POST", "/api/v1/auth/logout", `{"pushEndpoint":"`+mine+`"}`, sessionCookie("member")); res.Code != 200 {
		t.Fatalf("logout: %d %s", res.Code, res.Body.String())
	}
	if g := q(`select count(*)::text from app_push_subscription where endpoint = $1`, mine); g != "0" {
		t.Fatal("the logout left this browser's push subscription")
	}
}
