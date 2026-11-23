package webfront

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
)

// finishedServe answers one turn with a completed result and, when exit is
// set, the exit frame that serve sends after the runner's group is dead.
func finishedServe(t *testing.T, exit bool) string {
	t.Helper()
	socket := filepath.Join(testsock.Dir(t), "serve.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
			return
		}
		lines := []string{`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":1,"type":"run.finished","result":{"status":"completed","answer":"Done."}}}`}
		if exit {
			lines = append(lines, `{"type":"exit","code":0}`)
		}
		for _, l := range lines {
			if _, err := conn.Write([]byte(l + "\n")); err != nil {
				return
			}
		}
	}()
	return socket
}

func TestArtifactsComeBackOnlyAfterServeReportsTheRunnersEnd(t *testing.T) {
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENNEKO_SANDBOX_URL", "")
	clearAxEnv(t, nil)
	for _, exit := range []bool{true, false} {
		host, err := turnWorkspace("org", "thread-1", "run-1")
		if err != nil {
			t.Fatal(err)
		}
		os.RemoveAll(host.ArtifactRoot)
		st, err := stageTurn(host, stageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(st.Workspace.ArtifactRoot, "chart.csv"), "a,b")
		res, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-1", RunID: "run-1", RunKind: "work",
			OrgID: "org", Socket: finishedServe(t, exit), Workspace: &st.Workspace, Emit: func(context.Context, any) error { return nil }})
		if err != nil || res.Status != "completed" {
			t.Fatalf("exit frame %v: %+v %v", exit, res, err)
		}
		st.finish(context.Background(), nil, "org", "run-1", false, nil)
		_, err = os.Stat(filepath.Join(host.ArtifactRoot, "chart.csv"))
		if exit && err != nil {
			t.Error("the artifact did not come back after serve's exit frame")
		}
		if !exit && err == nil {
			t.Error("the artifact came back with no exit frame from serve, while the runner may still run")
		}
	}
}
