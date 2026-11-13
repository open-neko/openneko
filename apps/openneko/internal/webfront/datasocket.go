package webfront

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed dataclient/neko_data.py
var nekoDataPy string

//go:embed dataclient/neko-query
var nekoQueryCLI string

// dataSocketEnabled matches startRunDataSocket() in apps/worker/src/agent-sandbox/entry.ts.
func dataSocketEnabled(kind string, graphjinRead bool) bool {
	return kind == "workflow" || kind == "work" || kind == "" || (kind == "agent-job" && graphjinRead)
}

// dataSocket serves the run's neko_graphjin tools to skill scripts on a Unix
// socket, under the run's identity, policy, audit and trace. No token leaves
// this process.
type dataSocket struct {
	path   string
	server *http.Server
	done   chan struct{}
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

func startDataSocket(dir string, b turnBinding, be toolBackend) (*dataSocket, error) {
	if b.Kind != "work" && b.Kind != "" {
		b.GraphjinToolPolicy = ""
	}
	if _, err := parseGraphjinToolPolicy(b.GraphjinToolPolicy); err != nil {
		return nil, err
	}
	if dir == "" {
		dir = os.TempDir()
	}
	id := nonAlnum.ReplaceAllString(b.RunID, "")
	if len(id) > 12 {
		id = id[:12]
	}
	path := filepath.Join(dir, "neko-data-"+id+".sock")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	tools := graphjinTools(b, be).dynamic
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/mcp" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		serveDataMCP(w, r, tools)
	})
	s := &dataSocket{path: path, server: &http.Server{Handler: mux}, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		_ = s.server.Serve(ln)
	}()
	return s, nil
}

func (s *dataSocket) close() error {
	err := s.server.Close()
	<-s.done
	if rmErr := os.Remove(s.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) && err == nil {
		err = rmErr
	}
	return err
}

func dataMCPError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "error": rpcError{code, message}, "id": nil})
}

// serveDataMCP answers as the MCP SDK's stateless Streamable HTTP transport
// does with JSON responses, for one neko_graphjin server.
func serveDataMCP(w http.ResponseWriter, r *http.Request, tools *dynamicTools) {
	accept := r.Header.Get("accept")
	if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
		dataMCPError(w, http.StatusNotAcceptable, -32000, "Not Acceptable: Client must accept both application/json and text/event-stream")
		return
	}
	if !strings.Contains(r.Header.Get("content-type"), "application/json") {
		dataMCPError(w, http.StatusUnsupportedMediaType, -32000, "Unsupported Media Type: Content-Type must be application/json")
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil || !json.Valid(raw) {
		dataMCPError(w, http.StatusBadRequest, -32700, "Parse error: Invalid JSON")
		return
	}
	var batch []json.RawMessage
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' {
		_ = json.Unmarshal(raw, &batch)
	} else {
		batch = []json.RawMessage{raw}
	}
	type message struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  *string         `json:"method"`
		Params  json.RawMessage `json:"params"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	var requests []message
	for _, item := range batch {
		var m message
		if json.Unmarshal(item, &m) != nil || m.JSONRPC != "2.0" || (m.Method == nil && m.Result == nil && m.Error == nil) {
			dataMCPError(w, http.StatusBadRequest, -32700, "Parse error: Invalid JSON-RPC message")
			return
		}
		if m.Method != nil && len(m.ID) > 0 && string(m.ID) != "null" {
			requests = append(requests, m)
		}
	}
	if v := r.Header.Get("mcp-protocol-version"); v != "" && !containsString(mcpProtocolVersions, v) {
		dataMCPError(w, http.StatusBadRequest, -32000, "Bad Request: Unsupported protocol version: "+v+
			" (supported versions: "+strings.Join(mcpProtocolVersions, ", ")+")")
		return
	}
	if len(requests) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	replies := make([]map[string]any, 0, len(requests))
	for _, m := range requests {
		result, rpcErr := handleDataMCP(r.Context(), tools, *m.Method, m.Params)
		reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
		if rpcErr != nil {
			reply["error"] = rpcErr
		} else {
			reply["result"] = result
		}
		replies = append(replies, reply)
	}
	w.Header().Set("Content-Type", "application/json")
	if len(replies) == 1 {
		_ = json.NewEncoder(w).Encode(replies[0])
		return
	}
	_ = json.NewEncoder(w).Encode(replies)
}

func handleDataMCP(ctx context.Context, tools *dynamicTools, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(params, &p)
		version := mcpProtocolVersions[0]
		if containsString(mcpProtocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": tools.server, "version": "1.0.0"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list, err := tools.list(ctx)
		if err == nil {
			list, err = normalizeListedTools(list)
		}
		if err != nil {
			return nil, &rpcError{-32603, err.Error()}
		}
		return json.RawMessage(`{"tools":` + jsStringify(list, false) + `}`), nil
	case "tools/call":
		var p struct {
			Name      *string         `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(params, &p) != nil || p.Name == nil {
			return nil, &rpcError{-32602, "Invalid params"}
		}
		var args *jsObj
		if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
			parsed, err := jsParse(p.Arguments)
			obj, isObj := parsed.(*jsObj)
			if err != nil || !isObj {
				return nil, &rpcError{-32602, "Invalid params"}
			}
			args = obj
		}
		result, err := tools.call(ctx, *p.Name, args)
		if err == nil {
			result, err = normalizeCallResult(result)
		}
		if err != nil {
			return nil, &rpcError{-32603, err.Error()}
		}
		return json.RawMessage(jsStringify(result, false)), nil
	}
	return nil, &rpcError{-32601, "Method not found"}
}

// installDataClient writes neko_data.py, bound to the run's socket, and
// neko-query into binRoot, as installDataClient() in
// apps/worker/src/agent-sandbox/data-client.ts does.
func installDataClient(binRoot, socketPath string) error {
	if err := os.MkdirAll(binRoot, 0o777); err != nil {
		return err
	}
	client := strings.Replace(nekoDataPy, "__NEKO_DATA_SOCKET__", jsStringify(socketPath, false), 1)
	if err := os.WriteFile(filepath.Join(binRoot, "neko_data.py"), []byte(client), 0o666); err != nil {
		return err
	}
	cli := filepath.Join(binRoot, "neko-query")
	if err := os.WriteFile(cli, []byte(nekoQueryCLI), 0o666); err != nil {
		return err
	}
	return os.Chmod(cli, 0o755)
}
