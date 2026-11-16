package webfront

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func modelRequestServe(t *testing.T) (string, <-chan string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "serve.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	request := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return
		}
		request <- line
		for _, l := range []string{
			`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":1,"type":"model.request.finished","call_id":1,"name":"gpt-4.1","streaming":true,"stage":"root/executor","duration_ms":900,"usage":{"input_tokens":50,"output_tokens":7}}}`,
			`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":2,"type":"model.request.finished","call_id":2,"name":"gpt-4.1","streaming":true,"stage":"root/responder","duration_ms":4200,"usage":{"input_tokens":80,"output_tokens":120,"reasoning_tokens":30}}}`,
			`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":3,"type":"run.finished","result":{"status":"completed","answer":"Done."}}}`,
			`{"type":"exit","code":0}`,
		} {
			if _, err := conn.Write([]byte(l + "\n")); err != nil {
				return
			}
		}
	}()
	return socket, request
}

func evalTraceTurn(t *testing.T, register bool, effort string) (string, string) {
	t.Helper()
	clearAxEnv(t, map[string]string{evalResponderEffortEnv: effort})
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	trace, err := OpenEvalTrace(path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer trace.Close()
	if register {
		defer trace.register("run-1")()
	}
	socket, request := modelRequestServe(t)
	res, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-1", RunID: "run-1", RunKind: "work",
		OrgID: "org-1", Socket: socket, Emit: func(context.Context, any) error { return nil }})
	if err != nil || res.Status != "completed" {
		t.Fatalf("got %+v %v", res, err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return <-request, string(written)
}

func TestEvalTraceRecordsEachModelRequestAndTheResponderEffort(t *testing.T) {
	request, written := evalTraceTurn(t, true, "minimal")
	if !strings.Contains(request, `"responder_reasoning_effort":"minimal"`) {
		t.Fatalf("the spec has no responder effort: %s", request)
	}
	lines := strings.Split(strings.TrimSpace(written), "\n")
	if len(lines) != 2 {
		t.Fatalf("trace lines = %d, want 2:\n%s", len(lines), written)
	}
	for _, want := range []string{
		`{"modelRequest":{"runId":"run-1","runKind":"work","callId":1,"streamed":true,"durationMs":900,"stage":"root/executor","model":"gpt-4.1","responderReasoningEffort":"minimal","usage":{"input_tokens":50,"output_tokens":7}}}`,
		`{"modelRequest":{"runId":"run-1","runKind":"work","callId":2,"streamed":true,"durationMs":4200,"stage":"root/responder","model":"gpt-4.1","responderReasoningEffort":"minimal","usage":{"input_tokens":80,"output_tokens":120,"reasoning_tokens":30}}}`,
	} {
		if !strings.Contains(written, want) {
			t.Fatalf("trace misses %s:\n%s", want, written)
		}
	}
	if strings.Contains(written, `"event"`) {
		t.Fatalf("a model request line reads as a semantic trace event:\n%s", written)
	}
}

func TestResponderEffortNeedsAnEvalTraceAndAKnownLevel(t *testing.T) {
	request, written := evalTraceTurn(t, false, "low")
	if strings.Contains(request, "responder_reasoning_effort") || written != "" {
		t.Fatalf("a run with no eval trace took the eval setting: %s / %q", request, written)
	}
	request, _ = evalTraceTurn(t, true, "none")
	if strings.Contains(request, "responder_reasoning_effort") {
		t.Fatalf("an unknown level reached the spec: %s", request)
	}
}
