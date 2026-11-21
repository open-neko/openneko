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
	if confineIt || off != turnsOffLine(confine.State{Reason: noLandlock}) {
		t.Fatalf("agent turns on a kernel without Landlock: confine=%v off=%q", confineIt, off)
	}
}

func TestNoRunnerConfinementIsSaidOnStartDoctorAndReleaseCheck(t *testing.T) {
	t.Setenv(serveConfineEnv, "")
	notBuilt := confine.State{Reason: "confinement on windows is not built yet"}
	stubConfinement(t, notBuilt, notBuilt)
	var start strings.Builder
	if _, confineIt, off := runnerConfinement(&start, false); confineIt || off != "" || !strings.Contains(start.String(), confine.NotAvailable) {
		t.Fatalf("start: confine=%v off=%q log=%q", confineIt, off, start.String())
	}
	var doctor strings.Builder
	confinementDoctorLines(&doctor)
	if !strings.Contains(doctor.String(), confine.NotAvailable+" (confinement on windows is not built yet)") {
		t.Fatalf("doctor: %q", doctor.String())
	}
	t.Setenv("OPENNEKO_ENV", "production")
	cleanServeEnv(t)
	sock := filepath.Join(testsock.Dir(t), "serve.sock")
	ctx, stop := context.WithCancel(context.Background())
	done, err := serveAgentTurns(ctx, io.Discard, sock, 1, sandbox.Config{}, notBuilt, notBuilt)
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
