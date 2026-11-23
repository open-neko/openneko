package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/inference"
)

func TestKeyDoorCountMismatchGoesInTheAuditChain(t *testing.T) {
	f, db, g := hostFront(t)
	clearAxEnv(t, nil)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	finished := func(runID string, requests int) []json.RawMessage {
		frame, _ := json.Marshal(map[string]any{"type": "event", "event": map[string]any{"version": 1, "run_id": runID, "sequence": 1,
			"type": "run.finished", "result": map[string]any{"status": "completed", "kind": "answer", "answer": "ok",
				"usage": map[string]any{"requests": requests}}}})
		return []json.RawMessage{frame}
	}
	srv := newScriptedServe(t, [][]json.RawMessage{finished("honest", 0), finished("liar", 7)})
	cfg := &llm.AxBackendConfig{Route: llm.AxModelRoute{Provider: "openai-compatible", Model: "m", URL: "https://compat.example/v1"}}
	for _, tag := range []string{"honest", "liar"} {
		res, err := runAx(context.Background(), cfg, axTurnOptions{OrgID: "org", Tag: tag, Socket: srv.socket, Door: f.API.newKeyDoor(),
			Keys: map[string]string{"MODEL_API_KEY": "k"}})
		if err != nil || res.Status != "completed" {
			t.Fatalf("%s turn: %+v %v", tag, res, err)
		}
	}

	g.usage = inference.Usage{Requests: 3}
	calls := f.API.hostCalls()
	calls.Door = f.API.newKeyDoor()
	if err := calls.TestPrimary(context.Background(), "org", llm.Settings{Provider: "openai", Model: "m", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(context.Background(), `select concat_ws(' ', payload->>'purpose', payload->>'doorCalls', payload->>'reportedCalls') from audit_chain
		where org_id = 'org' and entity_kind = 'key_door' and event = 'agent:model_calls_mismatch' order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		got = append(got, payload)
	}
	want := []string{
		"turn liar 0 7",
		"host call 0 3",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("audit chain:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, line := range []string{
		`[key-door] org "org" turn liar: the door forwarded 0 model calls, serve reported 7`,
		`[key-door] org "org" host call: the door forwarded 0 model calls, serve reported 3`,
	} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("no log line %q in:\n%s", line, logs.String())
		}
	}
	if strings.Contains(logs.String(), "turn honest") {
		t.Errorf("a matching count was logged:\n%s", logs.String())
	}
}
