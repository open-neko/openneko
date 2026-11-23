package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/webfetch"
)

func TestWebFetchRunsInWebWithTheRunsHostsAndALogLine(t *testing.T) {
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })
	fetch, err := webFetchTool([]string{"localhost"}, "org-7", "run-9")
	if err != nil {
		t.Fatal(err)
	}
	s := newToolServer()
	s.webFetch = fetch
	call := func(server *toolServer, url string) (string, *rpcError) {
		params, _ := json.Marshal(map[string]any{"name": webfetch.BridgeTool, "arguments": map[string]string{"url": url}})
		out, rpcErr := server.handle(context.Background(), "tools/call", params)
		raw, _ := json.Marshal(out)
		return string(raw), rpcErr
	}
	for _, c := range []struct{ url, want string }{
		{"https://elsewhere.example/x?q=1", "not on the web allowlist"},
		{"http://localhost:1/", "public addresses only"},
	} {
		out, rpcErr := call(s, c.url)
		if rpcErr != nil || !strings.Contains(out, `"isError":true`) || !strings.Contains(out, c.want) {
			t.Fatalf("%s: %s %v", c.url, out, rpcErr)
		}
		if !strings.Contains(logged.String(), `org="org-7" run="run-9" url="`+c.url+`"`) {
			t.Fatalf("no log line for %s: %s", c.url, logged.String())
		}
	}
	if _, rpcErr := call(newToolServer(), "https://localhost/"); rpcErr == nil || !strings.Contains(rpcErr.Message, "unknown tool") {
		t.Fatalf("a run with no web hosts answered web_fetch: %v", rpcErr)
	}
	if _, err := webFetchTool([]string{"*a.com"}, "o", "r"); err == nil {
		t.Fatal("an invalid host list was accepted")
	}
}
