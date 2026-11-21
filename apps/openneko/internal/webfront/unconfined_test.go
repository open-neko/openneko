package webfront

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

type statusFake struct{ turnsOff, hostID, kernel string }

func statusServe(t *testing.T, turnsOff string) {
	t.Helper()
	statusServeOn(t, statusFake{turnsOff: turnsOff, hostID: "machine-id:box-1", kernel: "Linux 4.18.0"})
}

func statusServeOn(t *testing.T, s statusFake) {
	t.Helper()
	socket := filepath.Join(testsock.Dir(t), "serve.sock")
	ln, err := serve.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	state := []byte(`{"mechanism":"landlock","confined":true}`)
	go func() {
		_ = (&serve.Server{MaxTurns: 1, RequireDoor: true, Confinement: state, NetworkConfinement: state, TurnsOff: s.turnsOff,
			Host: "box-1", HostID: s.hostID, Kernel: s.kernel}).Serve(ctx, ln)
		close(done)
	}()
	t.Cleanup(func() { stop(); <-done; _ = os.Remove(socket) })
	t.Setenv("OPENNEKO_SERVE_SOCKET", socket)
}

const noLandlockOff = "agent confinement: off. Linux 4.18.0 has no Landlock, so agent turns are off"

func operatorCall(f *Front, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/admin/confinement/unconfined", strings.NewReader(body))
	w := httptest.NewRecorder()
	f.API.OperatorHandler().ServeHTTP(w, r)
	return w
}

func TestNoWebRouteChangesTheUnconfinedOverride(t *testing.T) {
	f, db, _ := hostFront(t)
	statusServe(t, noLandlockOff)
	for _, who := range []string{"admin", "member"} {
		for _, method := range []string{"PUT", "POST", "PATCH", "DELETE"} {
			if w := call(f, method, "/api/v1/settings/agent/unconfined", `{"on":true,"confirm":"box-1"}`, sessionCookie(who)); w.Code < 400 {
				t.Fatalf("%s %s: %d %s", who, method, w.Code, w.Body.String())
			}
		}
	}
	var n int
	if err := db.QueryRow(context.Background(), `select count(*) from install_setting`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("install_setting rows = %d, %v", n, err)
	}
	w := call(f, "GET", "/api/v1/settings/agent/unconfined", "", sessionCookie("admin"))
	got := decode(t, w)
	commands, _ := got["commands"].(map[string]any)
	if w.Code != 200 || got["applies"] != false || commands["allow"] != UnconfinedAllowCommand || commands["deny"] != UnconfinedDenyCommand ||
		got["system"].(map[string]any)["available"] != true {
		t.Fatalf("GET: %d %v", w.Code, got)
	}
	if w := call(f, "GET", "/api/v1/settings/agent/unconfined", "", sessionCookie("member")); w.Code != 403 {
		t.Fatalf("a member reads the override: %d", w.Code)
	}
}

func TestTheOperatorCannotAllowUnconfinedTurnsWhereTheSystemConfines(t *testing.T) {
	f, db, _ := hostFront(t)
	if w := operatorCall(f, `{"on":true,"reason":"r","hostUser":"neko"}`); w.Code != 409 {
		t.Fatalf("no serve: %d %s", w.Code, w.Body.String())
	}
	statusServe(t, "")
	if w := operatorCall(f, `{"on":true,"reason":"r","hostUser":"neko"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "confines agent turns") {
		t.Fatalf("a system that confines: %d %s", w.Code, w.Body.String())
	}
	statusServeOn(t, statusFake{turnsOff: noLandlockOff, kernel: "Linux 4.18.0"})
	if w := operatorCall(f, `{"on":true,"reason":"r","hostUser":"neko"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "host id") {
		t.Fatalf("serve with no host id: %d %s", w.Code, w.Body.String())
	}
	statusServe(t, noLandlockOff)
	for _, body := range []string{`{"on":"yes","reason":"r","hostUser":"neko"}`, `{"on":true,"hostUser":"neko"}`, `{"on":true,"reason":"r"}`} {
		if w := operatorCall(f, body); w.Code != 400 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	var n int
	if err := db.QueryRow(context.Background(), `select count(*) from install_setting`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("install_setting rows = %d, %v", n, err)
	}
}

func TestTheOperatorOverrideIsAuditedBoundAndShownRed(t *testing.T) {
	f, db, _ := hostFront(t)
	ctx := context.Background()
	statusServe(t, noLandlockOff)
	if got := servicesByID(t, f, "?refresh=1")["agent-confinement"]; got["state"] != "failing" || got["detail"] != noLandlockOff {
		t.Fatalf("status with turns off: %v", got)
	}
	w := operatorCall(f, `{"on":true,"reason":"RHEL 8 host, no newer kernel before March","hostUser":"neko-ops"}`)
	got := decode(t, w)
	setting := got["setting"].(map[string]any)
	if w.Code != 200 || got["applies"] != true || setting["on"] != true || setting["host"] != "box-1" || setting["kernel"] != "Linux 4.18.0" ||
		setting["hostUser"] != "neko-ops" || setting["hostId"] != nil {
		t.Fatalf("allow: %d %v", w.Code, got)
	}
	status := servicesByID(t, f, "?refresh=1")["agent-confinement"]
	if detail, _ := status["detail"].(string); status["state"] != "failing" || !strings.HasPrefix(detail, "agent turns run WITHOUT OS confinement: host user neko-ops allowed it on box-1") {
		t.Fatalf("status with the override: %v", status)
	}

	statusServeOn(t, statusFake{turnsOff: noLandlockOff, hostID: "machine-id:box-2", kernel: "Linux 4.18.0"})
	status = servicesByID(t, f, "?refresh=1")["agent-confinement"]
	if detail, _ := status["detail"].(string); !strings.Contains(detail, "does not apply: the override was given for host box-1") {
		t.Fatalf("status on another host: %v", status)
	}
	statusServeOn(t, statusFake{turnsOff: noLandlockOff, hostID: "machine-id:box-1", kernel: "Linux 4.18.1"})
	if got := decode(t, call(f, "GET", "/api/v1/settings/agent/unconfined", "", sessionCookie("admin"))); got["applies"] != false ||
		!strings.Contains(got["notApplied"].(string), "Linux 4.18.1") {
		t.Fatalf("another kernel: %v", got)
	}

	statusServe(t, noLandlockOff)
	if w := operatorCall(f, `{"on":false,"hostUser":"root"}`); w.Code != 200 || decode(t, w)["setting"].(map[string]any)["on"] != false {
		t.Fatalf("deny: %d %s", w.Code, w.Body.String())
	}
	rows, err := db.Query(ctx, `select event, payload->>'hostUser', payload->>'host', payload->>'hostId', payload->>'kernel', payload->>'missing' from audit_chain
		where entity_kind = 'install_setting' order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for rows.Next() {
		var event, user, host, hostID, kernel, missing string
		if err := rows.Scan(&event, &user, &host, &hostID, &kernel, &missing); err != nil {
			t.Fatal(err)
		}
		if host != "box-1" || hostID != "machine-id:box-1" || kernel != "Linux 4.18.0" || missing != noLandlockOff {
			t.Errorf("%s: host %q id %q kernel %q missing %q", event, host, hostID, kernel, missing)
		}
		events = append(events, event+" "+user)
	}
	if strings.Join(events, ",") != "agent.unconfined_turns.on neko-ops,agent.unconfined_turns.off root" {
		t.Fatalf("audit events: %v", events)
	}
	if ok, _, _, err := f.API.verifyAuditChain(ctx, "org"); !ok || err != nil {
		t.Fatalf("the audit chain does not verify: %v", err)
	}
}

func TestATurnRunsUnconfinedOnlyOnTheHostAndKernelOfTheOverride(t *testing.T) {
	f, db, _ := hostFront(t)
	ctx := context.Background()
	a := f.API
	seed(t, db, `insert into work_thread (id, org_id, title) values ('00000000-0000-0000-0000-0000000000a1', 'org', 't')`)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ('00000000-0000-0000-0000-0000000000b1', 'org', '00000000-0000-0000-0000-0000000000a1', 'ax')`)
	job := rowText(t, db, `insert into processing_job (org_id, kind, status, trigger) values ('org', 'metric_refresh', 'running', 'test') returning id::text`)
	run := unconfinedRecord{workRun: "00000000-0000-0000-0000-0000000000b1", job: job}
	recorded := func() (bool, bool) {
		var r, j bool
		if err := db.QueryRow(ctx, `select (select ran_unconfined from work_run where id::text = $1), (select ran_unconfined from processing_job where id::text = $2)`,
			run.workRun, run.job).Scan(&r, &j); err != nil {
			t.Fatal(err)
		}
		return r, j
	}
	reset := func() {
		seed(t, db, `update work_run set ran_unconfined = false`)
		seed(t, db, `update processing_job set ran_unconfined = false`)
	}
	statusServe(t, noLandlockOff)
	if on, err := a.turnRunsUnconfined(ctx, a.serveSocket(), run); on || err != nil {
		t.Fatalf("no override: %v %v", on, err)
	}
	t.Setenv("OPENNEKO_AGENT_UNCONFINED", "on")
	seed(t, db, `insert into install_setting (name, value) values ('agent_turns_unconfined', '{"on":true}')`)
	if on, _ := a.turnRunsUnconfined(ctx, a.serveSocket(), run); on {
		t.Fatal("an override with no host id applied")
	}
	if w := operatorCall(f, `{"on":true,"reason":"r","hostUser":"neko"}`); w.Code != 200 {
		t.Fatalf("allow: %d %s", w.Code, w.Body.String())
	}
	if on, err := a.turnRunsUnconfined(ctx, a.serveSocket(), run); !on || err != nil {
		t.Fatalf("override on this host: %v %v", on, err)
	}
	if r, j := recorded(); !r || !j {
		t.Fatalf("records: work_run %v, processing_job %v", r, j)
	}
	for _, s := range []statusFake{
		{turnsOff: noLandlockOff, hostID: "machine-id:box-2", kernel: "Linux 4.18.0"},
		{turnsOff: noLandlockOff, hostID: "machine-id:box-1", kernel: "Linux 5.14.0"},
		{hostID: "machine-id:box-1", kernel: "Linux 4.18.0"},
	} {
		reset()
		statusServeOn(t, s)
		if on, err := a.turnRunsUnconfined(ctx, a.serveSocket(), run); on || err != nil {
			t.Fatalf("%+v: %v %v", s, on, err)
		}
		if r, j := recorded(); r || j {
			t.Fatalf("%+v recorded an unconfined turn", s)
		}
	}
}

func TestOnlyTheOperatorRouteWritesTheInstallSetting(t *testing.T) {
	root := filepath.Join("..", "..")
	write := regexp.MustCompile(`(?i)(insert\s+into|update|delete\s+from)\s+install_setting`)
	var writers []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err == nil && write.Match(raw) {
			writers = append(writers, filepath.ToSlash(path))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(writers, ",") != "../../internal/webfront/unconfined.go" {
		t.Fatalf("files that write install_setting: %v", writers)
	}
	raw, _ := os.ReadFile("unconfined.go")
	if strings.Contains(string(raw), "Getenv(") {
		t.Fatal("unconfined.go reads an environment variable")
	}
	api, _ := os.ReadFile("api.go")
	if regexp.MustCompile(`operatorUnconfined|"/api/v1/settings/agent/unconfined":\s*\{[^}]*"(PUT|POST|PATCH|DELETE)"`).Match(api) {
		t.Fatal("api.go routes a write of the unconfined override")
	}
}
