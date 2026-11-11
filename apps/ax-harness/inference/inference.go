// Package inference is the one gateway to model providers, for agent turns
// and the host's own calls.
package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	ax "github.com/ax-llm/ax/packages/go"
)

const OpenAICompatible = "openai-compatible"

type Route struct {
	Provider string            `json:"provider,omitempty"`
	Model    string            `json:"model"`
	URL      string            `json:"url,omitempty"`
	Options  map[string]string `json:"options,omitempty"`
}

func (r Route) ProviderName() string {
	if r.Provider == "" {
		return OpenAICompatible
	}
	return r.Provider
}

func (r Route) Validate() error {
	if r.Model == "" || len(r.Model) > 128 || len(r.Provider) > 64 {
		return errors.New("invalid model route")
	}
	if err := ValidProviderURL(r.ProviderName(), r.URL); err != nil {
		return err
	}
	if !ValidOptions(r.Options) {
		return errors.New("invalid model options")
	}
	return nil
}

// reservedOptions belong to the gateway: the key, the endpoint, the model and the transport.
var reservedOptions = map[string]bool{"api_key": true, "apiKey": true, "base_url": true, "baseUrl": true, "model": true,
	"credential_provider": true, "credentialProvider": true, "transport": true, "runtimeHooks": true, "retry": true}

func ValidOptions(options map[string]string) bool {
	if len(options) > 16 {
		return false
	}
	for key, value := range options {
		if reservedOptions[key] || !validOptionKey(key) || len(value) > 256 {
			return false
		}
	}
	return true
}

func validOptionKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func ValidProviderURL(provider, base string) error {
	if base == "" && provider != OpenAICompatible {
		return nil
	}
	return ValidModelURL(base)
}

func ValidModelURL(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid model URL")
	}
	return nil
}

type Retry struct {
	MaxRetries int
}

// NoRetry leaves retries to the caller. The agent harness owns retries,
// route fallback and the model-call ceiling; a hidden retry would bypass all three.
var NoRetry = Retry{}

var HostRetry = Retry{MaxRetries: 4}

func NewClient(route Route, key string, retry Retry) (client ax.AxAIService, err error) {
	provider := route.ProviderName()
	// Ax checks Azure's endpoint only at the first request; fail at start instead.
	if provider == "azure-openai" && route.URL == "" && (route.Options["resource_name"] == "" || route.Options["deployment_name"] == "") {
		return nil, errors.New("azure-openai needs a URL, or resource_name and deployment_name options")
	}
	options := ax.Object("api_key", key, "model", route.Model, "retry", ax.Object("max_retries", retry.MaxRetries))
	for name, value := range route.Options {
		options[name] = value
	}
	if route.URL != "" {
		options["base_url"] = route.URL
	}
	if provider == OpenAICompatible {
		return ax.NewOpenAICompatibleClient(options), nil
	}
	defer func() {
		if recover() != nil {
			client, err = nil, fmt.Errorf("model provider %q is unsupported or needs a URL", provider)
		}
	}()
	service, ok := ax.NewAI(provider, options).(ax.AxAIService)
	if !ok {
		return nil, fmt.Errorf("model provider %q is unsupported", provider)
	}
	return service, nil
}

type Request struct {
	Route Route `json:"route"`
	// Fallback serves the call only after the route fails with an error Ax
	// classifies as retryable, once the route's own retries are spent.
	Fallback    *Route         `json:"fallback,omitempty"`
	Signature   string         `json:"signature"`
	Instruction string         `json:"instruction,omitempty"`
	Values      map[string]any `json:"values"`
}

type Result struct {
	Outputs  map[string]any `json:"outputs"`
	Usage    Usage          `json:"usage"`
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Fallback bool           `json:"fallback,omitempty"`
}

// Requests counts calls with no provider usage too, so coverage stays honest.
type Usage struct {
	Requests         int    `json:"requests"`
	Reported         int    `json:"reported"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64  `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64  `json:"reasoning_tokens,omitempty"`
	Coverage         string `json:"coverage"`
}

func (u *Usage) Add(other Usage) {
	u.Reported += other.Reported
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.TotalTokens += other.TotalTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheWriteTokens += other.CacheWriteTokens
	u.ReasoningTokens += other.ReasoningTokens
}

func (u *Usage) setCoverage() {
	switch {
	case u.Requests > 0 && u.Reported == u.Requests:
		u.Coverage = "complete"
	case u.Reported > 0:
		u.Coverage = "partial"
	default:
		u.Coverage = "unavailable"
	}
}

func Tokens(response ax.Value) (Usage, bool) {
	value, ok := response.(map[string]ax.Value)
	if !ok {
		return Usage{}, false
	}
	model, ok := value["model_usage"].(map[string]ax.Value)
	if !ok {
		return Usage{}, false
	}
	raw, err := json.Marshal(model["tokens"])
	if err != nil || string(raw) == "null" {
		return Usage{}, false
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
		return Usage{}, false
	}
	usage := Usage{Reported: 1, InputTokens: *tokens.Input, OutputTokens: *tokens.Output, TotalTokens: *tokens.Total}
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
			return Usage{}, false
		}
	}
	return usage, true
}

const (
	maxSignatureBytes   = 8 << 10
	maxInstructionBytes = 32 << 10
)

func CheckSignature(signature string) (err error) {
	if strings.TrimSpace(signature) == "" || len(signature) > maxSignatureBytes {
		return errors.New("invalid program signature")
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid program signature: %v", r)
		}
	}()
	ax.NewSignature(signature)
	return nil
}

func Generate(ctx context.Context, request Request, key, fallbackKey string) (Result, error) {
	if err := request.Route.Validate(); err != nil {
		return Result{}, err
	}
	if request.Fallback != nil {
		if err := request.Fallback.Validate(); err != nil {
			return Result{}, fmt.Errorf("fallback: %w", err)
		}
	}
	if len(request.Instruction) > maxInstructionBytes {
		return Result{}, errors.New("program instruction too long")
	}
	if err := CheckSignature(request.Signature); err != nil {
		return Result{}, err
	}
	result, err := generateOn(ctx, request, request.Route, key)
	if err == nil || request.Fallback == nil || ctx.Err() != nil || !ax.IsRetryable(err) {
		return result, redact(err, key)
	}
	failed := result.Usage
	result, err = generateOn(ctx, request, *request.Fallback, fallbackKey)
	result.Usage.Requests += failed.Requests
	result.Usage.Add(failed)
	result.Usage.setCoverage()
	result.Fallback = err == nil
	return result, redact(redact(err, fallbackKey), key)
}

func generateOn(ctx context.Context, request Request, route Route, key string) (Result, error) {
	client, err := NewClient(route, key, HostRetry)
	if err != nil {
		return Result{}, err
	}
	values := make(map[string]ax.Value, len(request.Values))
	for name, value := range request.Values {
		values[name] = value
	}
	var mu sync.Mutex
	usage := Usage{}
	limiter := ax.AxRateLimiterFunc(func(next ax.AxRequestExecutor, _ ax.AxRateLimitInfo) (ax.Value, error) {
		response, err := next()
		mu.Lock()
		usage.Requests++
		if tokens, ok := Tokens(response); ok {
			usage.Add(tokens)
		}
		mu.Unlock()
		return response, err
	})
	gen := ax.NewAx(request.Signature, ax.Object("instruction", request.Instruction))
	out, err := gen.ForwardWithHooks(ctx, client, values, ax.Object(), ax.AxRuntimeHooks{RateLimiter: limiter})
	mu.Lock()
	usage.setCoverage()
	mu.Unlock()
	if err != nil {
		return Result{Usage: usage}, err
	}
	outputs, ok := out.(map[string]ax.Value)
	if !ok {
		return Result{Usage: usage}, errors.New("model returned no program output")
	}
	result := Result{Outputs: make(map[string]any, len(outputs)), Usage: usage, Provider: route.ProviderName(), Model: route.Model}
	for name, value := range outputs {
		result.Outputs[name] = value
	}
	return result, nil
}

// redact keeps the key out of error text, which reaches logs and the caller.
func redact(err error, key string) error {
	if err == nil || key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[redacted]"))
}

func parseSignature(signature string) ax.AxSignature { return ax.NewSignature(signature) }
