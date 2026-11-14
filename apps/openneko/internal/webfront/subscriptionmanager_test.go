package webfront

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type subscriptionFixture struct {
	Queries []struct {
		SourceKind string          `json:"sourceKind"`
		Filter     json.RawMessage `json:"filter"`
		Payload    *struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		} `json:"payload"`
	} `json:"queries"`
	OutputCases []struct {
		Payload json.RawMessage `json:"payload"`
		Parsed  map[string]any  `json:"parsed"`
		Runs    []fixtureRun    `json:"runs"`
	} `json:"outputCases"`
	SourceCases []struct {
		Filter  json.RawMessage `json:"filter"`
		Payload json.RawMessage `json:"payload"`
		Parsed  json.RawMessage `json:"parsed"`
		Runs    []fixtureRun    `json:"runs"`
	} `json:"sourceCases"`
}

type fixtureRun struct {
	Template    *string         `json:"template"`
	Observation map[string]any  `json:"observation"`
	Audit       json.RawMessage `json:"audit"`
	Enqueue     struct {
		Payload struct {
			TriggerPayload json.RawMessage `json:"triggerPayload"`
		} `json:"payload"`
		Opts struct {
			DedupeKey string `json:"dedupeKey"`
		} `json:"opts"`
	} `json:"enqueue"`
}

const (
	fixSub      = "70000000-0000-4000-8000-000000000001"
	fixWorkflow = "71000000-0000-4000-8000-000000000001"
	fixSource   = "75000000-0000-4000-8000-000000000001"
)

func fixtureJS(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	v, err := jsParse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return jsStringify(v, false)
}

func strOrNil(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestSubscriptionMatchMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/subscription-match.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx subscriptionFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for i, q := range fx.Queries {
		query, vars, ok := subscriptionOperation(liveSubscription{SourceKind: q.SourceKind, Filter: q.Filter, OrgID: "org"})
		if q.Payload == nil {
			if ok {
				t.Fatalf("query %d: Go built %q, Node built none", i, query)
			}
			continue
		}
		if !ok || query != q.Payload.Query || !reflect.DeepEqual(vars, q.Payload.Variables) {
			t.Fatalf("query %d:\n%s %v\nwant\n%s %v", i, query, vars, q.Payload.Query, q.Payload.Variables)
		}
	}

	runs := 0
	for i, c := range fx.OutputCases {
		msg, err := jsParse(c.Payload)
		if err != nil {
			t.Fatal(err)
		}
		m, ok := parseOutputMatch(msg)
		if ok != (c.Parsed != nil) {
			t.Fatalf("output %d: parsed %v, Node %v", i, ok, c.Parsed)
		}
		if !ok {
			continue
		}
		got := map[string]any{"id": m.ID, "org_id": m.OrgID, "workflow_run_id": m.WorkflowRunID, "kind": m.Kind,
			"scope": strOrNil(m.Scope), "topic": strOrNil(m.Topic), "mood": strOrNil(m.Mood), "title": m.Title, "created_at": m.CreatedAt}
		if !reflect.DeepEqual(got, c.Parsed) {
			t.Fatalf("output %d: %v, want %v", i, got, c.Parsed)
		}
		for _, r := range c.Runs {
			runs++
			p := outputMatchPlan(liveSubscription{ID: fixSub, OrgID: "org", WorkflowID: fixWorkflow, IdempotencyKeyTemplate: r.Template}, m)
			obs := map[string]any{"orgId": "org", "sourceOutputId": strOrNil(p.Observation.SourceOutputID), "consumerKind": "workflow",
				"consumerWorkflowId": fixWorkflow, "subscriptionId": fixSub, "title": strOrNil(p.Observation.Title), "mood": strOrNil(p.Observation.Mood)}
			if !reflect.DeepEqual(obs, r.Observation) {
				t.Fatalf("output %d observation: %v, want %v", i, obs, r.Observation)
			}
			var tp struct {
				ObservationID string `json:"observation_id"`
			}
			_ = json.Unmarshal(r.Enqueue.Payload.TriggerPayload, &tp)
			if got, want := jsStringify(p.TriggerPayload(tp.ObservationID), false), fixtureJS(t, r.Enqueue.Payload.TriggerPayload); got != want {
				t.Fatalf("output %d trigger payload: %s, want %s", i, got, want)
			}
			if p.DedupeKey != r.Enqueue.Opts.DedupeKey {
				t.Fatalf("output %d dedupe key: %q, want %q", i, p.DedupeKey, r.Enqueue.Opts.DedupeKey)
			}
		}
	}

	for i, c := range fx.SourceCases {
		var f map[string]any
		_ = json.Unmarshal(c.Filter, &f)
		sc, ok := parseSourceChange(f)
		if !ok {
			t.Fatalf("source %d: filter refused", i)
		}
		msg, err := jsParse(c.Payload)
		if err != nil {
			t.Fatal(err)
		}
		m, ok := parseSourceMatch(msg, sc)
		if ok != (string(c.Parsed) != "null") {
			t.Fatalf("source %d: parsed %v, Node %s", i, ok, c.Parsed)
		}
		if !ok {
			continue
		}
		parsed := jsObjOf("table", m.Table, "primary_key", m.PrimaryKey, "snapshot", m.Snapshot, "version_token", any(nil))
		if m.VersionToken != nil {
			parsed.set("version_token", *m.VersionToken)
		}
		if got, want := jsStringify(parsed, false), fixtureJS(t, c.Parsed); got != want {
			t.Fatalf("source %d parsed:\n%s\nwant\n%s", i, got, want)
		}
		for _, r := range c.Runs {
			runs++
			p := sourceMatchPlan(liveSubscription{ID: fixSub, OrgID: "org", WorkflowID: fixWorkflow, IdempotencyKeyTemplate: r.Template}, m, fixSource)
			obs := map[string]any{"orgId": "org", "sourceOutputId": nil, "consumerKind": "workflow", "consumerWorkflowId": fixWorkflow,
				"subscriptionId": fixSub, "title": strOrNil(p.Observation.Title), "body": strOrNil(p.Observation.Body), "mood": nil}
			if !reflect.DeepEqual(obs, r.Observation) {
				t.Fatalf("source %d observation: %v, want %v", i, obs, r.Observation)
			}
			obsID := "74000000-0000-4000-8000-000000000009"
			if got, want := jsStringify(p.TriggerPayload(obsID), false), fixtureJS(t, r.Enqueue.Payload.TriggerPayload); got != want {
				t.Fatalf("source %d trigger payload:\n%s\nwant\n%s", i, got, want)
			}
			audit := jsObjOf("orgId", "org", "sourceId", fixSource, "tableName", p.AuditTable, "changeKind", "subscription_match", "payload", p.Audit(obsID))
			if got, want := jsStringify(audit, false), fixtureJS(t, r.Audit); got != want {
				t.Fatalf("source %d audit:\n%s\nwant\n%s", i, got, want)
			}
			if p.DedupeKey != r.Enqueue.Opts.DedupeKey {
				t.Fatalf("source %d dedupe key: %q, want %q", i, p.DedupeKey, r.Enqueue.Opts.DedupeKey)
			}
		}
	}
	if runs < 10 {
		t.Fatalf("the fixture ran %d match cases", runs)
	}
}

const (
	smProducer  = "81000000-0000-4000-8000-000000000001"
	smConsumer  = "81000000-0000-4000-8000-000000000002"
	smThread    = "82000000-0000-4000-8000-000000000001"
	smWorkRun   = "83000000-0000-4000-8000-000000000001"
	smRun       = "84000000-0000-4000-8000-000000000001"
	smOutput    = "85000000-0000-4000-8000-000000000001"
	smSub       = "86000000-0000-4000-8000-000000000001"
	smSourceSub = "86000000-0000-4000-8000-000000000002"
	smSource    = "87000000-0000-4000-8000-000000000001"
)

func seedSubscriptionMatch(t *testing.T) (*API, *pgxpool.Pool) {
	t.Helper()
	db := testpg.Migrated(t)
	seedOrg(t, db)
	seed(t, db, `insert into workflow_definition (id, org_id, name, owner_user_id) values ($1, 'org', 'Producer', ''), ($2, 'org', 'Consumer', '')`, smProducer, smConsumer)
	seed(t, db, `insert into work_thread (id, org_id) values ($1, 'org')`, smThread)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, status) values ($1, 'org', $2, 'test', 'completed')`, smWorkRun, smThread)
	seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, status)
		values ($1, 'org', $2, $3, $4, 'manual', 'completed')`, smRun, smProducer, smThread, smWorkRun)
	seed(t, db, `insert into workflow_output (id, org_id, workflow_run_id, work_run_id, kind, title, mood)
		values ($1, 'org', $2, $3, 'alert', 'Low stock', 'act')`, smOutput, smRun, smWorkRun)
	seed(t, db, `insert into subscription (id, org_id, workflow_id, source_kind, filter) values
		($1, 'org', $3, 'workflow_output', '{"kinds": ["alert"]}'),
		($2, 'org', $3, 'source_change', '{"table": "product", "primary_key": ["id"], "version_column": "updated_at"}')`,
		smSub, smSourceSub, smConsumer)
	seed(t, db, `insert into data_source (id, org_id, kind, graphql_url) values ($1, 'org', 'graphjin', 'http://127.0.0.1:1/api/v1/graphql')`, smSource)
	a := &API{DB: db}
	seedRiver(t, db, &Front{API: a})
	return a, db
}

func liveSub(t *testing.T, a *API, id string) liveSubscription {
	t.Helper()
	subs, err := a.enabledSubscriptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range subs {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("subscription %s is not enabled", id)
	return liveSubscription{}
}

func TestSubscriptionMatchGuards(t *testing.T) {
	a, db := seedSubscriptionMatch(t)
	ctx := context.Background()
	sub := liveSub(t, a, smSub)
	act := "act"
	m := outputMatch{ID: smOutput, OrgID: "org", WorkflowRunID: smRun, Kind: "alert", Mood: &act, Title: "Low stock", CreatedAt: "2026-11-14T10:00:00Z"}

	d, err := a.handleOutputMatch(ctx, sub, m)
	if err != nil || d.Dropped != "" || d.JobID == "" {
		t.Fatalf("first match: %+v %v", d, err)
	}
	jobsNow := riverQueueJobs(t, db, "workflow_run_fire", "workflow_run")
	if len(jobsNow) != 1 {
		t.Fatalf("jobs = %d", len(jobsNow))
	}
	var args struct {
		TriggerKind, TriggeredBySubscriptionID, TriggeredByOutputID, TriggeredByObservationID, Dedupe string
		TriggerPayload                                                                                map[string]any
	}
	_ = json.Unmarshal([]byte(jobsNow[0].Args), &args)
	if args.TriggerKind != "subscription" || args.TriggeredBySubscriptionID != smSub || args.TriggeredByOutputID != smOutput ||
		args.TriggeredByObservationID != d.ObservationID || !strings.HasPrefix(args.Dedupe, smSub+":"+smOutput+":"+m.CreatedAt+"@") ||
		args.TriggerPayload["observation_id"] != d.ObservationID {
		t.Fatalf("job args = %s", jobsNow[0].Args)
	}
	var title, mood, kind string
	if err := db.QueryRow(ctx, `select title, mood, consumer_kind from observation where id = $1 and source_output_id = $2 and subscription_id = $3 and consumer_workflow_id = $4`,
		d.ObservationID, smOutput, smSub, smConsumer).Scan(&title, &mood, &kind); err != nil || title != "Low stock" || mood != "act" || kind != "workflow" {
		t.Fatalf("observation: %q %q %q %v", title, mood, kind, err)
	}

	d, err = a.handleOutputMatch(ctx, sub, m)
	if err != nil || d.Dropped != "" || d.JobID != "" {
		t.Fatalf("duplicate match: %+v %v", d, err)
	}
	if n := len(riverQueueJobs(t, db, "workflow_run_fire", "workflow_run")); n != 1 {
		t.Fatalf("a duplicate added a job: %d", n)
	}

	drop := func(name string, sub liveSubscription, m outputMatch, want string) {
		t.Helper()
		d, err := a.handleOutputMatch(ctx, sub, m)
		if err != nil || !strings.Contains(d.Dropped, want) {
			t.Fatalf("%s: %+v %v, want %q", name, d, err, want)
		}
	}
	other := m
	other.OrgID = "other"
	drop("org", sub, other, "org mismatch")
	two := 2
	deep := sub
	deep.MaxChainDepthOverride = &two
	seed(t, db, `update workflow_run set chain_depth = 2 where id = $1`, smRun)
	drop("chain", deep, m, "chain depth 3 exceeds max 2")
	seed(t, db, `update workflow_run set chain_depth = 0 where id = $1`, smRun)
	self := sub
	self.WorkflowID = smProducer
	drop("cycle", self, m, "cycle detected")
	seed(t, db, `insert into observation (org_id, source_output_id, consumer_kind) select 'org', $1, 'human' from generate_series(1, 30)`, smOutput)
	drop("fanout", sub, m, "fanout cap reached (32/32)")
	seed(t, db, `delete from observation`)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, status) values ('83000000-0000-4000-8000-000000000002', 'org', $1, 'test', 'running')`, smThread)
	seed(t, db, `insert into workflow_run (org_id, workflow_id, thread_id, work_run_id, trigger_kind, status, triggered_by_subscription_id)
		values ('org', $1, $2, '83000000-0000-4000-8000-000000000002', 'subscription', 'running', $3)`, smConsumer, smThread, smSub)
	one := sub
	one.MaxConcurrentRuns = 1
	drop("concurrency", one, m, "at max_concurrent_runs (1/1)")
	seed(t, db, `update workflow_definition set daily_run_budget = 1 where id = $1`, smConsumer)
	drop("budget", sub, m, "reached daily_run_budget (1/1)")
	seed(t, db, `update workflow_definition set daily_run_budget = null where id = $1`, smConsumer)

	src := liveSub(t, a, smSourceSub)
	var f map[string]any
	_ = json.Unmarshal(src.Filter, &f)
	sc, _ := parseSourceChange(f)
	msg, _ := jsParse([]byte(`{"data":{"product":[{"id":42,"name":"Mug","updated_at":"2026-11-14 10:00:00"}]}}`))
	sm, ok := parseSourceMatch(msg, sc)
	if !ok {
		t.Fatal("source match not parsed")
	}
	d, err = a.handleSourceMatch(ctx, src, sm, smSource)
	if err != nil || d.Dropped != "" || d.JobID == "" {
		t.Fatalf("source match: %+v %v", d, err)
	}
	var table, payload string
	if err := db.QueryRow(ctx, `select table_name, payload::text from source_change_log where source_id = $1 and change_kind = 'subscription_match'`,
		smSource).Scan(&table, &payload); err != nil || table != "product" || !strings.Contains(payload, d.ObservationID) {
		t.Fatalf("source_change_log: %q %s %v", table, payload, err)
	}
	seed(t, db, `update workflow_run set source_writes = '[{"table": "product", "primary_key": {"id": 42}}]' where id = $1`, smRun)
	seed(t, db, `update workflow_run set workflow_id = $1 where id = $2`, smConsumer, smRun)
	d, err = a.handleSourceMatch(ctx, src, sm, smSource)
	if err != nil || !strings.Contains(d.Dropped, "recently wrote to product:{\"id\":42}") {
		t.Fatalf("source write cycle: %+v %v", d, err)
	}
}

type fakeGraphjinWS struct {
	mu        sync.Mutex
	conns     int
	queries   []string
	completes int
	pongs     int
	send      chan string
}

func (g *fakeGraphjinWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
	if err != nil {
		return
	}
	defer conn.CloseNow() //nolint:errcheck
	if conn.Subprotocol() != "graphql-transport-ws" {
		return
	}
	g.mu.Lock()
	g.conns++
	first := g.conns == 1
	g.mu.Unlock()
	ctx := r.Context()
	var subID string
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg wsMessage
		_ = json.Unmarshal(data, &msg)
		switch msg.Type {
		case "connection_init":
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"connection_ack"}`))
		case "subscribe":
			subID = msg.ID
			var p struct{ Query string }
			_ = json.Unmarshal(msg.Payload, &p)
			g.mu.Lock()
			g.queries = append(g.queries, p.Query)
			g.mu.Unlock()
			if first {
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"complete","id":"`+subID+`"}`))
				continue
			}
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ping"}`))
			go func() {
				for body := range g.send {
					_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"next","id":"`+subID+`","payload":`+body+`}`))
				}
			}()
		case "pong":
			g.mu.Lock()
			g.pongs++
			g.mu.Unlock()
		case "complete":
			g.mu.Lock()
			g.completes++
			g.mu.Unlock()
		}
	}
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSubscriptionManagerFollowsGraphjin(t *testing.T) {
	a, db := seedSubscriptionMatch(t)
	seed(t, db, `update subscription set enabled = false where id = $1`, smSourceSub)
	g := &fakeGraphjinWS{send: make(chan string, 4)}
	srv := httptest.NewServer(g)
	defer srv.Close()
	t.Setenv("OPENNEKO_GRAPHJIN_URL", srv.URL)
	retry := subscriptionRetryMin
	subscriptionRetryMin = 50 * time.Millisecond
	defer func() { subscriptionRetryMin = retry }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &subscriptionManager{api: a, open: map[string]openSubscription{}}
	if err := m.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "a reconnect after the server ended the stream", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.conns >= 2 && len(g.queries) >= 2 && g.pongs >= 1
	})
	g.mu.Lock()
	q := g.queries[1]
	g.mu.Unlock()
	if !strings.Contains(q, `kind: { in: ["alert"] }`) || !strings.Contains(q, "org_id: { eq: $org_id }") {
		t.Fatalf("subscription query = %s", q)
	}
	g.send <- `{"data":{"workflow_output":[{"id":"` + smOutput + `","org_id":"org","workflow_run_id":"` + smRun +
		`","kind":"alert","scope":null,"topic":null,"mood":"act","title":"Low stock","created_at":"2026-11-14T10:00:00Z"}]}}`
	waitUntil(t, "the queued run", func() bool { return len(riverQueueJobs(t, db, "workflow_run_fire", "workflow_run")) == 1 })

	seed(t, db, `update subscription set enabled = false where id = $1`, smSub)
	if err := m.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.open) != 0 {
		t.Fatalf("open after disable = %d", len(m.open))
	}
	waitUntil(t, "the complete message", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.completes == 1
	})
	close(g.send)
}
