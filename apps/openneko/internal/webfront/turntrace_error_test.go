package webfront

import (
	"strings"
	"testing"
)

func TestTraceStepKeepsTheErrorTextOfAFailedToolResult(t *testing.T) {
	reply, _ := jsParse([]byte(`{"content":[{"type":"text","text":"{\"errors\":[{\"message\":\"Field 'totl' is not a column on the queried table; key sk-planted-1\"}]}"}]}`))
	var contents []string
	unregister := registerTraceSink("run-error-trace", func(_, content *jsObj) {
		contents = append(contents, jsStringify(content, false))
	}, secretScrubber([]string{"sk-planted-1"}))
	args, _ := jsParse([]byte(`{"query":"query { orders { totl } }"}`))
	_, _ = traceGraphjinToolCall(traceBinding{RunID: "run-error-trace", OrgID: "org", Kind: "work"}, "execute_graphql", args.(*jsObj),
		func() (any, error) { return reply, nil })
	unregister()
	if len(contents) != 1 || !strings.Contains(contents[0], `"error":"Field 'totl' is not a column on the queried table; key [REDACTED]"`) {
		t.Fatalf("step content: %v", contents)
	}
}
