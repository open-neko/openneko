package webfront

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func TestAdminServicesShowServesConfinement(t *testing.T) {
	f, _, _ := hostFront(t)
	dir := testsock.Dir(t)
	for i, c := range []struct {
		state      *confine.State
		wantState  string
		wantDetail string
		wantPrefix bool
	}{
		{&confine.State{Mechanism: "landlock", ABI: 8, Confined: true, Gaps: []string{"connect to a named socket outside its list (ABI 9)"}}, "ok",
			"openneko serve runs under landlock ABI 8: it cannot read the config folder or other files of this account. This kernel cannot refuse: connect to a named socket outside its list (ABI 9).", false},
		{&confine.State{Mechanism: "landlock", Reason: "OPENNEKO_SERVE_CONFINE=off in development mode"}, "failing",
			"openneko serve runs unconfined: OPENNEKO_SERVE_CONFINE=off in development mode", false},
		{&confine.State{Reason: "confinement on darwin is not built yet"}, "ok", "Confinement of openneko serve on " + runtime.GOOS + " is not built yet", true},
		{nil, "failing", "openneko serve did not report its confinement; run serve from the same release as web", false},
	} {
		socket := filepath.Join(dir, "serve"+string(rune('a'+i))+".sock")
		ln, err := serve.Listen(socket)
		if err != nil {
			t.Fatal(err)
		}
		var raw json.RawMessage
		if c.state != nil {
			raw, _ = json.Marshal(c.state)
		}
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			_ = (&serve.Server{MaxTurns: 1, RequireDoor: true, Confinement: raw}).Serve(ctx, ln)
			close(done)
		}()
		t.Setenv("OPENNEKO_SERVE_SOCKET", socket)
		got := servicesByID(t, f, "?refresh=1")["agent-confinement"]
		stop()
		<-done
		_ = os.Remove(socket)
		detail, _ := got["detail"].(string)
		match := detail == c.wantDetail || (c.wantPrefix && strings.HasPrefix(detail, c.wantDetail))
		if got["state"] != c.wantState || !match || got["where"] != "openneko serve" {
			t.Errorf("case %d: %v", i, got)
		}
	}
}
