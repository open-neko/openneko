package batch

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
	adapter "github.com/open-neko/openneko/apps/ax-harness/adapters/mcp"
	product "github.com/open-neko/openneko/apps/ax-harness/adapters/openneko/mcp"
	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
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
	var recordsRequests atomic.Int32
	var workflowRequests atomic.Int32
	var eventPosts atomic.Int32
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected route", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized) // Bridge warmup.
			return
		}
		if r.URL.Path == "/v1/events" {
			var posted struct {
				Events []map[string]any `json:"events"`
			}
			if json.NewDecoder(r.Body).Decode(&posted) != nil || len(posted.Events) != 1 || (posted.Events[0]["type"] != "surface" && posted.Events[0]["type"] != "needs_input") {
				http.Error(w, "invalid interaction event", http.StatusBadRequest)
				return
			}
			eventPosts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["orgId"] != "org-fixture" || (r.URL.Path != "/v1/records/blueprints" && body["runId"] != "run-fixture") || (r.URL.Path == "/v1/memory/search" && body["query"] != "find policy") || (r.URL.Path == "/v1/library/search" && body["query"] != "find contract") {
			http.Error(w, "wrong scope or query", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/v1/library/search" {
			libraryRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"concept":{"path":"contracts/example","type":"contract","title":"Fixture contract","description":"Fixture","status":"stable","sources":[],"body":"TERMS-42"},"layer":"team","score":1}]`))
			return
		}
		if r.URL.Path == "/v1/workflow/list" {
			workflowRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"total":1,"workflows":[{"id":"workflow-1","name":"Fixture workflow","steps":[]}]}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/records/") {
			recordsRequests.Add(1)
			if body["appId"] != "crm" && r.URL.Path != "/v1/records/catalog" && r.URL.Path != "/v1/records/blueprints" {
				http.Error(w, "wrong records app", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/records/catalog":
				_, _ = w.Write([]byte(`{"apps":[{"appId":"crm","objects":[{"apiName":"lead"}]}]}`))
			case "/v1/records/find":
				_, _ = w.Write([]byte(`{"records":[{"id":"lead-42"}]}`))
			case "/v1/records/get":
				_, _ = w.Write([]byte(`{"id":"lead-42","name":"Fixture lead"}`))
			case "/v1/records/blueprints":
				_, _ = w.Write([]byte(`{"blueprints":[{"id":"crm"}]}`))
			case "/v1/records/recycle/find":
				_, _ = w.Write([]byte(`{"records":[{"id":"lead-deleted"}]}`))
			case "/v1/records/recycle/get":
				_, _ = w.Write([]byte(`{"id":"lead-deleted","deletedAt":"today"}`))
			default:
				http.Error(w, "unexpected records route", http.StatusNotFound)
			}
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
		OrgID: "org-fixture", ThreadID: "thread-fixture", RunID: "run-fixture", SkillsRoot: t.TempDir(), Interaction: true, Cards: true, Workflow: true,
	}, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeProduct()
	result, err = productCaps[0].Call(ctx, json.RawMessage(`{"query":"find policy"}`))
	if err != nil || !strings.Contains(string(result), "memory-1") || requests.Load() != 2 {
		t.Fatalf("product MCP read failed: result=%s err=%v requests=%d", result, err, requests.Load())
	}
	if len(productCaps) != 11 || productCaps[1].Name != "mcp_library_search" {
		t.Fatalf("library read was not admitted: %+v", productCaps)
	}
	result, err = productCaps[1].Call(ctx, json.RawMessage(`{"query":"find contract"}`))
	if err != nil || !strings.Contains(string(result), "TERMS-42") || libraryRequests.Load() != 1 {
		t.Fatalf("product library read failed: result=%s err=%v requests=%d", result, err, libraryRequests.Load())
	}
	for _, call := range []struct {
		index           int
		input, expected string
	}{
		{2, `{"app":"crm"}`, `crm`},
		{3, `{"blueprint":"crm"}`, `crm`},
		{4, `{"app":"crm","object":"lead","first":5}`, `lead-42`},
		{5, `{"app":"crm","object":"lead","id":"lead-42"}`, `Fixture lead`},
		{6, `{"app":"crm","object":"lead"}`, `lead-deleted`},
		{7, `{"app":"crm","object":"lead","id":"lead-deleted"}`, `deletedAt`},
	} {
		result, err := productCaps[call.index].Call(ctx, json.RawMessage(call.input))
		if err != nil || !strings.Contains(string(result), call.expected) {
			t.Fatalf("records read %d failed: %s %v", call.index, result, err)
		}
	}
	if recordsRequests.Load() != 6 {
		t.Fatalf("records route count %d", recordsRequests.Load())
	}
	result, err = productCaps[8].Call(ctx, json.RawMessage(`{"limit":5}`))
	if err != nil || !strings.Contains(string(result), "Fixture workflow") || workflowRequests.Load() != 1 {
		t.Fatalf("workflow list did not reach the host: result=%s err=%v requests=%d", result, err, workflowRequests.Load())
	}
	result, err = productCaps[9].Call(ctx, json.RawMessage(`{"questions":[{"question":"Which day?"}]}`))
	if err != nil || !strings.Contains(string(result), "needs_input") || eventPosts.Load() != 2 {
		t.Fatalf("clarification did not reach the host: result=%s err=%v events=%d", result, err, eventPosts.Load())
	}
	result, err = productCaps[10].Call(ctx, json.RawMessage(`{"messages":[{"version":"v1.0","createSurface":{"surfaceId":"answer","catalogId":"urn:openneko:catalog:work:v2","components":[{"id":"root","component":"Text"}]}}]}`))
	if err != nil || !strings.Contains(string(result), "accepted") || eventPosts.Load() != 3 {
		t.Fatalf("card did not reach the host: result=%s err=%v events=%d", result, err, eventPosts.Load())
	}
	recordsOnly, closeRecords, err := product.ConnectReads(ctx, product.ReadConfig{
		BridgePath: bridge, BrokerURL: broker.URL, BrokerToken: token,
		OrgID: "org-fixture", ThreadID: "thread-fixture", RunID: "run-fixture", SkillsRoot: t.TempDir(),
	}, false, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRecords()
	if len(recordsOnly) != 6 || recordsOnly[0].Name != "mcp_neko_records_browse_catalog" {
		t.Fatalf("records-only catalog invalid: %+v", recordsOnly)
	}
	bound := agent.Tools{Capabilities: recordsOnly}
	if _, err := bound.Binding("mcp_neko_records_find_records"); err != nil {
		t.Fatalf("records schema not admitted by engine: %v", err)
	}
	if _, err := bound.Binding("mcp_neko_records_browse_blueprints"); err != nil {
		t.Fatalf("records blueprint tool missing: %v", err)
	}
}

func TestOpenNekoStdioBridgeDeathFailsCall(t *testing.T) {
	source := os.Getenv("OPENNEKO_TEST_SOURCE")
	if source == "" {
		t.Skip("set OPENNEKO_TEST_SOURCE to the isolated OpenNeko checkout")
	}
	bridge := filepath.Join(source, "apps/worker/src/agent-sandbox/mcp-bridge.ts")
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer broker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.Command("node", "--import", "tsx", bridge, "neko_memory")
	cmd.Dir = filepath.Join(source, "apps/worker")
	cmd.Env = append(os.Environ(),
		"OPENNEKO_BROKER_URL="+broker.URL,
		"OPENNEKO_BROKER_TOKEN=fixture-broker-token",
		"OPENNEKO_MCP_MEMORY_READ_ONLY=1",
		"OPENNEKO_MCP_MODE=work",
		"OPENNEKO_MCP_ORG_ID=org-fixture",
		"OPENNEKO_MCP_THREAD_ID=thread-fixture",
		"OPENNEKO_MCP_RUN_ID=run-fixture",
		"OPENNEKO_MCP_SKILLS_ROOT="+t.TempDir(),
	)
	client := protocol.NewClient(&protocol.Implementation{Name: "harness-m5b-death", Version: "1"}, nil)
	session, err := client.Connect(ctx, &protocol.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 1 {
		t.Fatalf("bridge discovery failed: %v %+v", err, listed)
	}
	schema, err := json.Marshal(listed.Tools[0].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := adapter.Admit(ctx, session, []adapter.Admission{{
		Name: "memory_search", Alias: "mcp_memory_search", Version: "1", Origin: "openneko",
		Effect: "read", Description: "Search run-scoped memory.", Schema: schema,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	callCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if _, err := caps[0].Call(callCtx, json.RawMessage(`{"query":"find policy"}`)); err == nil {
		t.Fatal("dead bridge returned a successful tool result")
	}
}

func TestOpenNekoStalledBrokerCallEndsOnBridgeTeardown(t *testing.T) {
	source := os.Getenv("OPENNEKO_TEST_SOURCE")
	if source == "" {
		t.Skip("set OPENNEKO_TEST_SOURCE to the isolated OpenNeko checkout")
	}
	bridge := filepath.Join(source, "apps/worker/src/agent-sandbox/mcp-bridge.ts")
	started := make(chan struct{})
	stopped := make(chan struct{})
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["query"] == "stall policy" {
			close(started)
			<-r.Context().Done()
			close(stopped)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer broker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.Command("node", "--import", "tsx", bridge, "neko_memory")
	cmd.Dir = filepath.Join(source, "apps/worker")
	cmd.Env = append(os.Environ(),
		"OPENNEKO_BROKER_URL="+broker.URL,
		"OPENNEKO_BROKER_TOKEN=fixture-broker-token",
		"OPENNEKO_MCP_MEMORY_READ_ONLY=1",
		"OPENNEKO_MCP_MODE=work",
		"OPENNEKO_MCP_ORG_ID=org-fixture",
		"OPENNEKO_MCP_THREAD_ID=thread-fixture",
		"OPENNEKO_MCP_RUN_ID=run-fixture",
		"OPENNEKO_MCP_SKILLS_ROOT="+t.TempDir(),
	)
	session, err := protocol.NewClient(&protocol.Implementation{Name: "harness-m5b-stall", Version: "1"}, nil).
		Connect(ctx, &protocol.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 1 {
		t.Fatalf("bridge discovery failed: %v %+v", err, listed)
	}
	schema, err := json.Marshal(listed.Tools[0].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := adapter.Admit(ctx, session, []adapter.Admission{{
		Name: "memory_search", Alias: "mcp_memory_search", Version: "1", Origin: "openneko",
		Effect: "read", Description: "Search run-scoped memory.", Schema: schema,
	}})
	if err != nil {
		t.Fatal(err)
	}
	callCtx, stop := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer stop()
	callDone := make(chan error, 1)
	go func() {
		_, callErr := caps[0].Call(callCtx, json.RawMessage(`{"query":"stall policy"}`))
		callDone <- callErr
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled broker request never started")
	}
	select {
	case callErr := <-callDone:
		if callErr == nil {
			t.Fatal("cancelled MCP call succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MCP call did not observe its deadline")
	}
	closeErr := session.Close()
	if cmd.ProcessState == nil {
		t.Fatalf("bridge teardown left child process running: %v", closeErr)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge teardown left the broker request open")
	}
}
