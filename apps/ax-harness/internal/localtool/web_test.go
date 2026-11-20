package localtool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/webfetch"
)

func TestBridgedWebFetchesThroughTheBridgeOnly(t *testing.T) {
	var asked []string
	call := func(_ context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
		asked = append(asked, name+" "+string(input))
		if strings.Contains(string(input), "blocked") {
			return json.RawMessage(`{"content":["host blocked.example is not on the web allowlist"],"is_error":true}`), nil
		}
		return json.RawMessage(`{"content":["{\"url\":\"https://api.example.com/x\",\"status\":200,\"content\":\"ok\"}"],"is_error":false}`), nil
	}
	web, err := BridgedWeb("api.example.com, *.docs.example.com", call)
	if err != nil {
		t.Fatal(err)
	}
	if web.Name != "web_fetch" || !strings.Contains(web.Description, "api.example.com, *.docs.example.com") {
		t.Fatalf("capability = %s: %s", web.Name, web.Description)
	}
	out, err := web.Call(context.Background(), json.RawMessage(`{"url":"https://api.example.com/x"}`))
	if err != nil || !strings.Contains(string(out), `"status":200`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
	if _, err := web.Call(context.Background(), json.RawMessage(`{"url":"https://blocked.example/"}`)); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("a refusal from web came back as %v", err)
	}
	if len(asked) != 2 || !strings.HasPrefix(asked[0], webfetch.BridgeTool+" ") {
		t.Fatalf("bridge calls = %v", asked)
	}
	for _, bad := range []string{"", "a.com/x", "*a.com"} {
		if _, err := BridgedWeb(bad, call); err == nil {
			t.Fatalf("allowlist %q admitted", bad)
		}
	}
}
