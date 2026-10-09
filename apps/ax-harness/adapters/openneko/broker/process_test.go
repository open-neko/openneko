package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
)

func TestProcessRunSendsRuntimeIdentityAndRequiresFileReceipt(t *testing.T) {
	response := `{"ok":true,"files":[{"path":"runs/r/artifacts/process-2/result.csv","sha256":"` + strings.Repeat("a", 64) + `"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Binding     string `json:"binding"`
		}
		if r.URL.Path != "/v1/harness/process/run" || r.Header.Get("Authorization") != "Bearer synthetic" ||
			json.NewDecoder(r.Body).Decode(&body) != nil || body.OperationID != 2 ||
			body.Instruction != `{"language":"python","script":"print(1)","outputs":["result.csv"]}` ||
			body.Binding != strings.Repeat("b", 64) {
			t.Error("incorrect isolated process request")
		}
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	run, err := ProcessRun(server.URL, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithOperationID(context.Background(), 2)
	input := json.RawMessage(`{"language":"python","script":"print(1)","outputs":["result.csv"]}`)
	if raw, err := run(ctx, input, strings.Repeat("b", 64)); err != nil || string(raw) != response {
		t.Fatalf("receipt=%s err=%v", raw, err)
	}
	response = `{"error":"outcome_unknown"}`
	if _, err := run(ctx, input, strings.Repeat("b", 64)); err == nil {
		t.Fatal("accepted an unconfirmed process result")
	}
	if _, err := run(context.Background(), input, strings.Repeat("b", 64)); err == nil {
		t.Fatal("accepted a missing operation ID")
	}
}
