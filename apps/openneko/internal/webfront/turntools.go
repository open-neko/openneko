package webfront

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"regexp"
	"strings"
	"sync"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

// The turn's tools are the multiplexed MCP server of
// apps/worker/src/agent-sandbox/mcp-bridge.ts: one server whose tools are
// <logical server without "neko_">_<tool>. The harness reaches it over the
// serve socket's bridge stream, one JSON-RPC message per line.

type toolProp struct {
	name     string
	typ      string
	min, max *float64
	enum     []string
	uuid     bool
	required bool
}

type toolResult struct {
	text    string
	isError bool
}

type toolDef struct {
	server, name, description string
	props                     []toolProp
	shape                     *zObj
	call                      func(ctx context.Context, args *jsObj) (toolResult, error)
}

func (d toolDef) public() string { return strings.TrimPrefix(d.server, "neko_") + "_" + d.name }

func num(v float64) *float64 { return &v }

func strProp(name string, min, max float64, required bool) toolProp {
	return toolProp{name: name, typ: "string", min: num(min), max: num(max), required: required}
}

func intProp(name string, min, max float64, required bool) toolProp {
	return toolProp{name: name, typ: "integer", min: num(min), max: num(max), required: required}
}

const uuidPatternText = `^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}|00000000-0000-0000-0000-000000000000|ffffffff-ffff-ffff-ffff-ffffffffffff)$`

var zodUUID = regexp.MustCompile(uuidPatternText)

// schema is the JSON Schema that the MCP SDK gives for the zod shape.
func (d toolDef) schema() *jsObj {
	if d.shape != nil {
		out := d.shape.jsonSchema()
		out.set("$schema", "http://json-schema.org/draft-07/schema#")
		if len(d.shape.defs) > 0 {
			defs := &jsObj{}
			for _, l := range d.shape.defs {
				defs.set(l.def, l.inner.jsonSchema())
			}
			out.set("definitions", defs)
		}
		return out
	}
	props := &jsObj{}
	required := []any{}
	for _, p := range d.props {
		s := &jsObj{}
		s.set("type", p.typ)
		if p.uuid {
			s.set("format", "uuid")
			s.set("pattern", uuidPatternText)
		}
		if p.min != nil {
			if p.typ == "string" {
				s.set("minLength", *p.min)
			} else {
				s.set("minimum", *p.min)
			}
		}
		if p.max != nil {
			if p.typ == "string" {
				s.set("maxLength", *p.max)
			} else {
				s.set("maximum", *p.max)
			}
		}
		if p.enum != nil {
			values := make([]any, len(p.enum))
			for i, v := range p.enum {
				values[i] = v
			}
			s.set("enum", values)
		}
		props.set(p.name, s)
		if p.required {
			required = append(required, p.name)
		}
	}
	out := &jsObj{}
	out.set("type", "object")
	out.set("properties", props)
	out.set("required", required)
	out.set("$schema", "http://json-schema.org/draft-07/schema#")
	return out
}

func (d toolDef) listing() *jsObj {
	out := &jsObj{}
	out.set("name", d.public())
	out.set("description", d.description)
	out.set("inputSchema", d.schema())
	execution := &jsObj{}
	execution.set("taskSupport", "forbidden")
	out.set("execution", execution)
	return out
}

func jsTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	}
	return "object"
}

func zodIssue(fields ...any) *jsObj {
	issue := &jsObj{}
	for i := 0; i+1 < len(fields); i += 2 {
		issue.set(fields[i].(string), fields[i+1])
	}
	return issue
}

// validate checks args as the zod shape does and returns the issues in
// zod's order and words. Keys the shape does not name are dropped.
func (d toolDef) validate(args *jsObj) (*jsObj, []any) {
	issues := []any{}
	if d.shape != nil {
		clean := d.shape.parse(args, []any{}, &issues)
		return clean.(*jsObj), issues
	}
	clean := &jsObj{}
	for _, p := range d.props {
		v, present := args.vals[p.name]
		path := []any{p.name}
		if !present {
			if p.required {
				issues = append(issues, zodIssue("expected", zodExpected(p.typ), "code", "invalid_type",
					"path", path, "message", "Invalid input: expected "+zodExpected(p.typ)+", received undefined"))
			}
			continue
		}
		before := len(issues)
		switch p.typ {
		case "string":
			s, ok := v.(string)
			if p.enum != nil {
				if !ok || !containsString(p.enum, s) {
					values := make([]any, len(p.enum))
					quoted := make([]string, len(p.enum))
					for i, e := range p.enum {
						values[i] = e
						quoted[i] = jsStringify(e, false)
					}
					issues = append(issues, zodIssue("code", "invalid_value", "values", values, "path", path,
						"message", "Invalid option: expected one of "+strings.Join(quoted, "|")))
				}
				break
			}
			if !ok {
				issues = append(issues, zodIssue("expected", "string", "code", "invalid_type", "path", path,
					"message", "Invalid input: expected string, received "+jsTypeName(v)))
			}
			zLengthIssues(v, p.min, p.max, path, &issues)
			if !ok {
				break
			}
			if p.uuid && !zodUUID.MatchString(s) {
				issues = append(issues, zodIssue("origin", "string", "code", "invalid_format", "format", "uuid",
					"pattern", "/"+uuidPatternText+"/", "path", path, "message", "Invalid UUID"))
			}
		case "integer":
			f, ok := v.(float64)
			if !ok {
				issues = append(issues, zodIssue("expected", "number", "code", "invalid_type", "path", path,
					"message", "Invalid input: expected number, received "+jsTypeName(v)))
				break
			}
			if f != math.Trunc(f) || math.Abs(f) > maxSafeInt {
				issues = append(issues, zodIssue("expected", "int", "format", "safeint", "code", "invalid_type", "path", path,
					"message", "Invalid input: expected int, received number"))
				break
			}
			if p.min != nil && f < *p.min {
				issues = append(issues, zodIssue("origin", "number", "code", "too_small", "minimum", *p.min, "inclusive", true, "path", path,
					"message", "Too small: expected number to be >="+jsNumberText(*p.min)))
			}
			if p.max != nil && f > *p.max {
				issues = append(issues, zodIssue("origin", "number", "code", "too_big", "maximum", *p.max, "inclusive", true, "path", path,
					"message", "Too big: expected number to be <="+jsNumberText(*p.max)))
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				issues = append(issues, zodIssue("expected", "boolean", "code", "invalid_type", "path", path,
					"message", "Invalid input: expected boolean, received "+jsTypeName(v)))
			}
		}
		if len(issues) == before {
			clean.set(p.name, v)
		}
	}
	return clean, issues
}

func zodExpected(typ string) string {
	if typ == "integer" {
		return "number"
	}
	return typ
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// dynamicTools is a logical server whose tools come from another service,
// as createDynamicMcpServer() builds for neko_graphjin.
type dynamicTools struct {
	server string
	list   func(ctx context.Context) ([]any, error)
	call   func(ctx context.Context, name string, args *jsObj) (any, error)
}

type toolGroup struct {
	static  []toolDef
	dynamic *dynamicTools
}

type toolRoute struct {
	static   *toolDef
	dynamic  *dynamicTools
	original string
}

// toolServer is one run's multiplexed tool server. As in Node, a call is
// routed by the last tools/list; a name it does not hold lists again first.
type toolServer struct {
	groups     []toolGroup
	scriptData func(ctx context.Context, raw json.RawMessage) (*jsObj, error)
	mu         sync.Mutex
	routes     map[string]toolRoute
}

func newToolServer(groups ...toolGroup) *toolServer {
	return &toolServer{groups: groups, routes: map[string]toolRoute{}}
}

func (s *toolServer) list(ctx context.Context) ([]any, error) {
	out := []any{}
	routes := map[string]toolRoute{}
	for _, g := range s.groups {
		if g.dynamic == nil {
			for i := range g.static {
				t := &g.static[i]
				routes[t.public()] = toolRoute{static: t}
				out = append(out, t.listing())
			}
			continue
		}
		tools, err := g.dynamic.list(ctx)
		if err == nil {
			tools, err = normalizeListedTools(tools)
		}
		if err != nil {
			log.Printf("[mcp-bridge] %s tools/list failed; its tools are unavailable until the next tools/list: %v", g.dynamic.server, err)
			continue
		}
		for _, tool := range tools {
			obj := tool.(*jsObj)
			original := obj.vals["name"].(string)
			public := strings.TrimPrefix(g.dynamic.server, "neko_") + "_" + original
			if _, dup := routes[public]; dup {
				return nil, errors.New("mcp-bridge: duplicate multiplexed tool " + public)
			}
			routes[public] = toolRoute{dynamic: g.dynamic, original: original}
			obj.set("name", public)
			out = append(out, obj)
		}
	}
	s.mu.Lock()
	s.routes = routes
	s.mu.Unlock()
	return out, nil
}

func (s *toolServer) route(ctx context.Context, name string) (toolRoute, bool, error) {
	s.mu.Lock()
	r, ok := s.routes[name]
	s.mu.Unlock()
	if ok {
		return r, true, nil
	}
	if _, err := s.list(ctx); err != nil {
		return r, false, err
	}
	s.mu.Lock()
	r, ok = s.routes[name]
	s.mu.Unlock()
	return r, ok, nil
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var mcpProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05", "2024-10-07"}

// serve answers JSON-RPC requests from r on w until r ends or ctx is done.
// Calls run at once, each in its own goroutine, as the Node server does.
func (s *toolServer) serve(ctx context.Context, r io.Reader, w io.Writer) error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	defer wg.Wait()
	write := func(msg any) {
		raw, err := json.Marshal(msg)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(append(raw, '\n'))
	}
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(line, &req) == nil && len(req.ID) > 0 && req.Method != "" {
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, rpcErr := s.handle(ctx, req.Method, req.Params)
					reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
					if rpcErr != nil {
						reply["error"] = rpcErr
					} else {
						reply["result"] = result
					}
					write(reply)
				}()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (s *toolServer) handle(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
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
			"serverInfo":      map[string]any{"name": "openneko-mcp-multiplexer", "version": "1.0.0"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list, err := s.list(ctx)
		if err != nil {
			return nil, &rpcError{-32603, err.Error()}
		}
		return json.RawMessage(`{"tools":` + jsStringify(list, false) + `}`), nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{-32602, "Invalid params"}
		}
		if p.Name == sandbox.DataTool && s.scriptData != nil {
			result, err := s.scriptData(ctx, p.Arguments)
			if err != nil {
				return nil, &rpcError{-32603, "MCP error -32603: " + err.Error()}
			}
			return json.RawMessage(jsStringify(result, false)), nil
		}
		route, ok, err := s.route(ctx, p.Name)
		if err != nil {
			return nil, &rpcError{-32603, err.Error()}
		}
		if !ok {
			return nil, &rpcError{-32603, "mcp-bridge: unknown tool " + p.Name}
		}
		if route.static != nil {
			return json.RawMessage(jsStringify(s.call(ctx, *route.static, p.Arguments), false)), nil
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
		result, err := route.dynamic.call(ctx, route.original, args)
		if err == nil {
			result, err = normalizeCallResult(result)
		}
		if err != nil {
			return nil, &rpcError{-32603, "MCP error -32603: " + err.Error()}
		}
		return json.RawMessage(jsStringify(result, false)), nil
	}
	return nil, &rpcError{-32601, "Method not found"}
}

func (s *toolServer) call(ctx context.Context, tool toolDef, raw json.RawMessage) *jsObj {
	args := &jsObj{}
	if len(raw) > 0 && string(raw) != "null" {
		if parsed, err := jsParse(raw); err == nil {
			if obj, ok := parsed.(*jsObj); ok {
				args = obj
			}
		}
	}
	clean, issues := tool.validate(args)
	if len(raw) == 0 {
		issues = []any{zTypeIssue("object", jsUndefined{}, []any{})}
	}
	if len(issues) > 0 {
		return toolResultObj(toolResult{isError: true, text: "MCP error -32602: Input validation error: Invalid arguments for tool " +
			tool.name + ": " + jsStringifyIndent(issues, "  ")})
	}
	result, err := tool.call(ctx, clean)
	if err != nil {
		return toolResultObj(toolResult{isError: true, text: err.Error()})
	}
	return toolResultObj(result)
}

func toolResultObj(r toolResult) *jsObj {
	item := &jsObj{}
	item.set("type", "text")
	item.set("text", r.text)
	out := &jsObj{}
	out.set("content", []any{item})
	if r.isError {
		out.set("isError", true)
	}
	return out
}

func toolText(v any) toolResult { return toolResult{text: jsStringify(v, false)} }

func jsObjOf(pairs ...any) *jsObj {
	out := &jsObj{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out.set(pairs[i].(string), pairs[i+1])
	}
	return out
}

func optionalInt(args *jsObj, name string) *int {
	if f, ok := args.vals[name].(float64); ok {
		n := int(f)
		return &n
	}
	return nil
}

func stringArg(args *jsObj, name string) string {
	s, _ := args.vals[name].(string)
	return s
}
