// Package broker binds scoped OpenNeko capabilities to the standalone harness.
package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/open-neko/harness/internal/agent"
)

// GraphJin binds URL, token and source at the trusted host boundary. Actor input
// is only an instruction; it cannot supply identity, a source, headers or a URL.
func GraphJin(base, token, source string) (func(context.Context, string) (json.RawMessage, error), error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || token == "" {
		return nil, fmt.Errorf("invalid broker binding")
	}
	u.Path = "/v1/harness/lookup"
	u.RawPath = ""
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, instruction string) (json.RawMessage, error) {
		if strings.TrimSpace(instruction) == "" || len(instruction) > 8000 {
			return nil, fmt.Errorf("invalid lookup instruction")
		}
		operationID := agent.OperationID(ctx)
		if operationID == 0 || operationID > 4 {
			return nil, fmt.Errorf("missing or invalid runtime operation ID")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Source      string `json:"dataSourceId,omitempty"`
			MaxSteps    int    `json:"maxSteps"`
		}{operationID, instruction, source, 12})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("invalid broker request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("broker transport failed")
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("broker HTTP status %d", res.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 262145))
		if err != nil || len(data) > 262144 {
			return nil, fmt.Errorf("broker result unreadable or too large")
		}
		var envelope struct {
			Denied   bool            `json:"denied"`
			Error    string          `json:"error"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal(data, &envelope) != nil || (!envelope.Denied && envelope.Error == "" && (len(envelope.Response) == 0 || string(envelope.Response) == "null")) {
			return nil, fmt.Errorf("invalid broker result")
		}
		// Preserve refusal/evidence/trace/usage without reinterpreting or double-counting it.
		return json.RawMessage(data), nil
	}, nil
}
