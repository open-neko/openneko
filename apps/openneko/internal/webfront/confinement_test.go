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
	ll := &confine.State{Mechanism: "landlock", ABI: 8, Confined: true}
	for i, c := range []struct {
		state      *confine.State
		network    *confine.State
		runner     *confine.State
		turnsOff   string
		wantState  string
		wantDetail string
		wantPrefix bool
	}{
		{ll, &confine.State{Mechanism: "landlock", ABI: 8, Confined: true, Gaps: []string{"connect to a named socket outside its list (ABI 9)"}}, &confine.State{Mechanism: "landlock+seccomp", ABI: 8, Confined: true}, "", "ok",
			"The network part of openneko serve (files, GraphJin) runs under landlock ABI 8: it cannot read the config folder or other files of this account. This system cannot refuse: connect to a named socket outside its list (ABI 9). The part that starts agent turns runs under landlock ABI 8. Each agent turn runs in a runner under landlock+seccomp ABI 8: it opens no socket and reaches only the files of its turn.", false},
		{ll, ll, &confine.State{Mechanism: "landlock+seccomp", ABI: 8, Confined: true}, "", "ok",
			"The network part of openneko serve (files, GraphJin) runs under landlock ABI 8: it cannot read the config folder or other files of this account. The part that starts agent turns runs under landlock ABI 8. Each agent turn runs in a runner under landlock+seccomp ABI 8: it opens no socket and reaches only the files of its turn.", false},
		{&confine.State{Reason: "on macOS 15.5 the agent runners confine themselves, and a confined process cannot start them"}, &confine.State{Mechanism: "seatbelt", Confined: true},
			&confine.State{Mechanism: "seatbelt", Confined: true}, "", "ok",
			"The network part of openneko serve (files, GraphJin) runs under seatbelt: it cannot read the config folder or other files of this account. The part that starts agent turns runs unconfined: on macOS 15.5 the agent runners confine themselves, and a confined process cannot start them. Each agent turn runs in a runner under seatbelt: it opens no socket and reaches only the files of its turn.", false},
		{&confine.State{Mechanism: "landlock", Reason: "Linux 4.18.0 has no Landlock"}, &confine.State{Mechanism: "landlock", Reason: "Linux 4.18.0 has no Landlock"}, &confine.State{Mechanism: "landlock+seccomp", Reason: "Linux 4.18.0 has no Landlock"},
			"agent confinement: off. Linux 4.18.0 has no Landlock, so agent turns are off", "failing", "agent confinement: off. Linux 4.18.0 has no Landlock, so agent turns are off", false},
		{ll, &confine.State{Mechanism: "landlock", Reason: "OPENNEKO_SERVE_CONFINE=off in development mode"}, nil, "", "failing",
			"the network part of openneko serve runs unconfined: OPENNEKO_SERVE_CONFINE=off in development mode", false},
		{&confine.State{Mechanism: "landlock", Reason: "OPENNEKO_SERVE_CONFINE=off in development mode"}, ll, nil, "", "failing",
			"openneko serve runs unconfined: OPENNEKO_SERVE_CONFINE=off in development mode", false},
		{&confine.State{Reason: "not built yet"}, &confine.State{Reason: "not built yet"}, &confine.State{Mechanism: "seatbelt", Confined: true}, "", "ok", "Confinement of openneko serve on " + runtime.GOOS + " is not built yet", true},
		{&confine.State{Reason: "confinement on windows is not built yet"}, &confine.State{Reason: "confinement on windows is not built yet"}, &confine.State{Reason: "confinement on windows is not built yet"}, "", "failing",
			"agent confinement: not available on this system yet: agent turns run unconfined", false},
		{nil, nil, nil, "", "failing", "openneko serve did not report its confinement; run serve from the same release as web", false},
		{ll, nil, nil, "", "failing", "openneko serve did not report its confinement; run serve from the same release as web", false},
	} {
		socket := filepath.Join(dir, "serve"+string(rune('a'+i))+".sock")
		ln, err := serve.Listen(socket)
		if err != nil {
			t.Fatal(err)
		}
		var raw, network, runner json.RawMessage
		if c.state != nil {
			raw, _ = json.Marshal(c.state)
		}
		if c.network != nil {
			network, _ = json.Marshal(c.network)
		}
		if c.runner != nil {
			runner, _ = json.Marshal(c.runner)
		}
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			_ = (&serve.Server{MaxTurns: 1, RequireDoor: true, Confinement: raw, NetworkConfinement: network, RunnerConfinement: runner, TurnsOff: c.turnsOff}).Serve(ctx, ln)
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
