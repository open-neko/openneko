package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func TestBridgeDataBroker(t *testing.T) {
	var name, input string
	answer := ""
	broker := bridgeDataBroker(func(_ context.Context, n string, in json.RawMessage) (json.RawMessage, error) {
		name, input = n, string(in)
		return json.RawMessage(answer), nil
	})
	for _, c := range []struct{ answer, want, err string }{
		{`{"content":["{\"data\":{\"orders\":[{\"id\":1}]}}"],"is_error":false}`, `{"orders":[{"id":1}]}`, ""},
		{`{"content":[""],"structured_content":{"data":{"a":2}},"is_error":false}`, `{"a":2}`, ""},
		{`{"content":["No data source is available to this run."],"is_error":true}`, "", "No data source is available to this run."},
		{`{"content":["{\"errors\":[{\"message\":\"bad\"}]}"],"is_error":false}`, "", `[{"message":"bad"}]`},
		{`{"content":[],"is_error":false}`, "", "no data object"},
	} {
		answer = c.answer
		got, err := broker(context.Background(), sandbox.DataQuery{Query: "query { orders { id } }", Variables: json.RawMessage(`{"x":1}`)})
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %s %v", c.answer, got, err)
			}
		} else if err != nil || string(got) != c.want {
			t.Errorf("%s: %s %v", c.answer, got, err)
		}
	}
	if name != sandbox.DataTool || input != `{"query":"query { orders { id } }","variables":{"x":1}}` {
		t.Fatalf("call %s %s", name, input)
	}
}
