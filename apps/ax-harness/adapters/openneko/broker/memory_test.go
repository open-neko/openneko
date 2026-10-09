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

func TestMemorySaveSendsRuntimeIdentityAndRequiresReceipt(t *testing.T) {
	response := `{"ok":true,"memoryId":"memory-1"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Binding     string `json:"binding"`
		}
		if r.URL.Path != "/v1/harness/memory/save" || r.Header.Get("Authorization") != "Bearer synthetic" ||
			json.NewDecoder(r.Body).Decode(&body) != nil || body.OperationID != 2 || body.Instruction != `{"text":"Keep this rule"}` || body.Binding != strings.Repeat("a", 64) {
			t.Error("incorrect memory save request")
		}
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	save, err := MemorySave(server.URL, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithOperationID(context.Background(), 2)
	if raw, err := save(ctx, json.RawMessage(`{"text":"Keep this rule"}`), strings.Repeat("a", 64)); err != nil || string(raw) != response {
		t.Fatalf("receipt=%s err=%v", raw, err)
	}
	response = `{"error":"outcome_unknown"}`
	if _, err := save(ctx, json.RawMessage(`{"text":"Keep this rule"}`), strings.Repeat("a", 64)); err == nil {
		t.Fatal("accepted an unconfirmed effect")
	}
	if _, err := save(context.Background(), json.RawMessage(`{"text":"Keep this rule"}`), strings.Repeat("a", 64)); err == nil {
		t.Fatal("accepted a missing runtime operation ID")
	}
}
