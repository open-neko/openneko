package cli

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func stubConfinement(t *testing.T, serveState, runnerState confine.State) {
	t.Helper()
	oldServe, oldRunner := serveAvailable, runnerAvailable
	t.Cleanup(func() { serveAvailable, runnerAvailable = oldServe, oldRunner })
	serveAvailable = func() confine.State { return serveState }
	runnerAvailable = func() confine.State { return runnerState }
}

func TestServeStartsWithoutLandlockAndRefusesOnlyTurns(t *testing.T) {
	t.Setenv(serveConfineEnv, "")
	noLandlock := "Linux 4.18.0 has no Landlock: use kernel 5.13 or later with landlock in the lsm= boot list"
	stubConfinement(t, confine.State{Mechanism: "landlock", Reason: noLandlock}, confine.State{Mechanism: "landlock+seccomp", Reason: noLandlock})
	var log strings.Builder
	st, err := confineServe(&log, serveFolders{agentRuns: filepath.Join(t.TempDir(), "runs")}, false)
	if err != nil || st.Confined || !strings.Contains(log.String(), "runs unconfined") {
		t.Fatalf("production on a kernel without Landlock: %v, %v, %q", st, err, log.String())
	}
	_, confineIt, off := runnerConfinement(&log, false)
	if confineIt || off != turnsOffLine(confine.State{Mechanism: "landlock+seccomp", Reason: noLandlock}) {
		t.Fatalf("agent turns on a kernel without Landlock: confine=%v off=%q", confineIt, off)
	}
}

func TestNoRunnerConfinementIsSaidOnStartDoctorAndReleaseCheck(t *testing.T) {
	t.Setenv(serveConfineEnv, "")
	notBuilt := confine.State{Reason: "confinement on windows is not built yet"}
	stubConfinement(t, notBuilt, notBuilt)
	var start strings.Builder
	want := confine.NotAvailable + " (confinement on windows is not built yet), so agent turns are off"
	if _, confineIt, off := runnerConfinement(&start, false); confineIt || off != want || !strings.Contains(start.String(), unconfinedHint) {
		t.Fatalf("start: confine=%v off=%q log=%q", confineIt, off, start.String())
	}
	var doctor strings.Builder
	if !confinementDoctorLines(context.Background(), &doctor) || !strings.Contains(doctor.String(), want) {
		t.Fatalf("doctor: %q", doctor.String())
	}
	t.Setenv("OPENNEKO_ENV", "production")
	cleanServeEnv(t)
	sock := filepath.Join(testsock.Dir(t), "serve.sock")
	ctx, stop := context.WithCancel(context.Background())
	done, err := serveAgentTurns(ctx, io.Discard, sock, 1, turnHostStart{sandbox: sandbox.Config{}}, notBuilt, notBuilt)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = checkServeSocket(context.Background(), sock)
	stop()
	<-done
	if err == nil || !strings.Contains(err.Error(), confine.NotAvailable) {
		t.Fatalf("release-check passed a serve whose agent turns run unconfined: %v", err)
	}
}

// TestDoctorReportsWhatServeDoes: serve started when it could not confine
// its runners, and the system can confine them now. Doctor says what serve
// does, and falls back to what the system can give only when serve does not
// answer.
func TestDoctorReportsWhatServeDoes(t *testing.T) {
	t.Setenv(serveConfineEnv, "")
	t.Setenv("OPENNEKO_ENV", "production")
	cleanServeEnv(t)
	t.Setenv(webAdminSocketEnv, filepath.Join(testsock.Dir(t), "no-web.sock"))
	off := confine.State{Mechanism: "landlock+seccomp", Reason: "Linux 4.18.0 has no Landlock"}
	stubConfinement(t, confine.State{Mechanism: "landlock", Reason: off.Reason}, off)
	sock := filepath.Join(testsock.Dir(t), "serve.sock")
	ctx, stop := context.WithCancel(context.Background())
	done, err := serveAgentTurns(ctx, io.Discard, sock, 1, turnHostStart{sandbox: sandbox.Config{}}, confine.State{Mechanism: "landlock", Reason: off.Reason}, confine.State{Mechanism: "landlock", Reason: off.Reason})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_SERVE_SOCKET", sock)
	on := confine.State{Mechanism: "landlock+seccomp", ABI: 8, Confined: true}
	stubConfinement(t, on, on)

	var out strings.Builder
	failed := confinementDoctorLines(context.Background(), &out)
	for _, want := range []string{"openneko serve answers on " + sock, "agent turns: unconfined: " + off.Reason, "so agent turns are off"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor does not say %q:\n%s", want, out.String())
		}
	}
	if !failed {
		t.Errorf("doctor passed a serve that refuses agent turns:\n%s", out.String())
	}

	stop()
	<-done
	out.Reset()
	if confinementDoctorLines(context.Background(), &out) || !strings.Contains(out.String(), "openneko serve: no answer") || !strings.Contains(out.String(), "agent confinement: landlock+seccomp ABI 8") {
		t.Errorf("with no serve, doctor did not say what the system can give:\n%s", out.String())
	}
}
