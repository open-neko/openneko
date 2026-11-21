package webfront

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func statusServe(t *testing.T, turnsOff string) {
	t.Helper()
	name := "serve-confines.sock"
	if turnsOff != "" {
		name = "serve-cannot.sock"
	}
	socket := filepath.Join(testsock.Dir(t), name)
	ln, err := serve.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	state := []byte(`{"mechanism":"landlock","confined":true}`)
	go func() {
		_ = (&serve.Server{MaxTurns: 1, RequireDoor: true, Confinement: state, NetworkConfinement: state, TurnsOff: turnsOff,
			Host: "box-1", Kernel: "Linux 4.18.0"}).Serve(ctx, ln)
		close(done)
	}()
	t.Cleanup(func() { stop(); <-done; _ = os.Remove(socket) })
	t.Setenv("OPENNEKO_SERVE_SOCKET", socket)
}

const noLandlockOff = "agent confinement: off. Linux 4.18.0 has no Landlock, so agent turns are off"

func TestUnconfinedTurnsCannotBeTurnedOnWhereTheSystemConfines(t *testing.T) {
	f, db, _ := hostFront(t)
	admin := sessionCookie("admin")
	w := call(f, "GET", "/api/v1/settings/agent/unconfined", "", admin)
	got := decode(t, w)
	if w.Code != 200 || got["setting"].(map[string]any)["on"] != false || got["system"].(map[string]any)["available"] != false {
		t.Fatalf("default: %d %v", w.Code, got)
	}
	if w := call(f, "PUT", "/api/v1/settings/agent/unconfined", `{"on":true,"confirm":"box-1"}`, admin); w.Code != 409 {
		t.Fatalf("no serve: %d %s", w.Code, w.Body.String())
	}
	statusServe(t, "")
	if w := call(f, "PUT", "/api/v1/settings/agent/unconfined", `{"on":true,"confirm":"box-1"}`, admin); w.Code != 409 || !strings.Contains(w.Body.String(), "confines agent turns") {
		t.Fatalf("a system that confines: %d %s", w.Code, w.Body.String())
	}
	var n int
	if err := db.QueryRow(context.Background(), `select count(*) from install_setting`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("install_setting rows = %d, %v", n, err)
	}
	if w := call(f, "PUT", "/api/v1/settings/agent/unconfined", `{"on":true,"confirm":"box-1"}`, sessionCookie("member")); w.Code != 403 {
		t.Fatalf("a member: %d %s", w.Code, w.Body.String())
	}
}

func TestUnconfinedTurnsOnAndOffAreAuditedAndShownRed(t *testing.T) {
	f, db, _ := hostFront(t)
	ctx := context.Background()
	admin := sessionCookie("admin")
	statusServe(t, noLandlockOff)
	if w := call(f, "PUT", "/api/v1/settings/agent/unconfined", `{"on":"yes"}`, admin); w.Code != 400 {
		t.Fatalf("on is not a boolean: %d", w.Code)
	}
	if w := call(f, "PUT", "/api/v1/settings/agent/unconfined", `{"on":true,"confirm":"box-2"}`, admin); w.Code != 400 || !strings.Contains(w.Body.String(), `\"box-1\"`) {
		t.Fatalf("a wrong host name: %d %s", w.Code, w.Body.String())
	}
	if got := servicesByID(t, f, "?refresh=1")["agent-confinement"]; got["state"] != "failing" || got["detail"] != noLandlockOff {
		t.Fatalf("status with turns off: %v", got)
	}
	w := call(f, "PUT", "/api/v1/settings/agent/unconfined", `{"on":true,"confirm":" box-1 "}`, admin)
	got := decode(t, w)
	setting := got["setting"].(map[string]any)
	if w.Code != 200 || setting["on"] != true || setting["host"] != "box-1" || setting["kernel"] != "Linux 4.18.0" || setting["reason"] != noLandlockOff {
		t.Fatalf("turn on: %d %v", w.Code, got)
	}
	status := servicesByID(t, f, "?refresh=1")["agent-confinement"]
	if detail, _ := status["detail"].(string); status["state"] != "failing" || !strings.HasPrefix(detail, "agent turns run WITHOUT OS confinement") {
		t.Fatalf("status with the setting on: %v", status)
	}
	if w := call(f, "PUT", "/api/v1/settings/agent/unconfined", `{"on":false}`, admin); w.Code != 200 || decode(t, w)["setting"].(map[string]any)["on"] != false {
		t.Fatalf("turn off: %d %s", w.Code, w.Body.String())
	}
	rows, err := db.Query(ctx, `select event, payload->>'actorUserId', payload->>'host', payload->>'kernel', payload->>'reason' from audit_chain
		where entity_kind = 'install_setting' order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for rows.Next() {
		var event, actor, host, kernel, reason string
		if err := rows.Scan(&event, &actor, &host, &kernel, &reason); err != nil {
			t.Fatal(err)
		}
		if actor != "admin" || host != "box-1" || kernel != "Linux 4.18.0" || reason != noLandlockOff {
			t.Errorf("%s: actor %q host %q kernel %q reason %q", event, actor, host, kernel, reason)
		}
		events = append(events, event)
	}
	if strings.Join(events, ",") != "agent.unconfined_turns.on,agent.unconfined_turns.off" {
		t.Fatalf("audit events: %v", events)
	}
	if ok, _, _, err := f.API.verifyAuditChain(ctx, "org"); !ok || err != nil {
		t.Fatalf("the audit chain does not verify: %v", err)
	}
}

func TestATurnRunsUnconfinedOnlyWithTheSettingAndASystemThatCannotConfine(t *testing.T) {
	f, db, _ := hostFront(t)
	ctx := context.Background()
	a := f.API
	seed(t, db, `insert into work_thread (id, org_id, title) values ('00000000-0000-0000-0000-0000000000a1', 'org', 't')`)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ('00000000-0000-0000-0000-0000000000b1', 'org', '00000000-0000-0000-0000-0000000000a1', 'ax')`)
	run := "00000000-0000-0000-0000-0000000000b1"
	ranUnconfined := func() bool {
		var v bool
		if err := db.QueryRow(ctx, `select ran_unconfined from work_run where id = $1`, run).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	statusServe(t, noLandlockOff)
	if on, err := a.turnRunsUnconfined(ctx, a.serveSocket(), run); on || err != nil || ranUnconfined() {
		t.Fatalf("setting off: %v %v", on, err)
	}
	t.Setenv("OPENNEKO_AGENT_UNCONFINED", "on")
	if on, _ := a.turnRunsUnconfined(ctx, a.serveSocket(), run); on {
		t.Fatal("an environment variable turned the setting on")
	}
	seed(t, db, `insert into install_setting (name, value) values ('agent_turns_unconfined', '{"on":true}')`)
	if on, err := a.turnRunsUnconfined(ctx, a.serveSocket(), run); !on || err != nil || !ranUnconfined() {
		t.Fatalf("setting on, system cannot confine: %v %v", on, err)
	}
	seed(t, db, `update work_run set ran_unconfined = false`)
	statusServe(t, "")
	if on, err := a.turnRunsUnconfined(ctx, a.serveSocket(), run); on || err != nil || ranUnconfined() {
		t.Fatalf("setting on, system confines: %v %v", on, err)
	}
}

func TestOnlyTheAdminRouteWritesTheInstallSetting(t *testing.T) {
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
}
