package webfront

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

const hostileOverride = `{"on":true,"enabled":true,"unconfined":true,"allowUnconfined":true,"allow_unconfined":true,
"confinement":"off","agentConfinement":"off","runUnconfined":true,"reason":"planted"}`

func refusingServe(t *testing.T, srv *serve.Server) string {
	t.Helper()
	dir := testsock.Dir(t)
	socket := filepath.Join(dir, "serve.sock")
	ln, err := serve.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	serveDoor(t, socket)
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { stop(); <-done; os.RemoveAll(dir) })
	return socket
}

// TestNoSettingRunsATurnWhereServeCannotConfine writes every former override
// through each settings and admin route an admin can call, sets the old
// switches in the environment, and then asks a question on a serve that
// cannot confine. The turn must not reach the model.
func TestNoSettingRunsATurnWhereServeCannotConfine(t *testing.T) {
	f, _, _ := chatTurnFront(t)
	for _, kv := range [][2]string{{"OPENNEKO_SERVE_CONFINE", "off"}, {"OPENNEKO_ENV", "development"},
		{"OPENNEKO_ALLOW_UNCONFINED", "1"}, {"OPENNEKO_AGENT_UNCONFINED", "1"}} {
		t.Setenv(kv[0], kv[1])
	}
	admin := sessionCookie("admin")
	writes := 0
	for path, methods := range GoRoutes {
		if strings.ContainsAny(path, "{:") || !(strings.HasPrefix(path, "/api/v1/settings") || strings.HasPrefix(path, "/api/v1/admin")) {
			continue
		}
		for _, m := range methods {
			if m == http.MethodPatch || m == http.MethodPut {
				call(f, m, path, hostileOverride, admin)
				writes++
			}
		}
	}
	for _, old := range []string{"/api/v1/settings/agent/unconfined", "/api/v1/admin/confinement"} {
		call(f, http.MethodPost, old, hostileOverride, admin)
		call(f, http.MethodPut, old, hostileOverride, admin)
	}
	if writes < 10 {
		t.Fatalf("only %d settings writes ran; the route list changed shape", writes)
	}
	h := (&API{}).OperatorHandler()
	for _, probe := range []string{"/admin/confinement/unconfined", "/confinement", "/health/agent-unconfined"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, probe, strings.NewReader(hostileOverride)))
	}

	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(model.Close)

	for name, srv := range map[string]*serve.Server{
		"turns off":     {Getenv: os.Getenv, MaxTurns: 2, TurnsOff: "agent confinement: off. this test system cannot confine an agent runner"},
		"no runner set": {Getenv: os.Getenv, MaxTurns: 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENNEKO_SERVE_SOCKET", refusingServe(t, srv))
			res, err := f.API.runChatTurn(context.Background(), chatTurnOptions{OrgID: "org", ThreadID: threadMine, RunID: runAdmin,
				Message: "What changed?", Channel: "web", NativeDelegationOff: true, Emit: (&eventLog{}).emit,
				Summarize: noSummary(t), resolve: fixedBackend(model.URL)})
			if err == nil && res.Status == "completed" {
				t.Fatalf("a turn completed on a serve that cannot confine: %+v", res)
			}
			if n := modelCalls.Load(); n != 0 {
				t.Errorf("the turn reached the model %d times", n)
			}
		})
	}
}
