package webfront

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/open-neko/openneko/apps/ax-harness/webfetch"
)

// webFetchTool answers webfetch.BridgeTool with web's own fetch, limited to
// hosts, and logs each URL with the org and the run.
func webFetchTool(hosts []string, org, run string) (func(ctx context.Context, raw json.RawMessage) json.RawMessage, error) {
	web, err := webfetch.Open(strings.Join(hosts, ","))
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, raw json.RawMessage) json.RawMessage {
		var input struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(raw, &input)
		out, err := web.Fetch(ctx, raw)
		if err != nil {
			log.Printf("[web_fetch] org=%q run=%q url=%q refused or failed: %v", org, run, input.URL, err)
			return webFetchResult(err.Error(), true)
		}
		var answer struct {
			Status int `json:"status"`
		}
		_ = json.Unmarshal(out, &answer)
		log.Printf("[web_fetch] org=%q run=%q url=%q status=%d", org, run, input.URL, answer.Status)
		return webFetchResult(string(out), false)
	}, nil
}

func webFetchResult(text string, isError bool) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": isError})
	return raw
}
