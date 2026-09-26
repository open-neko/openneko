package m5b

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	adapter "github.com/open-neko/harness/adapters/mcp"
	product "github.com/open-neko/harness/adapters/openneko/mcp"
)

// Runs the actual OpenNeko stdio multiplexer through the Go MCP SDK. The
// broker is synthetic here; connected broker authorization is a separate gate.
func TestOpenNekoReadOnlyStdioBridge(t *testing.T) {
	source := os.Getenv("OPENNEKO_TEST_SOURCE")
	if source == "" {
		t.Skip("set OPENNEKO_TEST_SOURCE to the isolated OpenNeko checkout")
	}
	bridge := filepath.Join(source, "apps/worker/src/agent-sandbox/mcp-bridge.ts")
	if _, err := os.Stat(bridge); err != nil {
		t.Fatal(err)
	}
	const token = "fixture-broker-token"
	var requests atomic.Int32
	var libraryRequests atomic.Int32
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path != "/v1/memory/search" && r.URL.Path != "/v1/library/search") || r.Method != http.MethodPost {
			http.Error(w, "unexpected route", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized) // Bridge warmup.
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["orgId"] != "org-fixture" || body["runId"] != "run-fixture" || (r.URL.Path == "/v1/memory/search" && body["query"] != "find policy") || (r.URL.Path == "/v1/library/search" && body["query"] != "find contract") {
			http.Error(w, "wrong scope or query", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/v1/library/search" {
			libraryRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"concept":{"path":"contracts/example","type":"contract","title":"Fixture contract","description":"Fixture","status":"stable","sources":[],"body":"TERMS-42"},"layer":"team","score":1}]`))
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"memory-1","text":"Policy"}]`))
	}))
	defer broker.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.Command("node", "--import", "tsx", bridge, "neko_memory")
	cmd.Dir = filepath.Join(source, "apps/worker")
	cmd.Env = append(os.Environ(),
		"OPENNEKO_BROKER_URL="+broker.URL,
		"OPENNEKO_BROKER_TOKEN="+token,
		"OPENNEKO_MCP_MEMORY_READ_ONLY=1",
		"OPENNEKO_MCP_MODE=work",
		"OPENNEKO_MCP_ORG_ID=org-fixture",
		"OPENNEKO_MCP_THREAD_ID=thread-fixture",
		"OPENNEKO_MCP_RUN_ID=run-fixture",
		"OPENNEKO_MCP_SKILLS_ROOT="+t.TempDir(),
	)
	client := protocol.NewClient(&protocol.Implementation{Name: "harness-m5b-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &protocol.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "memory_search" {
		t.Fatalf("read-only bridge exposed unexpected tools: %+v", listed.Tools)
	}
	schema, err := json.Marshal(listed.Tools[0].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := adapter.Admit(ctx, session, []adapter.Admission{{
		Name: "memory_search", Alias: "mcp_memory_search", Version: "1", Origin: "openneko",
		Effect: "read", Description: "Search run-scoped memory.", Schema: schema,
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := capabilities[0].Call(ctx, json.RawMessage(`{"query":"find policy"}`))
	if err != nil || !strings.Contains(string(result), "memory-1") || requests.Load() != 1 {
		t.Fatalf("stdio MCP read failed: result=%s err=%v requests=%d", result, err, requests.Load())
	}
	if err := session.Close(); err != nil || cmd.ProcessState == nil {
		t.Fatalf("stdio bridge did not exit on close: err=%v state=%v", err, cmd.ProcessState)
	}
	productCaps, closeProduct, err := product.ConnectReads(ctx, product.ReadConfig{
		BridgePath: bridge, BrokerURL: broker.URL, BrokerToken: token,
		OrgID: "org-fixture", ThreadID: "thread-fixture", RunID: "run-fixture", SkillsRoot: t.TempDir(),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeProduct()
	result, err = productCaps[0].Call(ctx, json.RawMessage(`{"query":"find policy"}`))
	if err != nil || !strings.Contains(string(result), "memory-1") || requests.Load() != 2 {
		t.Fatalf("product MCP read failed: result=%s err=%v requests=%d", result, err, requests.Load())
	}
	if len(productCaps) != 2 || productCaps[1].Name != "mcp_library_search" {
		t.Fatalf("library read was not admitted: %+v", productCaps)
	}
	result, err = productCaps[1].Call(ctx, json.RawMessage(`{"query":"find contract"}`))
	if err != nil || !strings.Contains(string(result), "TERMS-42") || libraryRequests.Load() != 1 {
		t.Fatalf("product library read failed: result=%s err=%v requests=%d", result, err, libraryRequests.Load())
	}
}
