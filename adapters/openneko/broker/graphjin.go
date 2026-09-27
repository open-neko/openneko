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
	call, err := bind(base, token, "/v1/harness/lookup")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, instruction string) (json.RawMessage, error) {
		if strings.TrimSpace(instruction) == "" || len(instruction) > 8000 {
			return nil, fmt.Errorf("invalid lookup instruction")
		}
		operationID := agent.OperationID(ctx)
		if operationID == 0 || operationID > 32 {
			return nil, fmt.Errorf("missing or invalid runtime operation ID")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Source      string `json:"dataSourceId,omitempty"`
			MaxSteps    int    `json:"maxSteps"`
		}{operationID, instruction, source, 12})
		data, err := call(ctx, body)
		if err != nil {
			return nil, err
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

// Propose submits only a request for approval. The broker owns policy and identity.
func Propose(base, token string) (func(context.Context, agent.Proposal) (agent.ProposalReceipt, error), error) {
	call, err := bind(base, token, "/v1/harness/propose")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, proposal agent.Proposal) (agent.ProposalReceipt, error) {
		var receipt agent.ProposalReceipt
		raw, err := json.Marshal(proposal)
		if err != nil {
			return receipt, fmt.Errorf("invalid proposal")
		}
		if _, err = agent.ParseProposal(raw); err != nil {
			return receipt, err
		}
		id := agent.OperationID(ctx)
		if id < 1 || id > 32 {
			return receipt, fmt.Errorf("missing or invalid runtime operation ID")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
		}{id, string(raw)})
		data, err := call(ctx, body)
		if err != nil {
			return receipt, err
		}
		return agent.ParseProposalReceipt(data)
	}, nil
}

// GraphQLQuery is for an admitted file-backed batch runner only. OpenNeko
// rechecks the bound actor and enforces read-only GraphQL at the broker.
func GraphQLQuery(base, token string) (func(context.Context, string) ([]byte, error), error) {
	call, err := bindLimit(base, token, "/v1/graphjin/query", 16<<20)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, query string) ([]byte, error) {
		if strings.TrimSpace(query) == "" || len(query) > 60000 {
			return nil, fmt.Errorf("invalid batch query")
		}
		body, _ := json.Marshal(struct {
			Query string `json:"query"`
		}{query})
		return call(ctx, body)
	}, nil
}

func bind(base, token, path string) (func(context.Context, []byte) ([]byte, error), error) {
	return bindLimit(base, token, path, 262144)
}

func bindLimit(base, token, path string, maxResponse int64) (func(context.Context, []byte) ([]byte, error), error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || token == "" {
		return nil, fmt.Errorf("invalid broker binding")
	}
	u.Path = path
	u.RawPath = ""
	timeout := 45 * time.Second
	if maxResponse > 262144 {
		timeout = 65 * time.Second
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, body []byte) ([]byte, error) {
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
		data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
		if err != nil || int64(len(data)) > maxResponse {
			return nil, fmt.Errorf("broker result unreadable or too large")
		}
		return data, nil
	}, nil
}
