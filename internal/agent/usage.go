package agent

import (
	"encoding/json"

	ax "github.com/ax-llm/ax/packages/go"
)

// ModelUsage is content-free accounting. Requests includes calls with no
// provider usage, so coverage remains honest after a crash or missing report.
type ModelUsage struct {
	Requests         int    `json:"requests,omitempty"`
	Reported         int    `json:"reported,omitempty"`
	InputTokens      int64  `json:"input_tokens,omitempty"`
	OutputTokens     int64  `json:"output_tokens,omitempty"`
	TotalTokens      int64  `json:"total_tokens,omitempty"`
	CacheReadTokens  int64  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64  `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64  `json:"reasoning_tokens,omitempty"`
	Coverage         string `json:"coverage,omitempty"`
}

// RemoteUsage is one GraphJin agent lookup's aggregate receipt. ChargedTokens
// is the amount used for admission; it is a reservation when Reported is false.
// It is separate from outer Ax model usage so consumers cannot double-count it.
type RemoteUsage struct {
	PromptTokens     int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens int64 `json:"completion_tokens,omitempty"`
	TotalTokens      int64 `json:"total_tokens,omitempty"`
	LLMCalls         int64 `json:"llm_calls,omitempty"`
	ChargedTokens    int64 `json:"charged_tokens"`
	Reported         bool  `json:"reported"`
}

type stageUsageRow struct {
	Name  string
	Usage ModelUsage
}

// Ax's chat log contains complete prompts. This projection keeps only stage
// identity and token counters. It is telemetry, never an admission receipt.
func stageUsageProjection(chatLog ax.Value, prefix string) []stageUsageRow {
	var entries []ax.Value
	switch value := chatLog.(type) {
	case *ax.AxArray:
		entries = value.Items
	case []ax.Value:
		entries = value
	default:
		return nil
	}
	byName := map[string]*ModelUsage{}
	for _, entry := range entries {
		row, ok := entry.(map[string]ax.Value)
		if !ok {
			continue
		}
		name, _ := row["name"].(string)
		if name != "distiller" && name != "executor" && name != "responder" {
			continue
		}
		name = prefix + name
		u := byName[name]
		if u == nil {
			u = &ModelUsage{}
			byName[name] = u
		}
		u.Requests++
		if reported, ok := modelTokens(ax.Object("model_usage", ax.Object("tokens", row["usage"]))); ok {
			u.AddReported(reported)
		}
	}
	result := make([]stageUsageRow, 0, len(byName))
	for _, stage := range []string{"distiller", "executor", "responder"} {
		name := prefix + stage
		if u := byName[name]; u != nil {
			u.setCoverage()
			result = append(result, stageUsageRow{Name: name, Usage: *u})
		}
	}
	return result
}

// GraphJinRemoteUsage reads only the broker's flat response.usage. Nested
// evidence may contain unrelated usage and is deliberately ignored.
func GraphJinRemoteUsage(raw json.RawMessage) RemoteUsage {
	missing := RemoteUsage{ChargedTokens: remoteLookupReservation}
	var result struct {
		Response struct {
			Usage struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
				LLMCalls         int64 `json:"llm_calls"`
			} `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return missing
	}
	u := result.Response.Usage
	for _, count := range []int64{u.PromptTokens, u.CompletionTokens, u.TotalTokens, u.LLMCalls} {
		if count < 0 || count > 1_000_000_000_000 {
			return missing
		}
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	if u.TotalTokens <= 0 || u.TotalTokens > 1_000_000_000_000 {
		return missing
	}
	return RemoteUsage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
		TotalTokens: u.TotalTokens, LLMCalls: u.LLMCalls, ChargedTokens: u.TotalTokens, Reported: true}
}

func (u *ModelUsage) AddReported(other ModelUsage) {
	u.Reported += other.Reported
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.TotalTokens += other.TotalTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheWriteTokens += other.CacheWriteTokens
	u.ReasoningTokens += other.ReasoningTokens
}

func (u *ModelUsage) setCoverage() {
	switch {
	case u.Requests > 0 && u.Reported == u.Requests:
		u.Coverage = "complete"
	case u.Reported > 0:
		u.Coverage = "partial"
	default:
		u.Coverage = "unavailable"
	}
}

func modelTokens(response ax.Value) (ModelUsage, bool) {
	value, ok := response.(map[string]ax.Value)
	if !ok {
		return ModelUsage{}, false
	}
	model, ok := value["model_usage"].(map[string]ax.Value)
	if !ok {
		return ModelUsage{}, false
	}
	raw, err := json.Marshal(model["tokens"])
	if err != nil || string(raw) == "null" {
		return ModelUsage{}, false
	}
	var tokens struct {
		Input      *int64 `json:"prompt_tokens"`
		Output     *int64 `json:"completion_tokens"`
		Total      *int64 `json:"total_tokens"`
		CacheRead  *int64 `json:"cache_read_tokens"`
		CacheWrite *int64 `json:"cache_creation_tokens"`
		Reasoning  *int64 `json:"reasoning_tokens"`
	}
	if json.Unmarshal(raw, &tokens) != nil || tokens.Input == nil || tokens.Output == nil || tokens.Total == nil {
		return ModelUsage{}, false
	}
	usage := ModelUsage{Reported: 1, InputTokens: *tokens.Input, OutputTokens: *tokens.Output, TotalTokens: *tokens.Total}
	if tokens.CacheRead != nil {
		usage.CacheReadTokens = *tokens.CacheRead
	}
	if tokens.CacheWrite != nil {
		usage.CacheWriteTokens = *tokens.CacheWrite
	}
	if tokens.Reasoning != nil {
		usage.ReasoningTokens = *tokens.Reasoning
	}
	const maxTokens = 1_000_000_000_000
	for _, count := range []int64{usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens} {
		if count < 0 || count > maxTokens {
			return ModelUsage{}, false
		}
	}
	return usage, true
}
