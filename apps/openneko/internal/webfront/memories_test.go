package webfront

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const (
	memTeam      = "a0000000-0000-4000-8000-000000000001"
	memDatabase  = "a0000000-0000-4000-8000-000000000002"
	memMine      = "a0000000-0000-4000-8000-000000000003"
	memOther     = "a0000000-0000-4000-8000-000000000004"
	memThread    = "a0000000-0000-4000-8000-000000000005"
	threadMine   = "b0000000-0000-4000-8000-000000000001"
	threadOther  = "b0000000-0000-4000-8000-000000000002"
	runMine      = "b1000000-0000-4000-8000-000000000001"
	pendMine     = "c0000000-0000-4000-8000-000000000001"
	pendOther    = "c0000000-0000-4000-8000-000000000002"
	pendRun      = "c0000000-0000-4000-8000-000000000003"
	pendNote     = "c0000000-0000-4000-8000-000000000004"
	unknownUUID  = "d0000000-0000-4000-8000-000000000001"
	memoryAction = `{"text":"Fiscal year starts in April"}`
)

func seedMemories(t *testing.T, db *pgxpool.Pool) {
	seedOrg(t, db)
	seed(t, db, `insert into app_user (id, org_id, email) values ('other', 'org', 'o@example.com')`)
	seed(t, db, `delete from item_grant where org_id = 'org' and item_type = 'team_memory'`)
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'team_memory', 'global' from user_group where org_id = 'org' and slug = 'everyone'`)
	seed(t, db, `insert into work_thread (id, org_id, created_by_user_id) values ($1, 'org', 'member'), ($2, 'org', 'other')`, threadMine, threadOther)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values ($1, 'org', $2, 'test', 'member', 'member')`, runMine, threadMine)
	for _, m := range []struct{ id, kind, scope, scopeID, user, text, updated string }{
		{memTeam, "business_rule", "global", "", "", "Revenue is net of refunds", "2026-11-01T08:00:00Z"},
		{memDatabase, "metric_definition", "database", "erp", "", "AOV excludes tax", "2026-11-01T09:00:00Z"},
		{memMine, "preference", "global", "", "member", "Show amounts in INR", "2026-11-01T10:00:00Z"},
		{memOther, "preference", "global", "", "other", "Use weeks", "2026-11-01T11:00:00Z"},
		{memThread, "thread_note", "thread", threadMine, "", "This thread is about Q3", "2026-11-01T12:00:00Z"},
	} {
		seed(t, db, `insert into work_memory (id, org_id, kind, scope, scope_id, text, pinned, user_id, origin_id, metadata, created_at, updated_at)
			values ($1, 'org', $2, $3, nullif($4, ''), $5, $2 <> 'thread_note', nullif($6, ''), $1, '{"b":1,"a":2}', $7, $7)`,
			m.id, m.kind, m.scope, m.scopeID, m.text, m.user, m.updated)
	}
	for _, p := range []struct{ id, thread, run, kind, scope, text, created string }{
		{pendMine, threadMine, "", "business_rule", "global", "Orders ship from Pune", "2026-11-02T08:00:00Z"},
		{pendOther, threadOther, "", "preference", "global", "Say hello", "2026-11-02T09:00:00Z"},
		{pendRun, threadMine, runMine, "preference", "thread", "Prefer charts", "2026-11-02T10:00:00Z"},
		{pendNote, threadMine, "", "thread_note", "thread", "Ask about Pune", "2026-11-02T11:00:00Z"},
	} {
		seed(t, db, `insert into work_pending_memory (id, org_id, thread_id, run_id, draft_text, draft_kind, draft_scope, confidence, conflict, created_at, updated_at)
			values ($1, 'org', $2, nullif($3, '')::uuid, $4, $5, $6, 1.5, '[{"memoryId":"x","text":"y","similarity":0.5}]', $7, $7)`,
			p.id, p.thread, p.run, p.text, p.kind, p.scope, p.created)
	}
}

func memoryFront(t *testing.T) (*Front, *pgxpool.Pool) {
	db := testpg.Migrated(t)
	seedMemories(t, db)
	t.Setenv("HOME", t.TempDir())
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("memory-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, db
}

func texts(t *testing.T, w interface{ Bytes() []byte }, list string) string {
	t.Helper()
	return titles(t, w.Bytes(), list, "text")
}

func TestWorkMemoryReads(t *testing.T) {
	f, db := memoryFront(t)
	admin, member, other := sessionCookie("admin"), sessionCookie("member"), sessionCookie("other")
	for _, c := range []struct {
		path string
		who  map[string]string
		want string
	}{
		{"/api/v1/work/memories", admin, "AOV excludes tax,Revenue is net of refunds,This thread is about Q3"},
		{"/api/v1/work/memories", member, "Show amounts in INR,Revenue is net of refunds,This thread is about Q3"},
		{"/api/work/memories", other, "Use weeks,Revenue is net of refunds,This thread is about Q3"},
	} {
		w := call(f, "GET", c.path, "", c.who)
		if got := texts(t, w.Body, "memories"); w.Code != 200 || got != c.want {
			t.Errorf("%s: %d %s", c.path, w.Code, got)
		}
	}
	for _, c := range []struct {
		id   string
		who  map[string]string
		want int
	}{
		{memTeam, member, 200}, {memDatabase, member, 404}, {memDatabase, admin, 200}, {memThread, member, 200},
		{memMine, member, 200}, {memMine, other, 404}, {memOther, admin, 200}, {unknownUUID, admin, 404}, {"nope", admin, 404},
	} {
		if w := call(f, "GET", "/api/v1/work/memories/"+c.id, "", c.who); w.Code != c.want {
			t.Errorf("GET %s: %d %s", c.id, w.Code, w.Body)
		}
	}
	w := call(f, "GET", "/api/v1/work/memories/"+memTeam, "", member)
	var v struct{ Memory map[string]any }
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if !strings.HasPrefix(w.Body.String(), `{"memory":{"integrityOk":true,"expiresAt":null,"id":"`+memTeam+`","orgId":"org","kind":"business_rule"`) ||
		!strings.Contains(w.Body.String(), `"metadata":{"a":2,"b":1}`) || !strings.HasSuffix(w.Body.String(), `"createdAt":"2026-11-01T08:00:00.000Z","updatedAt":"2026-11-01T08:00:00.000Z"}}`) {
		t.Errorf("memory body: %s", w.Body)
	}
	seed(t, db, `update work_memory set integrity_hmac = 'stale' where id = $1`, memTeam)
	if w := call(f, "GET", "/api/v1/work/memories/"+memTeam, "", member); !strings.Contains(w.Body.String(), `"integrityOk":false`) {
		t.Errorf("tampered row: %s", w.Body)
	}
	seed(t, db, `update work_memory set archived_at = now() where id = $1`, memDatabase)
	if w := call(f, "GET", "/api/v1/work/memories?includeArchived=true", "", admin); texts(t, w.Body, "memories") != "AOV excludes tax,Revenue is net of refunds,This thread is about Q3" {
		t.Errorf("archived: %s", w.Body)
	}
	if w := call(f, "GET", "/api/v1/work/memories", "", admin); texts(t, w.Body, "memories") != "Revenue is net of refunds,This thread is about Q3" {
		t.Errorf("without archived: %s", w.Body)
	}
	seed(t, db, `delete from item_grant where org_id = 'org' and item_type = 'team_memory'`)
	if w := call(f, "GET", "/api/v1/work/memories", "", member); texts(t, w.Body, "memories") != "Show amounts in INR,This thread is about Q3" {
		t.Errorf("no grant: %s", w.Body)
	}
	if w := call(f, "GET", "/api/v1/work/memories/"+memTeam, "", member); w.Code != 404 {
		t.Errorf("no grant detail: %d", w.Code)
	}
}

func TestWorkMemoryOverrides(t *testing.T) {
	f, db := memoryFront(t)
	admin, member, other := sessionCookie("admin"), sessionCookie("member"), sessionCookie("other")
	path := "/api/v1/work/memories/" + memTeam
	if w := call(f, "PATCH", path, memoryAction, admin); w.Code != 403 {
		t.Errorf("admin patch: %d", w.Code)
	}
	for _, body := range []string{`{}`, `{"text":""}`, `{"suppress":"yes"}`, `x`} {
		if w := call(f, "PATCH", path, body, member); w.Code != 400 || w.Body.String() != `{"error":"provide text or suppress"}` {
			t.Errorf("patch %s: %d %s", body, w.Code, w.Body)
		}
	}
	for _, id := range []string{unknownUUID, "nope", memOther} {
		if w := call(f, "PATCH", "/api/v1/work/memories/"+id, memoryAction, member); w.Code != 404 {
			t.Errorf("patch %s: %d", id, w.Code)
		}
	}
	w := call(f, "PATCH", path, `{"text":"  Revenue is gross  "}`, member)
	var v struct{ Memory memoryOut }
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	o := v.Memory
	if w.Code != 200 || o.Text != "Revenue is gross" || deref(o.UserID) != "member" || deref(o.OverridesOriginID) != memTeam || o.ID == memTeam || !o.Pinned || !strings.Contains(w.Body.String(), `"metadata":{"a":2,"b":1,"origin":"user_override"}`) {
		t.Fatalf("override: %d %s", w.Code, w.Body)
	}
	if w := call(f, "PATCH", path, `{"text":"Revenue is gross of tax"}`, member); !strings.Contains(w.Body.String(), `"id":"`+o.ID+`"`) {
		t.Errorf("second override made a new row: %s", w.Body)
	}
	if w := call(f, "GET", "/api/v1/work/memories", "", member); texts(t, w.Body, "memories") != "Revenue is gross of tax,Show amounts in INR,This thread is about Q3" {
		t.Errorf("member list after override: %s", w.Body)
	}
	if w := call(f, "GET", "/api/v1/work/memories", "", other); texts(t, w.Body, "memories") != "Use weeks,Revenue is net of refunds,This thread is about Q3" {
		t.Errorf("other list after override: %s", w.Body)
	}
	if w := call(f, "PATCH", "/api/v1/work/memories/"+memMine, `{"suppress":true}`, member); w.Code != 200 || !strings.Contains(w.Body.String(), `"suppressed":true`) {
		t.Errorf("suppress own: %d %s", w.Code, w.Body)
	}
	var events, forks int
	_ = db.QueryRow(context.Background(), `select count(*) filter (where action = 'override'), count(*) filter (where action = 'suppress') from work_memory_event`).Scan(&events, &forks)
	if events != 2 || forks != 1 {
		t.Errorf("events: %d override, %d suppress", events, forks)
	}
	_ = db.QueryRow(context.Background(), `select count(*) from memory_fork where user_id = 'member'`).Scan(&forks)
	if forks != 1 {
		t.Errorf("forks: %d", forks)
	}
	var hash string
	_ = db.QueryRow(context.Background(), `select integrity_hmac from work_memory where id = $1`, o.ID).Scan(&hash)
	if want := f.API.memoryHMAC("org", "member", "business_rule", "global", "", "Revenue is gross of tax"); want == nil || *want != hash {
		t.Errorf("override hmac %q", hash)
	}

	if w := call(f, "DELETE", "/api/v1/work/memories/"+memOther, `{}`, member); w.Code != 404 || w.Body.String() != `{"error":"not found"}` {
		t.Errorf("delete other's: %d %s", w.Code, w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/work/memories/"+memDatabase, `{}`, other); w.Body.String() != `{"ok":true,"suppressed":true}` {
		t.Errorf("hide team row: %s", w.Body)
	}
	if w := call(f, "GET", "/api/v1/work/memories?includeArchived=true", "", admin); !strings.Contains(w.Body.String(), "AOV excludes tax") {
		t.Errorf("hiding archived the team row: %s", w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/work/memories/"+memOther, `{"reason":"old"}`, other); w.Body.String() != `{"ok":true}` {
		t.Errorf("delete own: %s", w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/work/memories/"+memOther, `{}`, other); w.Code != 404 || w.Body.String() != `{"error":"not found or already archived"}` {
		t.Errorf("delete twice: %d %s", w.Code, w.Body)
	}
	var payload string
	_ = db.QueryRow(context.Background(), `select payload::text from work_memory_event where memory_id = $1 and action = 'forget'`, memOther).Scan(&payload)
	if payload != `{"reason": "old"}` {
		t.Errorf("forget payload %s", payload)
	}
	if w := call(f, "DELETE", "/api/v1/work/memories/"+memThread, ``, admin); w.Body.String() != `{"ok":true}` {
		t.Errorf("admin delete: %s", w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/work/memories/nope", `{}`, admin); w.Code != 404 {
		t.Errorf("admin delete bad id: %d", w.Code)
	}
}

func TestWorkMemoryPromote(t *testing.T) {
	f, db := memoryFront(t)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	call(f, "PATCH", "/api/v1/work/memories/"+memTeam, `{"text":"Revenue is gross"}`, member)
	var override string
	_ = db.QueryRow(context.Background(), `select id::text from work_memory where overrides_origin_id = $1`, memTeam).Scan(&override)
	if w := call(f, "POST", "/api/v1/work/memories/"+override+"/promote", ``, member); w.Code != 403 || w.Body.String() != `{"error":"admin only"}` {
		t.Errorf("member promote: %d %s", w.Code, w.Body)
	}
	for id, want := range map[string]string{
		unknownUUID: `{"error":"not found"}`,
		memTeam:     `{"error":"Personal memory not found: ` + memTeam + `"}`,
	} {
		if w := call(f, "POST", "/api/v1/work/memories/"+id+"/promote", ``, admin); w.Code != 404 || w.Body.String() != want {
			t.Errorf("promote %s: %d %s", id, w.Code, w.Body)
		}
	}
	w := call(f, "POST", "/api/v1/work/memories/"+override+"/promote", ``, admin)
	var v struct{ Memory memoryOut }
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	m := v.Memory
	if w.Code != 200 || m.UserID != nil || deref(m.OriginID) != memTeam || deref(m.PromotedFromID) != override || deref(m.PromotedBy) != "admin" ||
		m.Text != "Revenue is gross" || !strings.Contains(w.Body.String(), `"origin":"promoted"`) {
		t.Fatalf("promote: %d %s", w.Code, w.Body)
	}
	var archived int
	_ = db.QueryRow(context.Background(), `select count(*) from work_memory where id in ($1, $2) and archived_at is not null`, memTeam, override).Scan(&archived)
	if archived != 2 {
		t.Errorf("archived %d of the old team row and the override", archived)
	}
	if w := call(f, "GET", "/api/v1/work/memories", "", member); texts(t, w.Body, "memories") != "Revenue is gross,Show amounts in INR,This thread is about Q3" {
		t.Errorf("member list after promote: %s", w.Body)
	}
	root := orgAgentRoot("org")
	file, err := os.ReadFile(filepath.Join(root, "memory", "business_rule", m.ID+".md"))
	want := "---\ntype: business_rule\nid: " + m.ID + "\nkind: business_rule\npinned: true\nupdated_at: " + m.UpdatedAt + "\n---\n\nRevenue is gross\n"
	if err != nil || string(file) != want {
		t.Errorf("snapshot file: %v %q", err, file)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "metric_definition", memDatabase+".md")); err != nil {
		t.Errorf("snapshot misses the database memory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "preference")); err == nil {
		t.Errorf("snapshot holds a personal memory")
	}
	out, _ := exec.Command("git", "-C", root, "log", "--format=%s %an").Output()
	if string(out) != "Memory checkpoint OpenNeko\nInitialize config versioning OpenNeko\n" {
		t.Errorf("git log: %q", out)
	}
	head, _ := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	var ref, change string
	_ = db.QueryRow(context.Background(), `select commit_sha from config_ref where org_id = 'org' and scope = 'team'`).Scan(&ref)
	_ = db.QueryRow(context.Background(), `select concat_ws('|', artifact_kind, artifact_ref, scope, user_id, actor_user_id, summary, status) from config_change`).Scan(&change)
	if ref != strings.TrimSpace(string(head)) || change != "memory|"+m.ID+"|team|member|admin|Promoted a personal memory to the team|promoted" {
		t.Errorf("config_ref %q, head %q, config_change %q", ref, head, change)
	}
	if w := call(f, "POST", "/api/v1/work/memories/"+override+"/promote", ``, admin); w.Code != 404 {
		t.Errorf("promote twice: %d", w.Code)
	}
}

func TestPendingMemories(t *testing.T) {
	f, db := memoryFront(t)
	admin, member, other := sessionCookie("admin"), sessionCookie("member"), sessionCookie("other")
	for _, c := range []struct {
		path string
		who  map[string]string
		code int
		want string
	}{
		{"/api/v1/work/memories/pending", member, 200, "Orders ship from Pune,Prefer charts,Ask about Pune"},
		{"/api/v1/work/memories/pending", other, 200, "Say hello"},
		{"/api/v1/work/memories/pending", admin, 200, ""},
		{"/api/v1/work/memories/pending?runId=" + runMine, member, 200, "Prefer charts"},
		{"/api/v1/work/memories/pending?threadId=" + threadMine, member, 200, "Orders ship from Pune,Prefer charts,Ask about Pune"},
		{"/api/v1/work/memories/pending?threadId=" + threadMine, admin, 404, ""},
		{"/api/v1/work/memories/pending?runId=" + runMine, other, 404, ""},
		{"/api/v1/work/memories/pending?threadId=nope", member, 404, ""},
	} {
		w := call(f, "GET", c.path, "", c.who)
		got := ""
		if c.code == 200 {
			got = titles(t, w.Body.Bytes(), "pending", "draftText")
		}
		if w.Code != c.code || got != c.want {
			t.Errorf("%s: %d %s", c.path, w.Code, w.Body)
		}
	}
	w := call(f, "GET", "/api/v1/work/memories/pending?runId="+runMine, "", member)
	if !strings.Contains(w.Body.String(), `"confidence":1.5,"reasoning":null,"conflicts":[{"text":"y","memoryId":"x","similarity":0.5}],"decisionText":null`) {
		t.Errorf("pending body: %s", w.Body)
	}

	for _, c := range []struct {
		body string
		who  map[string]string
		code int
		want string
	}{
		{`{}`, member, 400, `{"error":"id is required"}`},
		{`{"id":"` + unknownUUID + `","action":"decline"}`, member, 404, `{"error":"not found"}`},
		{`{"id":"` + pendOther + `","action":"decline"}`, member, 404, `{"error":"not found"}`},
		{`{"id":"` + pendMine + `","action":"keep"}`, member, 400, `{"error":"action must be accept or decline"}`},
	} {
		if w := call(f, "POST", "/api/v1/work/memories/decide", c.body, c.who); w.Code != c.code || w.Body.String() != c.want {
			t.Errorf("decide %s: %d %s", c.body, w.Code, w.Body)
		}
	}
	w = call(f, "POST", "/api/v1/work/memories/decide", `{"id":"`+pendMine+`","action":"accept","text":" Orders ship from Pune and Delhi ","scope":"database","scopeId":"erp"}`, member)
	var v struct {
		Pending pendingOut
		Memory  memoryOut
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"ok":true,"pending":{`) || v.Pending.Status != "accepted" ||
		deref(v.Pending.DecisionText) != "Orders ship from Pune and Delhi" || deref(v.Pending.MemoryID) != v.Memory.ID ||
		v.Memory.Scope != "global" || v.Memory.ScopeID != nil || !v.Memory.Pinned || v.Memory.Confidence != 1 || v.Memory.UserID != nil ||
		v.Memory.Text != "Orders ship from Pune and Delhi" || deref(v.Memory.OriginID) != v.Memory.ID || deref(v.Memory.SourceThreadID) != threadMine ||
		!strings.Contains(w.Body.String(), `"metadata":{"origin":"auto_memory_accepted","pendingId":"`+pendMine+`"}`) {
		t.Errorf("accept: %d %s", w.Code, w.Body)
	}
	if w := call(f, "POST", "/api/v1/work/memories/decide", `{"id":"`+pendMine+`","action":"decline"}`, member); w.Code != 400 || w.Body.String() != `{"error":"Pending memory `+pendMine+` already accepted"}` {
		t.Errorf("decide twice: %d %s", w.Code, w.Body)
	}
	w = call(f, "POST", "/api/v1/work/memories/decide", `{"id":"`+pendRun+`","action":"accept","pinned":true}`, member)
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if v.Memory.Scope != "thread" || deref(v.Memory.ScopeID) != threadMine || deref(v.Memory.UserID) != "member" || !v.Memory.Pinned || v.Pending.DecisionText != nil {
		t.Errorf("accept from a member run: %s", w.Body)
	}
	w = call(f, "POST", "/api/v1/work/memories/decide", `{"id":"`+pendNote+`","action":"accept","scope":"bogus"}`, member)
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if v.Memory.Scope != "thread" || v.Memory.Pinned || v.Memory.ExpiresAt == nil {
		t.Errorf("accept a thread note: %s", w.Body)
	}
	w = call(f, "POST", "/api/v1/work/memories/decide", `{"id":"`+pendOther+`","action":"decline","reason":"wrong"}`, other)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"ok":true,"pending":{`) || strings.Contains(w.Body.String(), `"memory"`) ||
		!strings.Contains(w.Body.String(), `"status":"declined"`) || !strings.Contains(w.Body.String(), `"decisionText":"wrong"`) {
		t.Errorf("decline: %d %s", w.Code, w.Body)
	}
	var payload string
	_ = db.QueryRow(context.Background(), `select payload::text from work_memory_event where action = 'remember' and run_id = $1`, runMine).Scan(&payload)
	if payload != `{"kind": "preference", "text": "Prefer charts", "scope": "thread", "pinned": true, "userId": "member", "scopeId": "`+threadMine+`"}` {
		t.Errorf("remember payload %s", payload)
	}
}

func TestMemoryPull(t *testing.T) {
	f, db := memoryFront(t)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	if w := call(f, "GET", "/api/v1/work/memories/pull", "", admin); w.Body.String() != `{"updates":[]}` {
		t.Errorf("admin pull: %s", w.Body)
	}
	if w := call(f, "POST", "/api/v1/work/memories/pull", `{"decisions":[]}`, admin); w.Code != 403 || w.Body.String() != `{"error":"members only"}` {
		t.Errorf("admin apply: %d %s", w.Code, w.Body)
	}
	call(f, "PATCH", "/api/v1/work/memories/"+memTeam, `{"text":"Revenue is gross"}`, member)
	call(f, "DELETE", "/api/v1/work/memories/"+memDatabase, `{}`, member)
	if w := call(f, "GET", "/api/v1/work/memories/pull", "", member); w.Body.String() != `{"updates":[]}` {
		t.Errorf("no team change: %s", w.Body)
	}
	seed(t, db, `update work_memory set text = 'Revenue is net', updated_at = now() + interval '1 second' where id = $1`, memTeam)
	seed(t, db, `update work_memory set archived_at = now() where id = $1`, memDatabase)
	w := call(f, "GET", "/api/v1/work/memories/pull", "", member)
	var v struct {
		Updates []struct {
			OriginID    string
			Override    memoryOut
			TeamMemory  *memoryOut
			TeamRemoved bool
		}
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	got := map[string]string{}
	for _, u := range v.Updates {
		team := "removed"
		if u.TeamMemory != nil {
			team = u.TeamMemory.Text
		}
		got[u.OriginID] = u.Override.Text + "|" + team
	}
	if len(got) != 2 || got[memTeam] != "Revenue is gross|Revenue is net" || got[memDatabase] != "AOV excludes tax|removed" {
		t.Errorf("updates: %s", w.Body)
	}
	seed(t, db, `insert into config_ref (org_id, scope, commit_sha) values ('org', 'team', 'abc')`)
	w = call(f, "POST", "/api/v1/work/memories/pull", `{"decisions":[{"originId":"`+memTeam+`","choice":"take_theirs"},{"originId":"`+memDatabase+`","choice":"keep_mine"},{"originId":"nope","choice":"take_theirs"},"x"]}`, member)
	if w.Body.String() != `{"ok":true,"applied":1}` {
		t.Errorf("apply: %s", w.Body)
	}
	if w := call(f, "GET", "/api/v1/work/memories", "", member); texts(t, w.Body, "memories") != "Revenue is net,Show amounts in INR,This thread is about Q3" {
		t.Errorf("list after pull: %s", w.Body)
	}
	var sha string
	_ = db.QueryRow(context.Background(), `select baseline_sha from memory_fork where user_id = 'member'`).Scan(&sha)
	if sha != "abc" {
		t.Errorf("baseline sha %q", sha)
	}
	if w := call(f, "GET", "/api/v1/work/memories/pull", "", member); titles(t, w.Body.Bytes(), "updates", "originId") != memDatabase {
		t.Errorf("after pull: %s", w.Body)
	}
}
