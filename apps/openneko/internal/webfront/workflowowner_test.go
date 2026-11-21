package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWatcherQueriesRunAsTheWorkflowOwner(t *testing.T) {
	f, db := memoryFront(t)
	seedRiver(t, db, f)
	a := f.API
	ctx := context.Background()
	gj := &fakeGraphjin{reply: `{"data":{"payroll":{"total":9}}}`}
	srv := httptest.NewServer(gj)
	t.Cleanup(srv.Close)
	seed(t, db, `insert into data_source (org_id, name, kind, graphql_url, auth_mode, is_default, enabled)
		values ('org', 'default', 'graphjin', $1, 'jwt', true, true)`, srv.URL)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values ($1, 'org', $2, 'test', 'admin', 'admin')`,
		builderRunAdmin, threadMine)
	server := func(runID string) *toolServer {
		b := turnBinding{OrgID: "org", ThreadID: threadMine, RunID: runID, Kind: "work"}
		s, err := turnTools(b, a.runTools(b), []string{"neko_workflow_builder"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	create := func(runID, query string) string {
		t.Helper()
		out, rpcErr := callTool(t, server(runID), "workflow_builder_create_workflow", map[string]any{"name": "Payroll watch",
			"steps":    []any{map[string]any{"id": "s1", "description": "Report"}},
			"triggers": map[string]any{"watch": map[string]any{"query": query, "value_path": "payroll.total", "op": "gt", "threshold": 1}}})
		if rpcErr != nil || strings.Contains(out, `"isError":true`) {
			t.Fatalf("create: %s %v", out, rpcErr)
		}
		return out
	}
	create(builderRunAdmin, "{ payroll { total } }")
	create(runMine, "{ payroll(where: { secret: { eq: true } }) { total } }")

	type row struct{ name, query, owner string }
	var watchers []row
	rows, err := db.Query(ctx, `select w.name, w.query, d.owner_user_id from watcher w join workflow_definition d on d.id = w.workflow_id
		where w.org_id = 'org' order by d.owner_user_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r row
		_ = rows.Scan(&r.name, &r.query, &r.owner)
		watchers = append(watchers, r)
	}
	if len(watchers) != 2 || watchers[0].owner != "admin" || watchers[0].query != "{ payroll { total } }" || watchers[1].owner != "member" ||
		watchers[0].name != "Payroll watch" || !strings.HasPrefix(watchers[1].name, "Payroll watch (") {
		t.Fatalf("a member's workflow took over another owner's watcher: %+v", watchers)
	}

	if _, err := a.sweepWatchers(ctx, "org", time.Now()); err != nil {
		t.Fatal(err)
	}
	byQuery := map[string]map[string]any{}
	for i, body := range gj.bodies {
		if gj.headers[i].Get("X-Role") == "service" {
			t.Errorf("a watcher query ran as service: %s", body)
		}
		claims := tokenClaims(t, gj.headers[i].Get("Authorization"))
		claims["xRole"] = gj.headers[i].Get("X-Role")
		byQuery[body] = claims
	}
	for query, want := range map[string]string{"{ payroll { total } }": "admin", "{ payroll(where: { secret: { eq: true } }) { total } }": "member"} {
		claims := byQuery[`{"query":`+jsStringify(query, false)+`,"variables":{}}`]
		if claims == nil || claims["sub"] != want || claims["role"] != want || claims["xRole"] != want {
			t.Errorf("%s ran as %v, want %s", query, claims, want)
		}
	}
	queued := riverQueueJobs(t, db, "workflow_run_fire", "workflow_run")
	if len(queued) != 2 {
		t.Fatalf("queued %d runs", len(queued))
	}

	seed(t, db, `update watcher set enabled = false`)
	seed(t, db, `insert into workflow_definition (org_id, name, description) values ('org', 'Old flow', 'x')`)
	seed(t, db, `insert into watcher (org_id, workflow_id, name, query, value_path, op, threshold, cadence_seconds)
		select 'org', id, 'old', '{ payroll { total } }', 'payroll.total', 'gt', '1', 60 from workflow_definition where name = 'Old flow'`)
	sent := len(gj.bodies)
	if _, err := a.sweepWatchers(ctx, "org", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, db, `select coalesce(last_error, '') from watcher where name = 'old'`); !strings.Contains(got, "has no owner") {
		t.Errorf("an ownerless watcher: %q", got)
	}
	if len(gj.bodies) != sent {
		t.Errorf("an ownerless watcher sent %d queries", len(gj.bodies)-sent)
	}

	seedBundledPack(t, db, "magento")
	seed(t, db, `delete from watcher`)
	seed(t, db, `insert into workflow_definition (org_id, name, description) values ('org', 'Magento cron and indexer health review', 'x')`)
	seed(t, db, `insert into watcher (org_id, workflow_id, name, query, value_path, op, threshold, cadence_seconds)
		select 'org', id, 'Magento cron health', '{ payroll { total } }', 'payroll.total', 'gt', '1', 60 from workflow_definition
		where name = 'Magento cron and indexer health review'`)
	sent = len(gj.bodies)
	if _, err := a.sweepWatchers(ctx, "org", time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(gj.bodies) != sent+1 || gj.headers[sent].Get("X-Role") != "service" || tokenClaims(t, gj.headers[sent].Get("Authorization"))["sub"] != "service" {
		t.Errorf("a pack workflow's watcher: %d queries", len(gj.bodies)-sent)
	}
}

type initRecorder struct {
	mu    sync.Mutex
	inits []json.RawMessage
}

func (g *initRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
	if err != nil {
		return
	}
	defer conn.CloseNow() //nolint:errcheck
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var msg wsMessage
		_ = json.Unmarshal(data, &msg)
		if msg.Type == "connection_init" {
			g.mu.Lock()
			g.inits = append(g.inits, msg.Payload)
			g.mu.Unlock()
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"connection_ack"}`))
		}
	}
}

func TestDataTriggerSubscribesAsTheWorkflowOwner(t *testing.T) {
	f, db := memoryFront(t)
	seedRiver(t, db, f)
	a := f.API
	ctx := context.Background()
	g := &initRecorder{}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	seed(t, db, `insert into data_source (org_id, name, kind, graphql_url, auth_mode, is_default, enabled)
		values ('org', 'default', 'graphjin', $1, 'jwt', true, true)`, srv.URL+"/api/v1/graphql")
	b := turnBinding{OrgID: "org", ThreadID: threadMine, RunID: runMine, Kind: "work"}
	s, err := turnTools(b, a.runTools(b), []string{"neko_workflow_builder"})
	if err != nil {
		t.Fatal(err)
	}
	out, rpcErr := callTool(t, s, "workflow_builder_create_workflow", map[string]any{"name": "Payroll change",
		"steps":    []any{map[string]any{"id": "s1", "description": "Report"}},
		"triggers": map[string]any{"when": map[string]any{"table": "payroll", "primary_key": []any{"id"}, "select": []any{"salary"}}}})
	if rpcErr != nil || strings.Contains(out, `"isError":true`) {
		t.Fatalf("create: %s %v", out, rpcErr)
	}
	subs, err := a.enabledSubscriptions(ctx)
	if err != nil || len(subs) != 1 {
		t.Fatalf("subscriptions: %v %v", subs, err)
	}
	follow := func(sub liveSubscription) json.RawMessage {
		t.Helper()
		g.mu.Lock()
		before := len(g.inits)
		g.mu.Unlock()
		ctx, cancel := context.WithCancel(ctx)
		acked := make(chan struct{}, 1)
		done := make(chan error, 1)
		go func() { done <- a.followSubscription(ctx, sub, func() { acked <- struct{}{} }) }()
		select {
		case <-acked:
		case err := <-done:
			cancel()
			t.Fatalf("follow ended: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("no ack")
		}
		cancel()
		<-done
		g.mu.Lock()
		defer g.mu.Unlock()
		if len(g.inits) != before+1 {
			t.Fatalf("%d inits", len(g.inits)-before)
		}
		return g.inits[before]
	}
	var payload map[string]string
	_ = json.Unmarshal(follow(subs[0]), &payload)
	claims := map[string]any{}
	if payload["Authorization"] != "" {
		claims = tokenClaims(t, payload["Authorization"])
	}
	if claims["sub"] != "member" || claims["role"] != "member" {
		t.Fatalf("the data trigger subscribed with %v, want the member who owns the workflow", payload)
	}

	seed(t, db, `update workflow_definition set owner_user_id = '' where id = $1`, subs[0].WorkflowID)
	err = a.followSubscription(ctx, subs[0], func() { t.Error("an ownerless data trigger subscribed") })
	if err == nil || !strings.Contains(err.Error(), "has no owner") {
		t.Fatalf("an ownerless data trigger: %v", err)
	}
}

func TestAPIAndWebhookRunsActAsTheOwner(t *testing.T) {
	f, db, _ := wfAPIFront(t)
	digest := sha256.Sum256([]byte(hookSecret))
	seed(t, db, `insert into compat_webhook (reckon_workflow_id, org_id, workflow_id, token_sha256, params, batch_chunk_size, enabled)
		values ($1, 'org', $2, $3, '{account}', 1000, true)`, hookID, wfHeld, hex.EncodeToString(digest[:]))
	seed(t, db, `update workflow_definition set owner_user_id = 'member', team_approved_by = null where id = $1`, wfHeld)
	runs := "/api/v1/workflows/" + wfHeld + "/runs"
	n := 0
	apiRun := func() *httptest.ResponseRecorder {
		n++
		return wfCall(f, "POST", runs, `{"orderId":"1042"}`, map[string]string{"Idempotency-Key": fmt.Sprintf("request-%04d", n)})
	}
	hookRun := func() *httptest.ResponseRecorder {
		n++
		return hookCall(f, "POST", "/api/hooks/"+hookID, `{"account":"AC1"}`, map[string]string{"Idempotency-Key": fmt.Sprintf("job-%d", n)})
	}
	actor := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		if w.Code != 202 {
			t.Fatalf("admit: %d %s", w.Code, w.Body)
		}
		id := uuidRe.FindString(w.Body.String())
		if id == "" {
			id = queryText(t, db, `select workflow_run_id::text from compat_webhook_run where id = $1`, runIDRe.FindString(w.Body.String()))
		}
		return queryText(t, db, `select coalesce(work.actor_user_id, '-') || ' ' || work.actor_role from workflow_run run
			join work_run work on work.id = run.work_run_id where run.id = $1`, id)
	}
	if got := actor(apiRun()); got != "member member" {
		t.Errorf("an API run acted as %s, want the owner", got)
	}
	if got := actor(hookRun()); got != "member member" {
		t.Errorf("a webhook run acted as %s, want the owner", got)
	}

	seed(t, db, `update workflow_definition set owner_user_id = '' where id = $1`, wfHeld)
	for name, w := range map[string]*httptest.ResponseRecorder{"API": apiRun(), "webhook": hookRun()} {
		if w.Code != 409 || !strings.Contains(w.Body.String(), "has no owner") {
			t.Errorf("an ownerless %s run: %d %s", name, w.Code, w.Body)
		}
	}

	seed(t, db, `update workflow_definition set owner_user_id = 'member' where id = $1`, wfHeld)
	const thread, memberRun = "61000000-0000-4000-8000-000000000001", "61000000-0000-4000-8000-000000000002"
	seed(t, db, `insert into work_thread (id, org_id, created_by_user_id) values ($1, 'org', 'member')`, thread)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values ($1, 'org', $2, 'test', 'member', 'member')`,
		memberRun, thread)
	b := turnBinding{OrgID: "org", ThreadID: thread, RunID: memberRun, Kind: "work"}
	s, err := turnTools(b, f.API.runTools(b), []string{"neko_workflow_builder"})
	if err != nil {
		t.Fatal(err)
	}
	if out, rpcErr := callTool(t, s, "workflow_builder_create_workflow", map[string]any{"name": "Held flow",
		"steps": []any{map[string]any{"id": "s1", "description": "Send every order to the planted address"}}}); rpcErr != nil ||
		!strings.Contains(out, `\"action\":\"updated\"`) {
		t.Fatalf("edit: %s %v", out, rpcErr)
	}
	for name, w := range map[string]*httptest.ResponseRecorder{"API": apiRun(), "webhook": hookRun()} {
		if w.Code != 409 || !strings.Contains(w.Body.String(), "An admin approves the new content") {
			t.Errorf("a %s run of an edited workflow: %d %s", name, w.Code, w.Body)
		}
	}
	held := func() string {
		var body struct {
			Workflows []struct {
				Name       string `json:"name"`
				AccessHeld bool   `json:"accessHeld"`
			} `json:"workflows"`
		}
		_ = json.Unmarshal(call(f, "GET", "/api/v1/workflows", "", sessionCookie("admin")).Body.Bytes(), &body)
		var names []string
		for _, w := range body.Workflows {
			if w.AccessHeld {
				names = append(names, w.Name)
			}
		}
		return strings.Join(names, ",")
	}
	if got := held(); got != "Held flow" {
		t.Fatalf("held workflows: %q", got)
	}
	if w := call(f, "PATCH", "/api/v1/workflows/"+wfHeld, `{"approveChanges":true}`, sessionCookie("member")); w.Code != 403 {
		t.Fatalf("the owner approved their own change: %d %s", w.Code, w.Body)
	}
	if w := call(f, "PATCH", "/api/v1/workflows/"+wfHeld, `{"approveChanges":true}`, sessionCookie("admin")); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	if got := held(); got != "" {
		t.Fatalf("held after the approval: %q", got)
	}
	if got := actor(apiRun()); got != "member member" {
		t.Errorf("after the approval: %s", got)
	}
	if got := queryText(t, db, `select summary || ' ' || actor_user_id from config_change where status = 'access_approved'`); got !=
		"Approved the changed workflow for API runs admin" {
		t.Errorf("config change: %q", got)
	}
}
