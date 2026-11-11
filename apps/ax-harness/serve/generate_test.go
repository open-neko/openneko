package serve

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/inference"
)

func frames(t *testing.T, c *client) []Frame {
	t.Helper()
	var out []Frame
	timeout := time.After(10 * time.Second)
	for {
		select {
		case frame, ok := <-c.frames:
			if !ok {
				return out
			}
			out = append(out, frame)
			if frame.Type == "exit" {
				return out
			}
		case <-timeout:
			t.Fatal("serve did not finish the call")
		}
	}
}

func TestServeRunsAHostModelCallWithTheCallersKey(t *testing.T) {
	var gotKey string
	var gotRequest inference.Request
	socket := startServer(t, &Server{Generate: func(_ context.Context, request inference.Request, key string) (inference.Result, error) {
		gotKey, gotRequest = key, request
		return inference.Result{Outputs: map[string]any{"answer": "42"}, Usage: inference.Usage{Requests: 1, Reported: 1, InputTokens: 3, OutputTokens: 1, TotalTokens: 4, Coverage: "complete"}}, nil
	}})
	request, _ := json.Marshal(inference.Request{Route: inference.Route{Provider: "openai", Model: "m"}, Signature: "question:string -> answer:string", Values: map[string]any{"question": "q"}})
	got := frames(t, dial(t, socket, Frame{Type: "generate", Generate: request, ModelKey: "org-key"}))
	if len(got) != 2 || got[0].Type != "result" || got[1].Type != "exit" || *got[1].Code != 0 {
		t.Fatalf("frames = %+v", got)
	}
	var result inference.Result
	if err := json.Unmarshal(got[0].Result, &result); err != nil || result.Outputs["answer"] != "42" || result.Usage.InputTokens != 3 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if gotKey != "org-key" || gotRequest.Route.Model != "m" || gotRequest.Values["question"] != "q" {
		t.Fatalf("key = %q, request = %+v", gotKey, gotRequest)
	}
}

func TestServeReportsAFailedHostModelCall(t *testing.T) {
	socket := startServer(t, &Server{Generate: func(context.Context, inference.Request, string) (inference.Result, error) {
		return inference.Result{}, errors.New("provider said no")
	}})
	request, _ := json.Marshal(inference.Request{Route: inference.Route{Provider: "openai", Model: "m"}, Signature: "question:string -> answer:string"})
	got := frames(t, dial(t, socket, Frame{Type: "generate", Generate: request}))
	if len(got) != 1 || got[0].Type != "exit" || *got[0].Code != 1 || got[0].Error != "provider said no" {
		t.Fatalf("frames = %+v", got)
	}
	got = frames(t, dial(t, socket, Frame{Type: "generate"}))
	if len(got) != 1 || *got[0].Code != 2 {
		t.Fatalf("a generate frame without a request: %+v", got)
	}
}
