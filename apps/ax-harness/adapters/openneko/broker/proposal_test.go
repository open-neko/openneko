package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
)

func TestProposalTransportBindsOperationAndRejectsExecution(t *testing.T) {
	status := "pending_approval"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.OperationID != 2 || r.URL.Path != "/v1/harness/propose" || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("incorrect bound request")
		}
		p, err := agent.ParseProposal([]byte(body.Instruction))
		if err != nil || p.Action != "fixture" {
			t.Error("invalid proposal input")
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "request-1", "status": status})
	}))
	defer server.Close()
	propose, err := Propose(server.URL, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithOperationID(context.Background(), 2)
	input := agent.Proposal{Action: "fixture", Arguments: json.RawMessage(`{"value":42}`), Summary: "Change fixture"}
	receipt, err := propose(ctx, input)
	if err != nil || receipt.ID != "request-1" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	status = "executed"
	if _, err = propose(ctx, input); err == nil {
		t.Fatal("accepted execution receipt")
	}
	if _, err = propose(context.Background(), input); err == nil {
		t.Fatal("accepted unbound operation")
	}
}
