package webfront

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// TestConfinedRunnerTurn runs a whole turn in the release binary's agent
// runner under its Seatbelt profile: a fake model behind the key door, which
// the runner reaches only through the descriptor that serve's broker passes,
// a tool call and a file in the turn's workspace.
func TestConfinedRunnerTurn(t *testing.T) {
	st := confine.RunnerAvailable()
	if !st.Confined {
		if os.Getenv("CI") != "" {
			t.Fatalf("this runner cannot prove the confinement: %s", st.Reason)
		}
		t.Skip(st.Reason)
	}
	bin := releaseBinary(t)
	clearAxEnv(t, nil)
	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", filepath.Join(base, "agent-runs"))

	fake := &fakeProviders{replies: []string{
		`{"javascriptCode":"final('Find reference', {})"}`,
		`{"javascriptCode":"const found = await mcp_neko_memory_search({query:'reference'}); final('Report', {reference:'REF-42', found});"}`,
		"Answer: REF-42",
	}}
	model := httptest.NewServer(fake)
	t.Cleanup(model.Close)
	target, _ := url.Parse(model.URL)

	socket := filepath.Join(testsock.Dir(t), "serve.sock")
	ln, err := serve.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_SERVE_SOCKET", socket)
	doorPath := keydoor.SocketPath(func(string) string { return "" }, socket)
	t.Setenv(keydoor.SocketEnv, doorPath)
	keydoor.SetSocket(doorPath)
	t.Cleanup(func() { keydoor.SetSocket("") })
	ctx, cancel := context.WithCancel(context.Background())
	door := &llm.KeyDoor{Transport: toFake{target}}
	if _, err := llm.ListenDoor(ctx, doorPath, door); err != nil {
		t.Fatal(err)
	}
	srv := &serve.Server{Getenv: os.Getenv, MaxTurns: 1, RequireDoor: true, ConfineRunner: true,
		Runner: []string{bin, "agent-runner"}, RunnerEnv: []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + t.TempDir()}}
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	hostWS, err := turnWorkspace("org-door", "thread-door", "run-door")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageTurn(hostWS, stageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(stage.Dir) })
	tools := &fakeTools{reply: []any{}}
	p := doorProviders[0]
	a := &API{Door: door, doorPath: doorPath}
	res, err := a.runAgentCore(context.Background(), agentCoreInput{
		Backend: &llm.AxBackendConfig{Route: p.route("model-a")}, Prompt: "Find reference",
		OrgID: "org-door", ThreadID: "thread-door", RunID: "run-door",
		Workspace: stage.Workspace, Keys: map[string]string{"MODEL_API_KEY": "planted-" + p.name}, Socket: socket,
		NativeDelegationOff: true,
		FilesEnabled:        func() bool { return false },
		toolBackend:         func(turnBinding) toolBackend { return tools },
		withoutScriptData:   true,
		Emit:                func(context.Context, any) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" || !strings.Contains(res.FinalText, "REF-42") {
		t.Fatalf("result %+v", res)
	}
	checkDoorCalls(t, p, "model-a", fake.all(), "planted-"+p.name)
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if len(tools.calls) == 0 {
		t.Fatal("the turn called no tool")
	}
}
