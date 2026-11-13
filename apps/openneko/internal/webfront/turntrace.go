package webfront

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// The semantic trace is packages/llm/src/work/semantic-trace.ts. The eval
// scorers compare its digests with digests that Node computed, so each
// digest must hash the same canonical JSON text.

const (
	traceSchemaVersion = "openneko.work.semantic-trace/v1"
	traceStepRowsMax   = 16 * 1024
	traceBroker        = "trusted-broker"
)

type traceBinding struct {
	RunID, OrgID, Kind string
}

type traceSink func(event, content *jsObj)

type traceRegistration struct {
	sink  traceSink
	scrub func(string) string
	next  int
}

var traceSinks = struct {
	sync.Mutex
	runs map[string]*traceRegistration
}{runs: map[string]*traceRegistration{}}

// registerTraceSink is registerWorkSemanticTraceSink(). With scrub set, each
// event also carries the step content, scrubbed.
func registerTraceSink(runID string, sink traceSink, scrub func(string) string) func() {
	reg := &traceRegistration{sink: sink, scrub: scrub, next: 1}
	traceSinks.Lock()
	traceSinks.runs[runID] = reg
	traceSinks.Unlock()
	return func() {
		traceSinks.Lock()
		if traceSinks.runs[runID] == reg {
			delete(traceSinks.runs, runID)
		}
		traceSinks.Unlock()
	}
}

func traceDigest(v any) string {
	sum := sha256.Sum256([]byte(jsCanonical(v)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type traceSpec struct {
	binding      traceBinding
	operation    string
	source       string
	request      any
	logicalError func(result any) bool
	evidence     func(result any) *jsObj
}

// traced runs execute and gives the run's sink one event, as traced() does.
// A sink that panics never changes the result.
func traced(spec traceSpec, execute func() (any, error)) (any, error) {
	traceSinks.Lock()
	reg := traceSinks.runs[spec.binding.RunID]
	sequence := 0
	if reg != nil {
		sequence = reg.next
		reg.next++
	}
	traceSinks.Unlock()
	if reg == nil {
		return execute()
	}
	started := time.Now()
	result, err := execute()
	func() {
		defer func() {
			if recover() != nil {
				log.Printf("[work-semantic-trace] emit failed run=%s operation=%s", spec.binding.RunID, spec.operation)
			}
		}()
		event := &jsObj{}
		event.set("schemaVersion", traceSchemaVersion)
		event.set("sequence", float64(sequence))
		event.set("timestamp", jsTime(started.UTC()))
		event.set("runId", spec.binding.RunID)
		event.set("orgId", spec.binding.OrgID)
		event.set("runKind", spec.binding.Kind)
		event.set("source", spec.source)
		logical := err == nil && spec.logicalError != nil && spec.logicalError(result)
		status := "ok"
		if err != nil || logical {
			status = "error"
		}
		event.set("status", status)
		event.set("durationMs", float64(max(0, time.Since(started).Milliseconds())))
		event.set("requestDigest", traceDigest(spec.request))
		if err != nil {
			event.set("errorType", "Error")
			event.set("errorDigest", traceDigest(err.Error()))
		} else {
			event.set("responseDigest", traceDigest(result))
			if logical {
				event.set("errorType", "tool_result_error")
				event.set("errorDigest", traceDigest(result))
			}
		}
		event.set("operation", spec.operation)
		var evidenceOf any
		if err == nil {
			evidenceOf = result
		}
		event.set("evidence", spec.evidence(evidenceOf))
		var content *jsObj
		if reg.scrub != nil {
			content = traceStepContent(sequence, spec, result, err, reg.scrub)
		}
		reg.sink(event, content)
	}()
	return result, err
}

func traceStepContent(sequence int, spec traceSpec, result any, err error, scrub func(string) string) *jsObj {
	content := &jsObj{}
	content.set("sequence", float64(sequence))
	content.set("operation", spec.operation)
	content.set("request", scrubJS(scrub, jsReparse(spec.request)))
	if err != nil {
		content.set("error", scrub(err.Error()))
		return content
	}
	rows, _ := scrubJS(scrub, jsReparse(traceResultRows(result))).([]any)
	kept := []any{}
	size := 2
	for _, row := range rows {
		n := len(jsStringify(row, false))
		if len(kept) > 0 {
			n++
		}
		if size+n > traceStepRowsMax {
			break
		}
		kept = append(kept, row)
		size += n
	}
	content.set("rows", kept)
	content.set("rowCount", float64(len(rows)))
	content.set("truncated", len(kept) < len(rows))
	return content
}

// jsReparse is toJsonValue(): JSON.parse(canonicalJson(v)).
func jsReparse(v any) any {
	out, err := jsParse([]byte(jsCanonical(v)))
	if err != nil {
		return nil
	}
	return out
}

func traceResultRows(result any) []any {
	switch t := result.(type) {
	case nil:
		return []any{}
	case []any:
		return t
	case *jsObj:
		if content, _ := t.vals["content"].([]any); len(content) > 0 {
			rows := []any{}
			for _, entry := range content {
				text, ok := jsNonEmptyString(jsGet(entry, "text"))
				if !ok {
					rows = append(rows, entry)
					continue
				}
				parsed, err := jsParse([]byte(text))
				if err != nil {
					rows = append(rows, text)
					continue
				}
				rows = append(rows, traceResultRows(parsed)...)
			}
			return rows
		}
		if data, ok := t.vals["data"]; ok {
			return traceGraphqlRows(data)
		}
		for _, key := range []string{"matches", "workflows", "rows"} {
			if list, ok := t.vals[key].([]any); ok {
				return list
			}
		}
	}
	return []any{result}
}

func traceGraphqlRows(data any) []any {
	obj, ok := data.(*jsObj)
	if !ok {
		if data == nil {
			return []any{}
		}
		return []any{data}
	}
	rows := []any{}
	for _, k := range obj.ordered() {
		switch field := obj.vals[k].(type) {
		case []any:
			rows = append(rows, field...)
		case nil:
		default:
			rows = append(rows, field)
		}
	}
	return rows
}

func jsGet(v any, key string) any {
	if obj, ok := v.(*jsObj); ok && obj != nil {
		return obj.vals[key]
	}
	return nil
}

func jsNonEmptyString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

func jsArray(v any) []any {
	list, _ := v.([]any)
	return list
}

func traceMemorySearch(binding traceBinding, query string, limit *int, execute func() (any, error)) (any, error) {
	request := &jsObj{}
	request.set("query", query)
	if limit != nil {
		request.set("limit", float64(*limit))
	}
	return traced(traceSpec{
		binding: binding, operation: "memory.search", source: traceBroker, request: request,
		evidence: func(result any) *jsObj {
			rows := jsArray(result)
			ev := &jsObj{}
			if limit != nil {
				ev.set("requestedLimit", float64(*limit))
			}
			ev.set("returnedCount", float64(len(rows)))
			memories := []any{}
			for _, row := range rows {
				memory, ok := jsGet(row, "memory").(*jsObj)
				id, idOK := jsNonEmptyString(jsGet(memory, "id"))
				if !ok || !idOK {
					continue
				}
				m := &jsObj{}
				m.set("id", id)
				m.set("contentDigest", traceDigest(memory.vals["text"]))
				layer := "team"
				if jsTruthy(memory.vals["userId"]) {
					layer = "personal"
				}
				m.set("layer", layer)
				if scope, ok := jsNonEmptyString(memory.vals["scope"]); ok {
					m.set("scope", scope)
				}
				if kind, ok := jsNonEmptyString(memory.vals["kind"]); ok {
					m.set("kind", kind)
				}
				memories = append(memories, m)
			}
			ev.set("memories", memories)
			return ev
		},
	}, execute)
}

func traceFileSearch(binding traceBinding, query string, limit *int, execute func() (any, error)) (any, error) {
	request := &jsObj{}
	request.set("query", query)
	if limit != nil {
		request.set("limit", float64(*limit))
	}
	return traced(traceSpec{
		binding: binding, operation: "files.search", source: traceBroker, request: request,
		evidence: func(result any) *jsObj {
			rows := jsArray(jsGet(result, "matches"))
			ev := &jsObj{}
			if limit != nil {
				ev.set("requestedLimit", float64(*limit))
			}
			ev.set("returnedCount", float64(len(rows)))
			files := []any{}
			for _, row := range rows {
				match, ok := row.(*jsObj)
				id, idOK := jsNonEmptyString(jsGet(match, "path"))
				if !ok || !idOK {
					continue
				}
				f := &jsObj{}
				f.set("id", id)
				f.set("bodyDigest", traceDigest(match.vals["passages"]))
				files = append(files, f)
			}
			ev.set("files", files)
			return ev
		},
	}, execute)
}

// secretScrubber is createScrubber() in packages/llm/src/work/secret-scrubber.ts:
// each value of 8 or more characters becomes [REDACTED], the longest first.
func secretScrubber(values []string) func(string) string {
	var usable []string
	seen := map[string]bool{}
	for _, v := range values {
		if utf16Len(v) >= 8 && !seen[v] {
			seen[v] = true
			usable = append(usable, v)
		}
	}
	if len(usable) == 0 {
		return func(s string) string { return s }
	}
	slices.SortStableFunc(usable, func(x, y string) int { return utf16Len(y) - utf16Len(x) })
	quoted := make([]string, len(usable))
	for i, v := range usable {
		quoted[i] = regexp.QuoteMeta(v)
	}
	rx := regexp.MustCompile(strings.Join(quoted, "|"))
	return func(s string) string { return rx.ReplaceAllLiteralString(s, "[REDACTED]") }
}

func scrubJS(scrub func(string) string, v any) any {
	switch t := v.(type) {
	case string:
		return scrub(t)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = scrubJS(scrub, item)
		}
		return out
	case *jsObj:
		out := &jsObj{}
		for _, k := range t.ordered() {
			out.set(k, scrubJS(scrub, t.vals[k]))
		}
		return out
	}
	return v
}

// jsFromGo turns a Go value into its JSON.parse form, through its JSON.
func jsFromGo(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	out, err := jsParse(raw)
	if err != nil {
		return nil
	}
	return out
}

func traceGraphjinToolsList(binding traceBinding, execute func() (any, error)) (any, error) {
	return traced(traceSpec{
		binding: binding, operation: "graphjin.tools_list", source: traceBroker, request: &jsObj{},
		evidence: func(result any) *jsObj {
			tools := []any{}
			for _, entry := range jsArray(result) {
				tool, ok := entry.(*jsObj)
				name, nameOK := jsNonEmptyString(jsGet(tool, "name"))
				if !ok || !nameOK {
					continue
				}
				tools = append(tools, jsObjOf("name", name, "schemaDigest", traceDigest(tool.vals["inputSchema"])))
			}
			ev := jsObjOf("returnedCount", float64(len(tools)))
			if result != nil {
				ev.set("catalogDigest", traceDigest(result))
			}
			ev.set("tools", tools)
			return ev
		},
	}, execute)
}

func graphjinTraceOperation(tool string) string {
	switch tool {
	case "execute_graphql":
		return "graphjin.execute"
	case "query_catalog":
		return "graphjin.catalog"
	}
	return "graphjin.call"
}

var graphqlLineComment = regexp.MustCompile(`#[^\n\r]*`)
var graphqlOperationWord = regexp.MustCompile(`(?i)^(query|mutation|subscription)\b`)

func graphqlOperationType(query string) string {
	text := jsTrimStart(graphqlLineComment.ReplaceAllString(query, ""))
	if strings.HasPrefix(text, "{") {
		return "query"
	}
	if m := graphqlOperationWord.FindStringSubmatch(text); m != nil {
		return strings.ToLower(m[1])
	}
	return "unknown"
}

func isToolResultError(result any) bool {
	obj, _ := result.(*jsObj)
	if obj == nil {
		return false
	}
	if obj.vals["isError"] == true {
		return true
	}
	for _, entry := range jsArray(obj.vals["content"]) {
		text, ok := jsNonEmptyString(jsGet(entry, "text"))
		if !ok {
			continue
		}
		payload, err := jsParse([]byte(text))
		if err == nil && len(jsArray(jsGet(payload, "errors"))) > 0 {
			return true
		}
	}
	return false
}

// traceGraphjinToolCall takes args nil when the call had no arguments.
func traceGraphjinToolCall(binding traceBinding, tool string, args *jsObj, execute func() (any, error)) (any, error) {
	if args == nil {
		args = &jsObj{}
	}
	operation := graphjinTraceOperation(tool)
	return traced(traceSpec{
		binding: binding, operation: operation, source: traceBroker,
		request: jsObjOf("name", tool, "arguments", args), logicalError: isToolResultError,
		evidence: func(any) *jsObj {
			ev := jsObjOf("toolName", tool)
			if operation != "graphjin.execute" {
				ev.set("argumentsDigest", traceDigest(args))
				return ev
			}
			query, _ := jsNonEmptyString(args.vals["query"])
			ev.set("queryDigest", traceDigest(query))
			if v, ok := args.vals["variables"]; ok {
				ev.set("variablesDigest", traceDigest(v))
			}
			ev.set("operationType", graphqlOperationType(query))
			return ev
		},
	}, execute)
}

func jsTrimStart(s string) string { return strings.TrimLeftFunc(s, jsSpace) }
