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

// MemorySave sends only model-selected memory content. The broker supplies the
// actor, run and thread, and journals the effect before it writes anything.
func MemorySave(base, token string) (func(context.Context, json.RawMessage, string) (json.RawMessage, error), error) {
	call, err := bind(base, token, "/v1/harness/memory/save")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, input json.RawMessage, binding string) (json.RawMessage, error) {
		id := agent.OperationID(ctx)
		if id < 1 || id > 32 || len(binding) != 64 || len(input) == 0 || len(input) > 4096 || !json.Valid(input) {
			return nil, fmt.Errorf("invalid memory save operation")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Binding     string `json:"binding"`
		}{id, string(input), binding})
		data, err := call(ctx, body)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			OK       bool   `json:"ok"`
			MemoryID string `json:"memoryId"`
		}
		if json.Unmarshal(data, &receipt) != nil || !receipt.OK || receipt.MemoryID == "" {
			return nil, fmt.Errorf("memory save was not confirmed by broker")
		}
		return data, nil
	}, nil
}

// SkillCreate publishes a new org skill through the trusted host. The host
// checks the current Work actor, journals intent and stages files before publish.
func SkillCreate(base, token string) (func(context.Context, json.RawMessage, string) (json.RawMessage, error), error) {
	call, err := bind(base, token, "/v1/harness/skill/create")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, input json.RawMessage, binding string) (json.RawMessage, error) {
		id := agent.OperationID(ctx)
		if id < 1 || id > 32 || len(binding) != 64 || len(input) == 0 || len(input) > 131072 || !json.Valid(input) {
			return nil, fmt.Errorf("invalid skill create operation")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Binding     string `json:"binding"`
		}{id, string(input), binding})
		data, err := call(ctx, body)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			OK   bool   `json:"ok"`
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &receipt) != nil || !receipt.OK || receipt.Name == "" {
			return nil, fmt.Errorf("skill create was not confirmed by broker")
		}
		return data, nil
	}, nil
}

func SkillInspect(base, token string) (func(context.Context, json.RawMessage) (json.RawMessage, error), error) {
	call, err := bind(base, token, "/v1/harness/skill/inspect")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		if len(input) == 0 || len(input) > 512 || !json.Valid(input) {
			return nil, fmt.Errorf("invalid skill inspection")
		}
		data, err := call(ctx, input)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			OK      bool   `json:"ok"`
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &receipt) != nil || !receipt.OK || len(receipt.Version) != 64 {
			return nil, fmt.Errorf("skill inspection was not confirmed by broker")
		}
		return data, nil
	}, nil
}

func SkillUpdate(base, token string) (func(context.Context, json.RawMessage, string) (json.RawMessage, error), error) {
	call, err := bind(base, token, "/v1/harness/skill/update")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, input json.RawMessage, binding string) (json.RawMessage, error) {
		id := agent.OperationID(ctx)
		if id < 1 || id > 32 || len(binding) != 64 || len(input) == 0 || len(input) > 131072 || !json.Valid(input) {
			return nil, fmt.Errorf("invalid skill update operation")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Binding     string `json:"binding"`
		}{id, string(input), binding})
		data, err := call(ctx, body)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			OK      bool   `json:"ok"`
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &receipt) != nil || !receipt.OK || len(receipt.Version) != 64 {
			return nil, fmt.Errorf("skill update was not confirmed by broker")
		}
		return data, nil
	}, nil
}

// WorkflowSave submits a version-guarded definition to the host. The broker
// supplies run identity and journals the effect before OpenNeko persists it.
func WorkflowSave(base, token string) (func(context.Context, json.RawMessage, string) (json.RawMessage, error), error) {
	call, err := bind(base, token, "/v1/harness/workflow/save")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, input json.RawMessage, binding string) (json.RawMessage, error) {
		id := agent.OperationID(ctx)
		if id < 1 || id > 32 || len(binding) != 64 || len(input) == 0 || len(input) > 65536 || !json.Valid(input) {
			return nil, fmt.Errorf("invalid workflow save operation")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Binding     string `json:"binding"`
		}{id, string(input), binding})
		data, err := call(ctx, body)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			OK         bool   `json:"ok"`
			WorkflowID string `json:"workflowId"`
			Error      string `json:"error"`
		}
		if json.Unmarshal(data, &receipt) != nil || (!receipt.OK && receipt.Error == "" && receipt.WorkflowID == "") {
			return nil, fmt.Errorf("workflow save was not confirmed by broker")
		}
		return data, nil
	}, nil
}

// WorkflowDelete sends a revision-bound deletion request. The host verifies a
// matching confirmation in the current user message before any hard cascade.
func WorkflowDelete(base, token string) (func(context.Context, json.RawMessage, string) (json.RawMessage, error), error) {
	call, err := bind(base, token, "/v1/harness/workflow/delete")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, input json.RawMessage, binding string) (json.RawMessage, error) {
		id := agent.OperationID(ctx)
		if id < 1 || id > 32 || len(binding) != 64 || len(input) == 0 || len(input) > 4096 || !json.Valid(input) {
			return nil, fmt.Errorf("invalid workflow delete operation")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Binding     string `json:"binding"`
		}{id, string(input), binding})
		data, err := call(ctx, body)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &receipt) != nil || (!receipt.OK && receipt.Error == "") {
			return nil, fmt.Errorf("workflow delete was not confirmed by broker")
		}
		return data, nil
	}, nil
}

// RuleSave submits a version-guarded policy change to the host, where current
// admin authority and the durable operation are checked before persistence.
func RuleSave(base, token string) (func(context.Context, json.RawMessage, string) (json.RawMessage, error), error) {
	call, err := bind(base, token, "/v1/harness/rule/save")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, input json.RawMessage, binding string) (json.RawMessage, error) {
		id := agent.OperationID(ctx)
		if id < 1 || id > 32 || len(binding) != 64 || len(input) == 0 || len(input) > 65536 || !json.Valid(input) {
			return nil, fmt.Errorf("invalid rule save operation")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Binding     string `json:"binding"`
		}{id, string(input), binding})
		data, err := call(ctx, body)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			OK     bool   `json:"ok"`
			RuleID string `json:"ruleId"`
			Error  string `json:"error"`
		}
		if json.Unmarshal(data, &receipt) != nil || (!receipt.OK && receipt.Error == "" && receipt.RuleID == "") {
			return nil, fmt.Errorf("rule save was not confirmed by broker")
		}
		return data, nil
	}, nil
}

// WorkflowOutput records a queued run's output under the host-bound workflow
// identity. The host journals the effect before persisting it.
func WorkflowOutput(base, token string) (func(context.Context, json.RawMessage, string) (json.RawMessage, error), error) {
	call, err := bind(base, token, "/v1/harness/workflow-output/emit")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, input json.RawMessage, binding string) (json.RawMessage, error) {
		id := agent.OperationID(ctx)
		if id < 1 || id > 32 || len(binding) != 64 || len(input) == 0 || len(input) > 131072 || !json.Valid(input) {
			return nil, fmt.Errorf("invalid workflow output operation")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Binding     string `json:"binding"`
		}{id, string(input), binding})
		data, err := call(ctx, body)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			OK       bool   `json:"ok"`
			OutputID string `json:"outputId"`
			Kind     string `json:"kind"`
		}
		if json.Unmarshal(data, &receipt) != nil || !receipt.OK || receipt.OutputID == "" || receipt.Kind == "" {
			return nil, fmt.Errorf("workflow output was not confirmed by broker")
		}
		return data, nil
	}, nil
}

// ProcessRun requests a host-owned isolated process. The broker supplies the
// run workspace and executable; model input can contain only a bounded script,
// selected upload basenames and declared output basenames.
func ProcessRun(base, token string) (func(context.Context, json.RawMessage, string) (json.RawMessage, error), error) {
	call, err := bindTimeout(base, token, "/v1/harness/process/run", 262144, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, input json.RawMessage, binding string) (json.RawMessage, error) {
		id := agent.OperationID(ctx)
		if id < 1 || id > 32 || len(binding) != 64 || len(input) == 0 || len(input) > 131072 || !json.Valid(input) {
			return nil, fmt.Errorf("invalid isolated process operation")
		}
		body, _ := json.Marshal(struct {
			OperationID uint64 `json:"operationId"`
			Instruction string `json:"instruction"`
			Binding     string `json:"binding"`
		}{id, string(input), binding})
		data, err := call(ctx, body)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			OK    bool `json:"ok"`
			Files []struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
			} `json:"files"`
		}
		if json.Unmarshal(data, &receipt) != nil || !receipt.OK || len(receipt.Files) == 0 || len(receipt.Files) > 16 {
			return nil, fmt.Errorf("isolated process was not confirmed by broker")
		}
		for _, file := range receipt.Files {
			if file.Path == "" || len(file.SHA256) != 64 {
				return nil, fmt.Errorf("invalid isolated process artifact receipt")
			}
		}
		return data, nil
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
	timeout := 45 * time.Second
	if maxResponse > 262144 {
		timeout = 65 * time.Second
	}
	return bindTimeout(base, token, path, maxResponse, timeout)
}

func bindTimeout(base, token, path string, maxResponse int64, timeout time.Duration) (func(context.Context, []byte) ([]byte, error), error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || token == "" {
		return nil, fmt.Errorf("invalid broker binding")
	}
	u.Path = path
	u.RawPath = ""
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
